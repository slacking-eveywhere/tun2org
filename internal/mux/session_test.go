package mux

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// startEcho starts a TCP echo server on 127.0.0.1 and returns its address
// plus a shutdown func.
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

// pipePair returns two connected io.ReadWriteClosers (like an SSH session
// stdin/stdout pair) backed by net.Pipe.
func pipePair() (io.ReadWriteCloser, io.ReadWriteCloser) {
	a, b := net.Pipe()
	return a, b
}

// runPair starts a local Session (no AcceptFunc) and an agent Session
// (AcceptFunc = dialer) over a pipe pair. Returns the local session and a
// shutdown func.
func runPair(t *testing.T, agentDial AcceptFunc) (*Session, func()) {
	t.Helper()
	localEnd, agentEnd := pipePair()
	agentSess := NewSession(agentEnd, SessionConfig{
		AcceptFunc: agentDial,
		PingEvery:  0, // disable ping for deterministic tests
	})
	localSess := NewSession(localEnd, SessionConfig{
		PingEvery: 0,
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = agentSess.Serve()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = localSess.Serve()
	}()
	return localSess, func() {
		_ = localSess.Close()
		_ = agentSess.Close()
		wg.Wait()
	}
}

func dialerAccept(addr Addr) (net.Conn, error) {
	return net.DialTimeout("tcp", addr.String(), 5*time.Second)
}

func TestOpenEchoRoundTrip(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	local, stop := runPair(t, dialerAccept)
	defer stop()

	host, port := hostPort(t, echoAddr)
	c, err := local.Open(FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	payload := []byte("hello")
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

func TestOpenEchoLargeChunked(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	local, stop := runPair(t, dialerAccept)
	defer stop()

	host, port := hostPort(t, echoAddr)
	c, err := local.Open(FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// ~256KB, exercises DATA chunking (MaxDataLen=64KiB).
	payload := make([]byte, 256*1024+7)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	go func() {
		_, _ = c.Write(payload)
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestOpenDialFailureReturnsError(t *testing.T) {
	// AcceptFunc that always fails.
	failAccept := func(Addr) (net.Conn, error) {
		return nil, errors.New("no route to host")
	}
	local, stop := runPair(t, failAccept)
	defer stop()

	_, err := local.Open(FromHostPort("127.0.0.1", 1))
	if err == nil {
		t.Fatal("Open: expected dial error, got nil")
	}
}

func TestClosePropagates(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	local, stop := runPair(t, dialerAccept)
	defer stop()

	host, port := hostPort(t, echoAddr)
	c, err := local.Open(FromHostPort(host, port))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Close locally; the agent side should close the echo conn, and the
	// echo conn closing should surface as EOF on the remote pipe (handled
	// by the session teardown).
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Subsequent read returns an error.
	if _, err := c.Read(make([]byte, 4)); err == nil {
		t.Fatal("expected error reading after close")
	}
}

func TestPingPong(t *testing.T) {
	_, echoStop := startEcho(t)
	defer echoStop()

	// Build a pair with pings enabled on the local side.
	localEnd, agentEnd := pipePair()
	agentSess := NewSession(agentEnd, SessionConfig{
		AcceptFunc: dialerAccept,
		PingEvery:  0,
	})
	localSess := NewSession(localEnd, SessionConfig{
		PingEvery: 20 * time.Millisecond,
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = agentSess.Serve() }()
	go func() { defer wg.Done(); _ = localSess.Serve() }()
	defer func() {
		_ = localSess.Close()
		_ = agentSess.Close()
		wg.Wait()
	}()

	// Give it a few ping cycles; if pong fails the session dies.
	time.Sleep(100 * time.Millisecond)
	if localSess.Err() != nil {
		t.Fatalf("session died during ping/pong: %v", localSess.Err())
	}
}

func TestOpenMultipleConcurrent(t *testing.T) {
	echoAddr, echoStop := startEcho(t)
	defer echoStop()

	local, stop := runPair(t, dialerAccept)
	defer stop()

	host, port := hostPort(t, echoAddr)
	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := local.Open(FromHostPort(host, port))
			if err != nil {
				t.Errorf("Open %d: %v", i, err)
				return
			}
			defer c.Close()
			msg := []byte(fmt.Sprintf("conn-%d-payload", i))
			if _, err := c.Write(msg); err != nil {
				t.Errorf("Write %d: %v", i, err)
				return
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(c, buf); err != nil {
				t.Errorf("Read %d: %v", i, err)
				return
			}
			if !bytes.Equal(buf, msg) {
				t.Errorf("echo %d = %q, want %q", i, buf, msg)
			}
		}(i)
	}
	wg.Wait()
}

// hostPort splits "host:port" using net.SplitHostPort.
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
