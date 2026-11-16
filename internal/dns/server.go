// Package dns provides optional local DNS services:
//   - ListenServer: a UDP DNS server bound to dns_listen (e.g. 127.0.0.1:5353)
//     that forwards queries to the remote resolver through the mux (via the
//     relay's OpenUDP). Useful for apps that let you point at a resolver.
//
// The transparent TUN-side hijack (rewriting UDP/53 dst to dns_remote) lives
// in package tunnel; this package is the opt-in local listener.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"ntun-ng/internal/mux"
)

// Forwarder opens a UDP association to a remote resolver. *relay.Relay satisfies
// this.
type Forwarder interface {
	OpenUDP(addr mux.Addr) (*mux.UDPConn, error)
}

// Logger is the minimal logging surface.
type Logger interface {
	Printf(format string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// ListenServer answers DNS queries on a local UDP address by forwarding them
// over the mux to a remote resolver.
type ListenServer struct {
	listen  string
	remote  string // host:port of the remote resolver
	fwd     Forwarder
	log     Logger
	timeout time.Duration

	mu       sync.Mutex
	pc       net.PacketConn
	closed   bool
	doneChan chan struct{}
}

// NewListenServer creates a server bound to listen that forwards to remote
// (host:port) via fwd. timeout is the per-query upstream deadline (0 = 5s).
func NewListenServer(listen, remote string, fwd Forwarder, timeout time.Duration, log Logger) *ListenServer {
	if log == nil {
		log = discardLogger{}
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &ListenServer{
		listen:   listen,
		remote:   remote,
		fwd:      fwd,
		log:      log,
		timeout:  timeout,
		doneChan: make(chan struct{}),
	}
}

// LocalAddr returns the actual bound address after ListenAndServe has bound
// the socket, or empty if not yet listening.
func (s *ListenServer) LocalAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pc == nil {
		return ""
	}
	return s.pc.LocalAddr().String()
}

// ListenAndServe starts the server. Blocks until ctx is cancelled or a fatal
// listen error occurs.
func (s *ListenServer) ListenAndServe(ctx context.Context) error {
	pc, err := net.ListenPacket("udp", s.listen)
	if err != nil {
		return fmt.Errorf("dns: listen %s: %w", s.listen, err)
	}
	s.mu.Lock()
	s.pc = pc
	s.mu.Unlock()
	defer func() {
		pc.Close()
		close(s.doneChan)
	}()

	remoteHost, remotePort := splitHostPort(s.remote, 53)
	target := mux.FromHostPort(remoteHost, uint16(remotePort))

	s.log.Printf("dns: serving %s -> %s", s.listen, s.remote)

	var wg sync.WaitGroup
	for {
		buf := make([]byte, 1500)
		n, raddr, err := pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				wg.Wait()
				return nil
			default:
				if s.isClosed() {
					wg.Wait()
					return nil
				}
				continue
			}
		}
		query := buf[:n]
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.forward(ctx, pc, raddr, query, target)
		}()
	}
}

// forward sends query to the remote resolver via the mux and writes the reply
// back to the client.
func (s *ListenServer) forward(ctx context.Context, pc net.PacketConn, client net.Addr, query []byte, target mux.Addr) {
	uc, err := s.fwd.OpenUDP(target)
	if err != nil {
		s.log.Printf("dns: open upstream: %v", err)
		return
	}
	defer uc.Close()
	if dl, ok := ctx.Deadline(); ok {
		uc.SetReadDeadline(dl)
	} else {
		uc.SetReadDeadline(time.Now().Add(s.timeout))
	}
	if _, err := uc.WriteTo(query, nil); err != nil {
		s.log.Printf("dns: write upstream: %v", err)
		return
	}
	reply := make([]byte, 1500)
	uc.SetReadDeadline(time.Now().Add(s.timeout))
	n, _, err := uc.ReadFrom(reply)
	if err != nil {
		s.log.Printf("dns: read upstream: %v", err)
		return
	}
	if _, err := pc.WriteTo(reply[:n], client); err != nil {
		s.log.Printf("dns: write client: %v", err)
	}
}

// Close stops the server. Subsequent ListenAndServe calls return.
func (s *ListenServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pc := s.pc
	s.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
	<-s.doneChan
	return nil
}

func (s *ListenServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// splitHostPort splits host:port with a default.
func splitHostPort(s string, def int) (string, int) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, def
	}
	port := 0
	for _, c := range []byte(p) {
		if c < '0' || c > '9' {
			return h, def
		}
		port = port*10 + int(c-'0')
	}
	return h, port
}

var _ = errors.New
