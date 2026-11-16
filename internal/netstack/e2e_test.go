package netstack_test

// End-to-end integration harness: the complete local data path of ntun,
// in-process, without TUN or SSH:
//
//   gonet client (inside a gVisor stack)
//     → link endpoint over a loopback "TUN" (outbound → inbound, exactly how
//       the kernel feeds ntun)
//     → TCP/UDP forwarders
//     → tunnel.Handler (real)
//     → local mux.Session ←net.Pipe→ agent mux.Session (agent.Run)
//     → fixed dialer → real TCP/UDP servers on 127.0.0.1
//
// This is where field-reported bugs (truncated transfers, the psql wedge)
// must reproduce or be disproved deterministically.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"ntun-ng/internal/agent"
	"ntun-ng/internal/mux"
	"ntun-ng/internal/netstack"
	"ntun-ng/internal/tunnel"
)

// loopRW is a TUN loopback: everything the stack writes is served back to the
// dispatch loop, mirroring the kernel <-> ntun path.
type loopRW struct {
	mu     sync.Mutex
	ready  chan struct{}
	q      [][]byte
	closed bool
}

func (r *loopRW) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		if len(r.q) > 0 {
			next := r.q[0]
			r.q = r.q[1:]
			r.mu.Unlock()
			return copy(p, next), nil
		}
		if r.closed {
			r.mu.Unlock()
			return 0, net.ErrClosed
		}
		ready := r.ready
		r.mu.Unlock()
		<-ready
	}
}

func (r *loopRW) Write(p []byte) (int, error) {
	cp := append([]byte(nil), p...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, net.ErrClosed
	}
	r.q = append(r.q, cp)
	if len(r.q) == 1 {
		close(r.ready)
		r.ready = make(chan struct{})
	}
	return len(p), nil
}

func (r *loopRW) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.ready)
	}
	return nil
}

// fixedDialer ignores the dialed address (a fake remote IP from the TUN side)
// and always dials the fixed local test target.
type fixedDialer struct{ target string }

func (d fixedDialer) Dial(network, _ string) (net.Conn, error) {
	return net.Dial(network, d.target)
}

// muxDialer adapts a *mux.Session to tunnel.Dialer.
type muxDialer struct{ sess *mux.Session }

func (d *muxDialer) Open(a mux.Addr) (*mux.Conn, error)       { return d.sess.Open(a) }
func (d *muxDialer) OpenUDP(a mux.Addr) (*mux.UDPConn, error) { return d.sess.OpenUDP(a) }

// e2e wires the full stack once; cleanup closes everything.
type e2e struct {
	t     *testing.T
	stack *stack.Stack
}

func newE2E(t *testing.T, tcpTarget, udpTarget string) *e2e {
	t.Helper()

	localEnd, agentEnd := net.Pipe()
	localSess := mux.NewSession(localEnd, mux.SessionConfig{})
	go func() { _ = localSess.Serve() }()
	go func() {
		_ = agent.Run(agentEnd, agent.Config{
			Dialer:    fixedDialer{target: tcpTarget},
			UDPDialer: fixedDialer{target: udpTarget},
		})
	}()

	tnl := tunnel.New(tunnel.Options{
		Dialer:     &muxDialer{sess: localSess},
		UDPTimeout: 30 * time.Second,
	})

	rw := &loopRW{ready: make(chan struct{})}
	link, err := netstack.NewLinkEndpoint(rw, 1500, 0)
	if err != nil {
		t.Fatalf("NewLinkEndpoint: %v", err)
	}
	stk, err := netstack.CreateStack(netstack.Config{Link: link, Handler: tnl})
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	tnl.AttachStack(stk)

	if err := stk.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address: tcpip.AddrFrom4([4]byte{10, 9, 9, 9}), PrefixLen: 24,
		},
	}, stack.AddressProperties{}); err != nil {
		t.Fatalf("AddProtocolAddress: %v", err)
	}

	t.Cleanup(func() {
		rw.Close()
		link.Close()
		_ = localSess.Close()
		tnl.Close()
		stk.Wait()
	})

	return &e2e{t: t, stack: stk}
}

// dialTCPClient returns a gonet TCP conn to fakeDst through the stack.
func (e *e2e) dialTCPClient(fakeDst [4]byte, port uint16) *gonet.TCPConn {
	e.t.Helper()
	conn, err := gonet.DialTCP(e.stack, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4(fakeDst),
		Port: port,
	}, ipv4.ProtocolNumber)
	if err != nil {
		e.t.Fatalf("DialTCP: %v", err)
	}
	return conn
}

// startTCPEcho runs a plain echo server.
func startTCPEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// startUDPEcho echoes datagrams.
func startUDPEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	return pc.LocalAddr().String()
}

// --- Tests -----------------------------------------------------------------

// TestE2ETCPEchoRoundTrip: 512KB through the whole chain, integrity checked.
func TestE2ETCPEchoRoundTrip(t *testing.T) {
	e := newE2E(t, startTCPEcho(t), startUDPEcho(t))
	conn := e.dialTCPClient([4]byte{1, 2, 3, 4}, 5432)
	defer conn.Close()

	payload := make([]byte, 512*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	go func() {
		if _, err := conn.Write(payload); err != nil {
			t.Errorf("client write: %v", err)
		}
	}()

	got := make([]byte, len(payload))
	if n, err := readFullDeadline(t, conn, got, 30*time.Second); err != nil {
		t.Fatalf("read back after %d bytes: %v", n, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("echo mismatch")
	}
}

// TestE2EServerCloseDrainsAllData: server streams 2MB then FINs immediately —
// the truncated-document scenario. The client must receive every byte before
// EOF.
func TestE2EServerCloseDrainsAllData(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	payload := bytes.Repeat([]byte{0xAB}, 2*1024*1024)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() { _, _ = io.Copy(io.Discard, c) }()
		_, _ = c.Write(payload)
		_ = c.Close()
	}()

	e := newE2E(t, ln.Addr().String(), startUDPEcho(t))
	conn := e.dialTCPClient([4]byte{1, 2, 3, 4}, 5432)
	defer conn.Close()

	got := make([]byte, len(payload))
	if n, err := readFullDeadline(t, conn, got, 30*time.Second); err != nil {
		t.Fatalf("stream truncated/stalled after %d of %d bytes: %v", n, len(payload), err)
	}
}

// TestE2EUDPExchange: a DNS-style UDP round trip through the forwarder.
func TestE2EUDPExchange(t *testing.T) {
	e := newE2E(t, startTCPEcho(t), startUDPEcho(t))
	conn, err := gonet.DialUDP(e.stack, nil, &tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4([4]byte{8, 8, 8, 8}),
		Port: 53,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	query := []byte("fake-dns-query")
	if _, err := conn.Write(query); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	buf := make([]byte, 1500)
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("udp read: %v (DNS/UDP path broken)", err)
	}
	if !bytes.Equal(buf[:n], query) {
		t.Fatalf("udp echo mismatch: %q", buf[:n])
	}
}

// TestE2EOverflowThenRecover reproduces the psql report shape: one connection
// floods past the mux receive cap (slow consumer) and gets killed — and both
// the session and new connections must keep working. Before the write-path
// fix the whole tunnel wedged here.
func TestE2EOverflowThenRecover(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var connCount atomic.Int32
	payload := bytes.Repeat([]byte{0xCD}, 24*1024*1024) // >> recvCh capacity
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if connCount.Add(1) == 1 {
				// First conn: flood + close.
				go func(c net.Conn) {
					defer c.Close()
					go func() { _, _ = io.Copy(io.Discard, c) }()
					_, _ = c.Write(payload)
				}(c)
				continue
			}
			// Later conns: plain echo.
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	e := newE2E(t, ln.Addr().String(), startUDPEcho(t))

	// Slow consumer: tiny reads with pauses, so the flood overflows the
	// mux recv cap while the server blasts.
	conn := e.dialTCPClient([4]byte{1, 2, 3, 4}, 5432)
	buf := make([]byte, 4*1024)
	for {
		n, err := conn.Read(buf)
		_ = n
		if err != nil {
			break // conn killed by overflow — expected
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close()

	// The tunnel MUST survive: a new conn must echo cleanly.
	conn2 := e.dialTCPClient([4]byte{5, 6, 7, 8}, 9999)
	defer conn2.Close()
	msg := []byte("still-alive")
	if _, err := conn2.Write(msg); err != nil {
		t.Fatalf("post-overflow write failed: %v (session wedged — psql regression)", err)
	}
	got := make([]byte, len(msg))
	if n, err := readFullDeadline(t, conn2, got, 10*time.Second); err != nil {
		t.Fatalf("post-overflow read failed after %d bytes: %v (session wedged — psql regression)", n, err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("post-overflow echo mismatch: %q", got)
	}
}

// --- helpers ---------------------------------------------------------------

func readFullDeadline(t *testing.T, r io.Reader, buf []byte, d time.Duration) (int, error) {
	t.Helper()
	type res struct {
		n   int
		err error
	}
	done := make(chan res, 1)
	go func() {
		n, err := io.ReadFull(r, buf)
		done <- res{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(d):
		return 0, fmt.Errorf("timeout after %v", d)
	}
}
