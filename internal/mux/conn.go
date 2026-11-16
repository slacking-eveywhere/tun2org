package mux

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSessionClosed is returned by Open/Read/Write when the session is closed.
var ErrSessionClosed = errors.New("mux: session closed")

// ErrWriteStall is the terminal error used when the outbound transport is
// wedged: a control frame could not be queued in time. The session is failed
// so the relay can reconnect instead of deadlocking.
var ErrWriteStall = errors.New("mux: transport write stalled")

// recvCap is the maximum number of inbound DATA frames buffered per Conn
// before the peer is told to stop (ERR overflow + conn closed). It is the
// single backpressure mechanism: the session read loop never blocks on a
// slow consumer beyond this bound.
const recvCap = 128

// Conn is a single multiplexed stream over a Session. It implements net.Conn.
//
// A Conn exists on both sides of the session, keyed by the same ConnID. The
// side that calls Session.Open creates the Conn and picks the ConnID; the
// peer creates its Conn on receipt of the OPEN frame.
type Conn struct {
	id   ConnID
	sess *Session

	// inbound data, fed by the session read loop from DATA frames.
	recvCh chan []byte

	readMu  sync.Mutex
	readBuf []byte // leftover from a previous recvCh slice

	// ready is closed once OPEN_OK is received (local side) or sent (agent
	// side). Open blocks on it.
	ready chan struct{}
	errCh chan error // buffered 1, for dial failures before ready

	closed  atomic.Bool
	done    chan struct{}
	readErr error

	writeMu sync.Mutex
}

func newConn(id ConnID, sess *Session) *Conn {
	return &Conn{
		id:     id,
		sess:   sess,
		recvCh: make(chan []byte, recvCap),
		ready:  make(chan struct{}),
		errCh:  make(chan error, 1),
		done:   make(chan struct{}),
	}
}

// ID returns the ConnID.
func (c *Conn) ID() ConnID { return c.id }

// markReady signals that the remote accepted the OPEN (OPEN_OK).
func (c *Conn) markReady() {
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
}

// markFailed delivers a dial failure to a not-yet-ready Open caller.
func (c *Conn) markFailed(err error) {
	select {
	case c.errCh <- err:
	default:
	}
}

// markClosed closes the conn. err becomes the terminal Read error. Safe to
// call multiple times. Does not send a CLOSE frame (callers do that).
func (c *Conn) markClosed(err error) {
	if c.closed.Swap(true) {
		return
	}
	c.readMu.Lock()
	c.readErr = err
	c.readMu.Unlock()
	close(c.done)
}

// deliver enqueues inbound DATA. Returns false if the conn is closed or the
// recv buffer is full (overflow); in the overflow case the caller should send
// ERR overflow and close the conn.
func (c *Conn) deliver(data []byte) bool {
	if c.closed.Load() {
		return false
	}
	select {
	case c.recvCh <- data:
		return true
	default:
		return false
	}
}

// Read implements io.Reader. It blocks until data is available or the conn is
// closed. The read mutex guards only readBuf, never held across a blocking
// select, so markClosed can always make progress.
//
// Drain-first: buffered DATA frames are always returned before the terminal
// error, even when the peer sends DATA immediately followed by CLOSE (FIN).
// Without this, the select below could randomly pick the done case while
// frames were still buffered, silently truncating the stream.
func (c *Conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	if len(c.readBuf) > 0 {
		n := copy(p, c.readBuf)
		c.readBuf = c.readBuf[n:]
		c.readMu.Unlock()
		return n, nil
	}
	c.readMu.Unlock()

	// Fast path: a frame is already buffered.
	select {
	case b := <-c.recvCh:
		return c.take(p, b), nil
	default:
	}

	select {
	case b := <-c.recvCh:
		return c.take(p, b), nil
	case <-c.done:
		// Drain frames delivered before the close, then report the error.
		select {
		case b := <-c.recvCh:
			return c.take(p, b), nil
		default:
			return 0, c.readErr
		}
	}
}

// take copies frame b into p, stashing the leftover in readBuf, and returns
// how many bytes were consumed.
func (c *Conn) take(p []byte, b []byte) int {
	c.readMu.Lock()
	c.readBuf = b
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	c.readMu.Unlock()
	return n
}

// Write implements io.Writer. Data is split into MaxDataLen frames.
func (c *Conn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrSessionClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	total := 0
	for len(p) > 0 {
		if c.closed.Load() {
			return total, ErrSessionClosed
		}
		chunk := p
		if len(chunk) > MaxDataLen {
			chunk = chunk[:MaxDataLen]
		}
		payload := make([]byte, 0, ConnIDLen+len(chunk))
		payload = append(payload, c.id[:]...)
		payload = append(payload, chunk...)
		if err := c.sess.enqueueData(TypeData, payload); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

// Close closes the conn locally and notifies the peer with a CLOSE frame.
func (c *Conn) Close() error {
	c.markClosed(io.EOF)
	c.sess.unregister(c.id)
	// Best-effort CLOSE notification, on the data queue so it cannot overtake
	// this conn's queued DATA frames (order matters: FIN must follow data).
	_ = c.sess.enqueueData(TypeClose, c.id[:])
	return nil
}

// SetDeadline is not supported; it is implemented only to satisfy net.Conn.
func (c *Conn) SetDeadline(time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(time.Time) error { return nil }

// LocalAddr/RemoteAddr are not meaningful for a muxed stream.
func (c *Conn) LocalAddr() net.Addr  { return Addr{} }
func (c *Conn) RemoteAddr() net.Addr { return Addr{} }
