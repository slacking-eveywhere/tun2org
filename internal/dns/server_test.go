package dns_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"ntun-ng/internal/dns"
	"ntun-ng/internal/mux"
)

// fakeForwarder implements dns.Forwarder by opening an in-process UDP agent
// over net.Pipe that dials the target directly.
type fakeForwarder struct{}

func (fakeForwarder) OpenUDP(addr mux.Addr) (*mux.UDPConn, error) {
	localEnd, agentEnd := net.Pipe()
	agentSess := mux.NewSession(agentEnd, mux.SessionConfig{
		AcceptUDPFunc: func(a mux.Addr) (net.Conn, error) {
			return net.DialTimeout("udp", a.String(), 3*time.Second)
		},
		PingEvery: 0,
	})
	go func() { _ = agentSess.Serve() }()
	localSess := mux.NewSession(localEnd, mux.SessionConfig{PingEvery: 0})
	go func() { _ = localSess.Serve() }()
	return localSess.OpenUDP(addr)
}

// startUDPDNSEcho answers any datagram by echoing it back (stands in for a
// remote resolver).
func startUDPDNSEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenpacket: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, raddr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], raddr)
		}
	}()
	return pc.LocalAddr().String()
}

// waitForAddr polls s.LocalAddr until it is non-empty or times out.
func waitForAddr(s *dns.ListenServer, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a := s.LocalAddr(); a != "" {
			return a, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", errTimeout
}

var errTimeout = &timeoutErr{}

type timeoutErr struct{}

func (*timeoutErr) Error() string { return "timed out" }

// TestListenServerForwardQuery: local DNS server forwards a query to a UDP
// echo (standing in for a resolver) through the in-process mux agent and
// returns the reply to the client.
func TestListenServerForwardQuery(t *testing.T) {
	remote := startUDPDNSEcho(t)
	srv := dns.NewListenServer("127.0.0.1:0", remote, fakeForwarder{}, 0, testLogger{t})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()

	addr, err := waitForAddr(srv, time.Second)
	if err != nil {
		t.Fatalf("server did not bind: %v", err)
	}

	client, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer client.Close()

	query := []byte("fake dns query")
	if _, err := client.Write(query); err != nil {
		t.Fatalf("write query: %v", err)
	}
	client.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 1500)
	n, err := client.Read(reply)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if string(reply[:n]) != string(query) {
		t.Fatalf("reply = %q, want %q", reply[:n], query)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Close() }()
	wg.Wait()
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) {
	l.t.Logf("dns: "+format, args...)
}
