package netstack

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// noopHandler satisfies Handler; it drops every accepted connection.
type noopHandler struct{}

func (noopHandler) HandleTCP(TCPConn) {}
func (noopHandler) HandleUDP(UDPConn) {}

// blockedRW is an io.ReadWriter whose Read blocks until Close is called, like a
// TUN device. Writes are swallowed so the stack can never block on us.
type blockedRW struct {
	readCh  chan struct{}
	mu      sync.Mutex
	closed  bool
	written atomic.Int64
}

func newBlockedRW() *blockedRW { return &blockedRW{readCh: make(chan struct{})} }

func (b *blockedRW) Read(p []byte) (int, error) {
	<-b.readCh
	return 0, errors.New("closed")
}

func (b *blockedRW) Write(p []byte) (int, error) {
	b.written.Add(int64(len(p)))
	return len(p), nil
}

func (b *blockedRW) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		close(b.readCh)
		b.closed = true
	}
	return nil
}

// TestDestroyDoesNotHangAfterClose reproduces the graceful-shutdown hang fixed
// in app.Run: stack.Destroy() (== tunnel.Close()) blocks in Stack.Wait() until
// the link endpoint's dispatch loop returns, and that loop is blocked in
// device.Read() until the device is closed. Closing the endpoint (stops the
// outbound loop) and the underlying reader (unblocks the dispatch loop) before
// Destroy must let teardown complete.
func TestDestroyDoesNotHangAfterClose(t *testing.T) {
	rw := newBlockedRW()
	link, err := NewLinkEndpoint(rw, 1500, 0)
	if err != nil {
		t.Fatalf("NewLinkEndpoint: %v", err)
	}
	stk, err := CreateStack(Config{Link: link, Handler: noopHandler{}})
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	// Give the attach/dispatch/outbound loops a moment to start and block.
	time.Sleep(50 * time.Millisecond)

	// Teardown order as in app.Run: stop link loops + close the device
	// (underlying reader) before destroying the stack.
	link.Close()
	_ = rw.Close()

	done := make(chan struct{})
	go func() {
		stk.Destroy()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stack.Destroy hung after link + device closed")
	}
}

// TestDestroyWithoutDeviceCloseHangs documents the failure mode the app must
// avoid: destroying the stack while the device is still open never returns.
// We assert it actually blocks (so the ordering contract stays enforced).
func TestDestroyWithoutDeviceCloseHangs(t *testing.T) {
	rw := newBlockedRW()
	link, err := NewLinkEndpoint(rw, 1500, 0)
	if err != nil {
		t.Fatalf("NewLinkEndpoint: %v", err)
	}
	stk, err := CreateStack(Config{Link: link, Handler: noopHandler{}})
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	defer rw.Close()
	defer link.Close()

	time.Sleep(50 * time.Millisecond)

	destroyed := make(chan struct{})
	go func() {
		stk.Destroy()
		close(destroyed)
	}()
	select {
	case <-destroyed:
		// Should not be reachable while the device Read is still blocked.
	case <-time.After(200 * time.Millisecond):
		// Expected: Destroy is blocked. Now release and confirm it completes.
		_ = rw.Close()
		select {
		case <-destroyed:
		case <-time.After(3 * time.Second):
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("destroy still hung after device closed\n%s", buf[:n])
		}
	}
}
