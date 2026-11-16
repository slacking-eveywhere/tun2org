package agent_test

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"ntun-ng/internal/agent"
	"ntun-ng/internal/mux"
)

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
		if c < '0' || c > '9' {
			t.Fatalf("non-numeric port %q", p)
		}
		port = port*10 + uint16(c-'0')
	}
	return h, port
}

// TestAgentRunEcho drives the public agent.Run over a net.Pipe with a local
// mux Session on the other end. It verifies that the agent dials the echo
// server and relays data end-to-end.
func TestAgentRunEcho(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	localEnd, agentEnd := net.Pipe()

	cfg := agent.Config{
		Dialer:    &net.Dialer{Timeout: 5 * time.Second},
		PingEvery: 0,
	}
	go func() {
		_ = agent.Run(agentEnd, cfg)
	}()

	localSess := mux.NewSession(localEnd, mux.SessionConfig{PingEvery: 0})
	go func() { _ = localSess.Serve() }()
	defer func() {
		_ = localSess.Close()
	}()

	host, port := hostPort(t, echoAddr)
	c, err := localSess.Open(mux.FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	payload := []byte("through the agent")
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("echo = %q, want %q", buf, payload)
	}
}

// TestAgentRunDialFailure verifies that an agent-side dial failure surfaces
// as an error to the local Open caller.
func TestAgentRunDialFailure(t *testing.T) {
	localEnd, agentEnd := net.Pipe()

	// Dialer that always fails.
	cfg := agent.Config{
		Dialer:    failingDialer{},
		PingEvery: 0,
	}
	go func() {
		_ = agent.Run(agentEnd, cfg)
	}()

	localSess := mux.NewSession(localEnd, mux.SessionConfig{PingEvery: 0})
	go func() { _ = localSess.Serve() }()
	defer func() { _ = localSess.Close() }()

	_, err := localSess.Open(mux.FromHostPort("127.0.0.1", 1))
	if err == nil {
		t.Fatal("Open: expected error, got nil")
	}
}

type failingDialer struct{}

func (failingDialer) Dial(network, addr string) (net.Conn, error) {
	return nil, io.EOF
}
