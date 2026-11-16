package mux

// Regression tests for two field-reported bugs:
//
//  1. Truncated transfers: Conn.Read could return EOF while DATA frames were
//     still buffered (peer sends DATA immediately followed by CLOSE/FIN);
//     the select randomly picked the done case and dropped the bytes.
//  2. Session write-path deadlock: synchronous frame writes from the read
//     loop could park both peers' read loops on each other (SSH window
//     exhaustion), and the pingLoop's own write parked before arming the
//     pong-timeout watchdog, so the session zombied forever instead of
//     failing and reconnecting.

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// --- Bug 1: DATA-then-CLOSE must not truncate ------------------------------

// TestReadDrainsDataAfterClose drives DATA frames followed immediately by a
// CLOSE (server FIN), with the local side reading only after both have been
// processed. Every buffered byte must still be readable. Before the
// drain-first fix, Read randomly picked the done case and truncated ~50% of
// the time.
func TestReadDrainsDataAfterClose(t *testing.T) {
	payload := []byte("0123456789abcdef")

	sess, stop := runPair(t, func(Addr) (net.Conn, error) {
		serverEnd, agentEnd := net.Pipe()
		go func() {
			_, _ = serverEnd.Write(payload) // server sends the response...
			_ = serverEnd.Close()           // ...then FINs right away
		}()
		return agentEnd, nil
	})
	defer stop()

	c, err := sess.Open(Addr{Type: AddrIPv4, IPv4: [4]byte{127, 0, 0, 1}, Port: 80})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Let the agent deliver the DATA frames and the CLOSE (pipeConn closes
	// its mux conn once the first direction hits EOF) before reading, so the
	// conn has buffered data AND a closed done channel at once.
	time.Sleep(300 * time.Millisecond)

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFull: %v (buffered data lost on close; truncation regression)", err)
	}
	// After draining, Read must report the close.
	if _, err := c.Read(buf[:1]); err == nil {
		t.Fatal("expected EOF after draining buffered data")
	}
}

// --- Bug 2: wedged transport must fail the session -------------------------

// stalledConn is a transport whose reads and writes block until Close. It
// simulates an SSH session that stopped draining (peer read loop parked /
// window exhausted).
type stalledConn struct {
	once   sync.Once
	closed chan struct{}
}

func newStalledConn() *stalledConn { return &stalledConn{closed: make(chan struct{})} }

func (c *stalledConn) Read(p []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}
func (c *stalledConn) Write(p []byte) (int, error) {
	<-c.closed
	return 0, errors.New("stalled: closed")
}
func (c *stalledConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// TestPingPongTimeoutKillsWedgedTransport verifies the watchdog still fires
// when the writer goroutine is parked in a transport write: the PING got
// queued, PONG never comes back, and PongTimeout fails the session. Before
// the writer-goroutine rework, pingLoop's own write parked on the session
// write lock before arming this timeout, so a wedged transport zombied the
// session forever.
func TestPingPongTimeoutKillsWedgedTransport(t *testing.T) {
	rwc := newStalledConn()
	sess := NewSession(rwc, SessionConfig{
		PingEvery:   50 * time.Millisecond,
		PongTimeout: 500 * time.Millisecond,
	})
	defer sess.Close()
	go sess.Serve() // starts the ping loop

	select {
	case <-sess.Done():
		// Session failed as required.
	case <-time.After(5 * time.Second):
		t.Fatal("session stayed alive with a wedged transport (deadlock regression)")
	}
}

// TestPingEnqueueStallKillsWedgedTransport verifies the other watchdog: the
// control queue stays full (writer wedged in a transport write), so pingLoop
// cannot enqueue its PING. The bounded enqueue must fail the session
// instead of parking forever before arming the pong timeout.
func TestPingEnqueueStallKillsWedgedTransport(t *testing.T) {
	old := pingWriteTimeout
	pingWriteTimeout = 250 * time.Millisecond
	defer func() { pingWriteTimeout = old }()

	rwc := newStalledConn()
	sess := NewSession(rwc, SessionConfig{
		PingEvery:   50 * time.Millisecond,
		PongTimeout: time.Hour, // far away: only the enqueue stall may fire
	})
	defer sess.Close()

	// Wedge the writer and fill the control queue in two phases: the writer
	// consumes one frame into a stalled transport write, then stops consuming.
	// Phase 1 fills, a pause lets the writer drain one frame and park, phase 2
	// tops the queue back up so it is provably full *with the writer wedged*.
	fillCtl := func() {
		for {
			select {
			case sess.ctlCh <- outFrame{typ: TypePong}:
			default:
				return
			}
		}
	}
	fillCtl()
	time.Sleep(100 * time.Millisecond) // let the writer park in the stalled write
	fillCtl()

	go sess.Serve() // starts the ping loop

	select {
	case <-sess.Done():
		if !errors.Is(sess.Err(), ErrWriteStall) {
			t.Fatalf("session err = %v, want ErrWriteStall", sess.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session stayed alive with a full control queue (deadlock regression)")
	}
}

// TestDataWriteUnblocksOnSessionDeath verifies data-path writers blocked on a
// full outbound queue (backpressure) wake up with an error once the session
// dies, instead of parking forever.
func TestDataWriteUnblocksOnSessionDeath(t *testing.T) {
	rwc := newStalledConn()
	sess := NewSession(rwc, SessionConfig{})

	// Fill the data queue with max-size frames (the writer consumes one then
	// stalls in the transport write) and launch more senders than queue slots:
	// some may sneak into the space the writer freed, the rest must block.
	frame := outFrame{typ: TypeData, payload: make([]byte, MaxDataLen)}
fill:
	for {
		select {
		case sess.outCh <- frame:
		default:
			break fill
		}
	}

	const senders = outQueueCap + 4
	results := make(chan error, senders)
	for i := 0; i < senders; i++ {
		go func() {
			results <- sess.enqueueData(TypeData, make([]byte, MaxDataLen))
		}()
	}

	// Let the blocked senders settle, then kill the session.
	time.Sleep(100 * time.Millisecond)
	sess.Close()

	blocked := 0
	for i := 0; i < senders; i++ {
		select {
		case err := <-results:
			if err == nil {
				continue // fit into the slot the writer freed
			}
			if !errors.Is(err, ErrSessionClosed) {
				t.Fatalf("enqueueData = %v, want ErrSessionClosed", err)
			}
			blocked++
		case <-time.After(5 * time.Second):
			t.Fatal("blocked data writer never woke on session death")
		}
	}
	if blocked == 0 {
		t.Fatal("no sender blocked on the full queue; test setup is broken")
	}
}
