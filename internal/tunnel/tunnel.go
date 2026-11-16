// Package tunnel bridges the gVisor netstack to the mux relay: every TCP/UDP
// connection accepted by the netstack is paired with a mux.Conn dialed
// through the relay to the remote agent, which dials the real target.
//
// It is the local-side equivalent of tun2socks's tunnel package, but uses the
// ntun mux protocol (no SOCKS5, no localhost hop).
package tunnel

import (
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"ntun-ng/internal/mux"
	"ntun-ng/internal/netstack"
)

// Dialer is the subset of *relay.Relay used by Tunnel (Open / OpenUDP).
type Dialer interface {
	Open(addr mux.Addr) (*mux.Conn, error)
	OpenUDP(addr mux.Addr) (*mux.UDPConn, error)
}

// Logger is the minimal logging surface.
type Logger interface {
	Printf(format string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Options configures the Tunnel.
type Options struct {
	Dialer Dialer
	Log    Logger
	// DNSHijack, when true, redirects UDP/53 on the TUN to DNSRemote instead
	// of the original resolver IP.
	DNSHijack bool
	// DNSRemote is the resolver (host:port) the agent forwards hijacked DNS to.
	// Empty => use the original destination resolver (relayed as-is).
	DNSRemote string
}

// Tunnel implements netstack.Handler.
type Tunnel struct {
	opts Options
	log  Logger

	mu     sync.Mutex
	stack  *stack.Stack
	closed bool
}

var _ netstack.Handler = (*Tunnel)(nil)

// New returns a Tunnel. CreateStack attaches it as the transport handler.
func New(opts Options) *Tunnel {
	if opts.Log == nil {
		opts.Log = discardLogger{}
	}
	return &Tunnel{
		opts: opts,
		log:  opts.Log,
	}
}

// dnsTarget decides which mux.Addr a UDP datagram to (dstIP:dstPort) should be
// sent to. If DNS hijack is on and dstPort==53, the target becomes DNSRemote
// (parsed); otherwise the original destination.
func (t *Tunnel) dnsTarget(dst mux.Addr) mux.Addr {
	if !t.opts.DNSHijack || dst.Port != 53 {
		return dst
	}
	if t.opts.DNSRemote == "" {
		return dst // no remote resolver configured: relay to original dst
	}
	host, port := splitHostPortDefault(t.opts.DNSRemote, 53)
	return mux.FromHostPort(host, uint16(port))
}

// AttachStack records the stack so Close can shut it down.
func (t *Tunnel) AttachStack(s *stack.Stack) {
	t.mu.Lock()
	t.stack = s
	t.mu.Unlock()
}

// HandleTCP pipes a netstack TCP conn through a mux conn to the agent.
func (t *Tunnel) HandleTCP(c netstack.TCPConn) {
	defer c.Close()
	id := c.ID()

	// The destination the application tried to reach is the netstack "local"
	// address (the TUN's addr); the source is the app's ephemeral port.
	dst := tcpipAddrToMux(id.LocalAddress, id.LocalPort)
	if dst.Type == 0 {
		t.log.Printf("tunnel: tcp: undialable destination %s", id.LocalAddress)
		return
	}

	remote, err := t.opts.Dialer.Open(dst)
	if err != nil {
		t.log.Printf("tunnel: tcp: open %s: %v", dst.String(), err)
		return
	}
	defer remote.Close()
	pipeStream(remote, c)
}

// pipeStream copies bytes both ways for TCP conns until one side closes.
func pipeStream(remote io.ReadWriter, local io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(remote, local)
		if c, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, remote)
		if c, ok := local.(interface{ CloseRead() error }); ok {
			_ = c.CloseRead()
		}
	}()
	wg.Wait()
	_ = local.Close()
}

// HandleUDP pipes a netstack UDP conn through a UDP association to the agent.
// DNS hijack (Options.DNSHijack + dst port 53) rewrites the target to
// Options.DNSRemote so DNS queries reach the forward server's resolver.
func (t *Tunnel) HandleUDP(c netstack.UDPConn) {
	defer c.Close()
	id := c.ID()

	dst := tcpipAddrToMux(id.LocalAddress, id.LocalPort)
	if dst.Type == 0 {
		t.log.Printf("tunnel: udp: undialable destination %s", id.LocalAddress)
		return
	}
	target := t.dnsTarget(dst)
	remote, err := t.opts.Dialer.OpenUDP(target)
	if err != nil {
		t.log.Printf("tunnel: udp: open %s: %v", target.String(), err)
		return
	}
	defer remote.Close()
	pipePacket(c, remote)
}

// pipePacket relays datagrams between a netstack UDPConn and a mux UDPConn
// until either side closes.
func pipePacket(local netstack.UDPConn, remote *mux.UDPConn) {
	var wg sync.WaitGroup
	wg.Add(2)
	// local -> remote
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := local.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := remote.WriteTo(buf[:n], nil); err != nil {
				return
			}
		}
	}()
	// remote -> local
	go func() {
		defer wg.Done()
		id := local.ID()
		dstAddr := netstack.Addr{IP: id.RemoteAddress, Port: id.RemotePort}
		buf := make([]byte, 64*1024)
		for {
			n, _, err := remote.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := local.WriteTo(buf[:n], dstAddr); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}

// splitHostPortDefault splits host:port, defaulting the port if absent or
// non-numeric.
func splitHostPortDefault(s string, def int) (string, int) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, def
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return h, def
	}
	return h, port
}

// Close stops the tunnel and its stack.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.stack != nil {
		t.stack.Close()
		t.stack.Wait()
	}
	return nil
}

// tcpipAddrToMux converts a gVisor tcpip.Address + port to a mux.Addr.
func tcpipAddrToMux(a tcpip.Address, port uint16) mux.Addr {
	s := a.AsSlice()
	if ip4, ok := netip.AddrFromSlice(s); ok && ip4.Is4() {
		var v4 [4]byte
		copy(v4[:], s)
		return mux.Addr{Type: mux.AddrIPv4, IPv4: v4, Port: port}
	}
	if ip6, ok := netip.AddrFromSlice(s); ok && ip6.Is6() {
		var v6 [16]byte
		copy(v6[:], s)
		return mux.Addr{Type: mux.AddrIPv6, IPv6: v6, Port: port}
	}
	return mux.Addr{}
}
