package mux

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// startUDPEcho starts a UDP echo server on 127.0.0.1.
func startUDPEcho(t *testing.T) (addr string, shutdown func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenpacket: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, raddr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], raddr)
		}
	}()
	return pc.LocalAddr().String(), func() { pc.Close(); wg.Wait() }
}

func udpHostPort(t *testing.T, s string) (string, uint16) {
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

// TestUDPRoundTrip opens a UDP association through an in-process agent to a UDP
// echo server and verifies a datagram is echoed back.
func TestUDPRoundTrip(t *testing.T) {
	echoAddr, echoStop := startUDPEcho(t)
	defer echoStop()

	localEnd, agentEnd := pipePair()
	agentSess := NewSession(agentEnd, SessionConfig{
		AcceptFunc:    func(a Addr) (net.Conn, error) { return net.DialTimeout("tcp", a.String(), 5*time.Second) },
		AcceptUDPFunc: func(a Addr) (net.Conn, error) { return net.DialTimeout("udp", a.String(), 5*time.Second) },
		PingEvery:     0,
	})
	localSess := NewSession(localEnd, SessionConfig{PingEvery: 0})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = agentSess.Serve() }()
	go func() { defer wg.Done(); _ = localSess.Serve() }()
	defer func() {
		_ = localSess.Close()
		_ = agentSess.Close()
		wg.Wait()
	}()

	host, port := udpHostPort(t, echoAddr)
	uc, err := localSess.OpenUDP(FromHostPort(host, port))
	if err != nil {
		t.Fatalf("OpenUDP: %v", err)
	}
	defer uc.Close()

	payload := []byte("dns query goes here")
	if _, err := uc.WriteTo(payload, nil); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	buf := make([]byte, len(payload))
	uc.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := uc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("echo = %q, want %q", buf[:n], payload)
	}
}

func TestAddrIPv6String(t *testing.T) {
	a := FromHostPort("::1", 53)
	if a.Type != AddrIPv6 {
		t.Fatalf("type = %d, want ipv6", a.Type)
	}
	if a.String() != "[::1]:53" {
		t.Fatalf("String = %q", a.String())
	}
}

func TestFromHostPortIPv6RoundTrip(t *testing.T) {
	a := FromHostPort("fd00::1", 5353)
	enc := encodeAddr(nil, a)
	got, err := decodeAddr(enc)
	if err != nil {
		t.Fatalf("decodeAddr: %v", err)
	}
	if got.Type != AddrIPv6 {
		t.Fatalf("type = %d", got.Type)
	}
	if got.Port != 5353 {
		t.Fatalf("port = %d", got.Port)
	}
}
