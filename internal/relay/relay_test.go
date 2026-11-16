package relay_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ntun-ng/internal/agent"
	"ntun-ng/internal/mux"
	"ntun-ng/internal/relay"
)

// startEcho starts a TCP echo server on 127.0.0.1.
func startEcho(t *testing.T) (addr string, shutdown func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		wg.Wait()
	}
}

func hostPort(t *testing.T, s string) (string, uint16) {
	t.Helper()
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		t.Fatalf("SplitHostPort %q: %v", s, err)
	}
	var port uint16
	for _, c := range []byte(p) {
		port = port*10 + uint16(c-'0')
	}
	return h, port
}

// fakeDialer returns in-memory transports: each DialSession creates a net.Pipe
// and runs a real agent on the far end, so the relay gets a live mux Session
// without any SSH. Sessions can be killed individually via killSession.
type fakeDialer struct {
	failN    int32 // number of consecutive dial failures before success
	calls    atomic.Int32
	sessions atomic.Int32

	mu    sync.Mutex
	kills []chan struct{} // one kill channel per session, in creation order
}

func (f *fakeDialer) DialSession(ctx context.Context) (io.ReadWriteCloser, error) {
	c := f.calls.Add(1)
	if int(c) <= int(f.failN) {
		return nil, errors.New("dial failed (simulated)")
	}
	kill := make(chan struct{})
	f.mu.Lock()
	f.kills = append(f.kills, kill)
	f.mu.Unlock()

	localEnd, agentEnd := net.Pipe()
	go func() {
		_ = agent.Run(agentEnd, agent.Config{
			Dialer: &net.Dialer{Timeout: 5 * time.Second},
		})
	}()
	f.sessions.Add(1)
	go func() {
		<-kill
		_ = localEnd.Close()
	}()
	return localEnd, nil
}

// killSession closes the i-th session's transport (killing it like a dropped
// SSH connection).
func (f *fakeDialer) killSession(i int) {
	f.mu.Lock()
	kill := f.kills[i]
	f.mu.Unlock()
	close(kill)
}

// TestRelayOpenThroughAgent: dial succeeds immediately; Open reaches the echo
// server through the agent.
func TestRelayOpenThroughAgent(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	f := &fakeDialer{}
	r := relay.New(relay.Options{
		Dialer:       f,
		PingEvery:    0,
		ReconnectMin: 10 * time.Millisecond,
		ReconnectMax: 50 * time.Millisecond,
		Log:          testLogger(t),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	host, port := hostPort(t, echoAddr)
	c, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	payload := []byte("via relay")
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo = %q, want %q", buf, payload)
	}
	cancel()
	r.Stop()
}

// TestRelayReconnectAfterFailures: the dialer fails N times then succeeds; the
// relay backs off and eventually establishes a working session.
func TestRelayReconnectAfterFailures(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	f := &fakeDialer{failN: 3}
	r := relay.New(relay.Options{
		Dialer:       f,
		PingEvery:    0,
		ReconnectMin: 5 * time.Millisecond,
		ReconnectMax: 20 * time.Millisecond,
		Log:          testLogger(t),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	host, port := hostPort(t, echoAddr)
	c, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()
	if got := f.calls.Load(); got < 4 {
		t.Fatalf("expected >=4 dial attempts (3 fail + 1 ok), got %d", got)
	}
	cancel()
	r.Stop()
}

// TestRelayReconnectAfterSessionDeath: session established, then transport
// dies; relay reconnects and Open works again.
func TestRelayReconnectAfterSessionDeath(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	f := &fakeDialer{}
	r := relay.New(relay.Options{
		Dialer:       f,
		PingEvery:    0,
		ReconnectMin: 10 * time.Millisecond,
		ReconnectMax: 30 * time.Millisecond,
		Log:          testLogger(t),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	host, port := hostPort(t, echoAddr)
	c, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	// Kill the first session's transport.
	f.killSession(0)
	_ = c.Close()

	// The relay should reconnect and Open should work again.
	c2, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open (2nd, after reconnect): %v", err)
	}
	defer c2.Close()
	if f.sessions.Load() < 2 {
		t.Fatalf("expected >=2 sessions, got %d", f.sessions.Load())
	}
	cancel()
	r.Stop()
}

// relayOpenRetry polls r.Open until it succeeds or times out (the relay may
// not have a live session yet during reconnect).
func relayOpenRetry(r *relay.Relay, addr mux.Addr) (*mux.Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := r.Open(addr)
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return nil, lastErr
}

// TestRelayRebindDropsSession: calling Rebind drops the live session and the
// relay establishes a new one; Open works again on the new session.
func TestRelayRebindDropsSession(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	f := &fakeDialer{}
	r := relay.New(relay.Options{
		Dialer:       f,
		PingEvery:    0,
		ReconnectMin: 5 * time.Millisecond,
		ReconnectMax: 20 * time.Millisecond,
		Log:          testLogger(t),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	host, port := hostPort(t, echoAddr)
	c, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	_ = c.Close()
	sessionsBefore := f.sessions.Load()

	// Trigger a rebind; the relay should drop the session and reconnect.
	r.Rebind()

	// Wait until at least one more session has been established.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.sessions.Load() > sessionsBefore {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f.sessions.Load() <= sessionsBefore {
		t.Fatalf("rebind did not create a new session (sessions=%d, want >%d)",
			f.sessions.Load(), sessionsBefore)
	}

	// Open must work on the new session.
	c2, err := relayOpenRetry(r, mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open (after rebind): %v", err)
	}
	_ = c2.Close()
	cancel()
	r.Stop()
}

// testLogger logs through t.Logf.
func testLogger(t *testing.T) func(format string, args ...any) {
	return func(format string, args ...any) {
		t.Logf("relay: "+format, args...)
	}
}
