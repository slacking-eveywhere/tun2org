package mux

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// AcceptFunc is the agent-side callback invoked when an OPEN frame arrives
// (TCP). It must dial the target and return the resulting net.Conn, which the
// Session will pipe to/from the muxed stream. On error the Session replies
// with an ERR frame and the local Open caller receives it.
//
// On the local (ntun) side AcceptFunc is nil: that side never receives OPEN.
type AcceptFunc func(Addr) (net.Conn, error)

// AcceptUDPFunc is the agent-side callback invoked when a UDP_OPEN frame
// arrives. It must dial a connected UDP socket and return it. On error the
// Session replies with ERR. May be nil (UDP_OPEN then returns ErrConnNotFound).
type AcceptUDPFunc func(Addr) (net.Conn, error)

// SessionConfig configures a Session.
type SessionConfig struct {
	// AcceptFunc handles inbound OPEN frames (agent side). May be nil.
	AcceptFunc AcceptFunc
	// AcceptUDPFunc handles inbound UDP_OPEN frames (agent side). May be nil.
	AcceptUDPFunc AcceptUDPFunc

	// PingEvery is the keepalive interval. 0 disables ping.
	PingEvery time.Duration

	// PongTimeout is how long to wait for a PONG before declaring the session
	// dead. Defaults to PingEvery*2 (or 30s if pings disabled and this is 0).
	PongTimeout time.Duration
}

// Session is a mux over a single io.ReadWriteCloser.
//
// Serve runs the read loop and must be called on both sides. It blocks until
// the underlying stream errors or Close is called.
//
// Writes are serialized through a single writer goroutine fed by two queues:
//   - outCh (data frames): data-path writers block on it, preserving TCP
//     backpressure; they wake on session death via done.
//   - ctlCh (control frames: PING/PONG/CLOSE/ERR): non-blocking enqueue from
//     the read loop; a full queue means the writer (and thus the transport)
//     is wedged, so the frame is dropped and the ping watchdog kills the
//     session instead of deadlocking it.
// The writer prefers ctlCh so keepalives overtake bulk data.
type Session struct {
	rwc io.ReadWriteCloser
	br  *bufio.Reader
	bw  *bufio.Writer

	outCh chan outFrame
	ctlCh chan outFrame

	cfg SessionConfig

	connsMu sync.Mutex
	conns   map[ConnID]*Conn
	closed atomic.Bool
	done   chan struct{}
	err    error

	pongCh chan struct{}

	udpMu  sync.Mutex
	udpConns map[ConnID]*UDPConn
}

// outQueueCap bounds data frames queued for the writer. With 64KiB frames
// this is at most ~16MiB in flight per session; beyond it, data-path writes
// block (backpressure) until the transport drains.
const outQueueCap = 256

// ctlQueueCap bounds queued control frames. It only fills when the transport
// is wedged; the pingLoop's bounded enqueue then fails the session.
const ctlQueueCap = 32

// pingWriteTimeout bounds how long pingLoop may wait to enqueue a PING. If
// the control queue stays full this long, the transport is wedged and the
// session must fail (and reconnect) instead of deadlocking.
var pingWriteTimeout = 5 * time.Second

// outFrame is one queued frame for the writer loop.
type outFrame struct {
	typ     FrameType
	payload []byte
}

// NewSession creates a Session over rwc.
func NewSession(rwc io.ReadWriteCloser, cfg SessionConfig) *Session {
	if cfg.PongTimeout == 0 {
		if cfg.PingEvery > 0 {
			cfg.PongTimeout = cfg.PingEvery * 2
		} else {
			cfg.PongTimeout = 30 * time.Second
		}
	}
	s := &Session{
		rwc:    rwc,
		br:     bufio.NewReaderSize(rwc, MaxFrameLen+headerLen),
		bw:     bufio.NewWriterSize(rwc, MaxFrameLen+headerLen),
		outCh:  make(chan outFrame, outQueueCap),
		ctlCh:  make(chan outFrame, ctlQueueCap),
		cfg:    cfg,
		conns:  make(map[ConnID]*Conn),
		udpConns: make(map[ConnID]*UDPConn),
		done:   make(chan struct{}),
		pongCh: make(chan struct{}, 1),
	}
	go s.writerLoop()
	return s
}

// Done returns a channel closed when the session terminates.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns the terminal error (nil if closed cleanly).
func (s *Session) Err() error { return s.err }

// writerLoop is the single writer goroutine. It prefers control frames over
// data frames so PING/PONG/CLOSE overtake bulk transfers. Any write error
// fails the session; on session death it closes rwc so that any other
// goroutine still parked in a transport write unblocks with an error.
func (s *Session) writerLoop() {
	for {
		var f outFrame
		// Control frames first, if any.
		select {
		case f = <-s.ctlCh:
		default:
			select {
			case f = <-s.ctlCh:
			case f = <-s.outCh:
			case <-s.done:
				return
			}
		}
		if err := writeFrame(s.bw, f.typ, f.payload); err != nil {
			s.fail(err)
			return
		}
		if err := s.bw.Flush(); err != nil {
			s.fail(err)
			return
		}
	}
}

// enqueueData queues a data-path frame, blocking while the queue is full
// (backpressure). It returns promptly once the session is dead. Data-path
// frames (DATA, OPEN, OPEN_OK, CLOSE) share one FIFO queue so a conn's CLOSE
// can never overtake its queued DATA.
func (s *Session) enqueueData(typ FrameType, payload []byte) error {
	for {
		if s.closed.Load() {
			return ErrSessionClosed
		}
		select {
		case <-s.done:
			return ErrSessionClosed
		default:
		}
		select {
		case s.outCh <- outFrame{typ, payload}:
			return nil
		case <-s.done:
			return ErrSessionClosed
		}
	}
}

// enqueueCtl queues a control frame without ever blocking the caller (the
// read loop uses this). When the queue is full the frame is dropped: that
// only happens when the transport is wedged, and the pingLoop watchdog then
// fails the session.
func (s *Session) enqueueCtl(typ FrameType, payload []byte) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}
	select {
	case s.ctlCh <- outFrame{typ, payload}:
		return nil
	case <-s.done:
		return ErrSessionClosed
	default:
		return ErrWriteStall
	}
}

// enqueueCtlTimeout queues a control frame, waiting at most timeout. Used by
// pingLoop: a control queue wedged this long means the session is dead in
// the water, so fail it (which closes rwc and unblocks everything).
func (s *Session) enqueueCtlTimeout(typ FrameType, payload []byte, timeout time.Duration) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case s.ctlCh <- outFrame{typ, payload}:
		return nil
	case <-s.done:
		return ErrSessionClosed
	case <-t.C:
		s.fail(ErrWriteStall)
		return ErrWriteStall
	}
}

// Open initiates a new muxed connection to addr (local/client side). It blocks
// until the peer acknowledges (OPEN_OK) or reports a dial failure (ERR).
func (s *Session) Open(addr Addr) (*Conn, error) {
	if s.closed.Load() {
		return nil, ErrSessionClosed
	}
	var id ConnID
	if _, err := readRand(id[:]); err != nil {
		return nil, err
	}
	c := newConn(id, s)
	s.register(c)

	payload := make([]byte, 0, ConnIDLen+1+maxAddrLen(addr)+2)
	payload = append(payload, id[:]...)
	payload = encodeAddr(payload, addr)
	if err := s.enqueueData(TypeOpen, payload); err != nil {
		s.unregister(id)
		return nil, err
	}

	select {
	case <-c.ready:
		return c, nil
	case err := <-c.errCh:
		s.unregister(id)
		return nil, err
	case <-s.done:
		s.unregister(id)
		return nil, ErrSessionClosed
	}
}

// Serve runs the read loop until the session ends. Returns the terminal error.
func (s *Session) Serve() error {
	if s.cfg.PingEvery > 0 {
		go s.pingLoop()
	}
	for {
		f, err := readFrame(s.br)
		if err != nil {
			s.fail(err)
			return err
		}
		if err := s.handle(f); err != nil {
			s.fail(err)
			return err
		}
	}
}

// Close shuts the session down and closes the underlying stream.
func (s *Session) Close() error {
	s.fail(ErrSessionClosed)
	return s.rwc.Close()
}

func (s *Session) fail(err error) {
	if !s.closed.Swap(true) {
		s.err = err
		close(s.done)
		// Closing rwc unblocks the writer loop (which may be parked in a
		// transport write) and any peer-side reader; without this a wedged
		// transport could keep goroutines parked forever.
		_ = s.rwc.Close()
		s.closeAllConns()
	}
}

func (s *Session) handle(f Frame) error {
	switch f.Type {
	case TypeOpen:
		return s.handleOpen(f.Payload)
	case TypeOpenOK:
		s.handleOpenOK(f.Payload)
	case TypeData:
		s.handleData(f.Payload)
	case TypeClose:
		s.handleClose(f.Payload)
	case TypeErr:
		s.handleErr(f.Payload)
	case TypePing:
		_ = s.enqueueCtl(TypePong, nil)
	case TypePong:
		select {
		case s.pongCh <- struct{}{}:
		default:
		}
	case TypeUDPOpen:
		return s.handleUDPOpen(f.Payload)
	case TypeUDPData:
		s.handleUDPData(f.Payload)
	default:
		return fmt.Errorf("mux: unknown frame type 0x%02x", f.Type)
	}
	return nil
}

func (s *Session) handleOpen(p []byte) error {
	if len(p) < ConnIDLen {
		return errors.New("mux: OPEN: missing connID")
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	addr, err := decodeAddr(p[ConnIDLen:])
	if err != nil {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrProtocol)))
		return err
	}
	if s.cfg.AcceptFunc == nil {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrConnNotFound)))
		return nil
	}
	go s.acceptConn(id, addr)
	return nil
}

func (s *Session) acceptConn(id ConnID, addr Addr) {
	conn, err := s.cfg.AcceptFunc(addr)
	if err != nil {
		_ = s.enqueueData(TypeErr, append(id[:], byte(ErrDialFailed)))
		return
	}
	c := newConn(id, s)
	s.register(c)
	okPayload := make([]byte, ConnIDLen)
	copy(okPayload, id[:])
	if err := s.enqueueData(TypeOpenOK, okPayload); err != nil {
		conn.Close()
		return
	}
	c.markReady()
	pipeConn(c, conn)
}

// pipeConn bridges a mux Conn with a dialed net.Conn until either end closes.
func pipeConn(c *Conn, conn net.Conn) {
	done := make(chan struct{}, 2)
	// mux -> net.Conn
	go func() {
		_, _ = io.Copy(conn, c)
		done <- struct{}{}
	}()
	// net.Conn -> mux
	go func() {
		_, _ = io.Copy(c, conn)
		done <- struct{}{}
	}()
	<-done
	_ = conn.Close()
	_ = c.Close()
	<-done
}

// pipeUDP bridges a mux UDPConn with a dialed net.Conn (a connected UDP
// socket) until either side closes. Datagrams from the dialed socket are
// forwarded as UDP_DATA frames; UDP_DATA frames from the peer are written to
// the socket.
func pipeUDP(c *UDPConn, conn net.Conn) {
	done := make(chan struct{}, 2)
	// mux -> net.Conn
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				break
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	// net.Conn -> mux
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			if _, err := c.WriteTo(buf[:n], nil); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
	_ = conn.Close()
	_ = c.Close()
	<-done
}

func (s *Session) handleOpenOK(p []byte) {
	if len(p) < ConnIDLen {
		return
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	// OPEN_OK acknowledges both TCP OPEN and UDP OPEN (keyed by ConnID).
	if c := s.lookup(id); c != nil {
		c.markReady()
		return
	}
	if c := s.lookupUDP(id); c != nil {
		c.markReady()
	}
}

func (s *Session) handleData(p []byte) {
	if len(p) < ConnIDLen {
		return
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	c := s.lookup(id)
	if c == nil {
		// Unknown conn: the local side has already closed it. Dropping the
		// frame (instead of erroring back) avoids control-frame churn when the
		// peer races a close against in-flight data.
		return
	}
	// readFrame allocates a fresh payload slice, so it is safe to retain here.
	if !c.deliver(p[ConnIDLen:]) {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrOverflow)))
		_ = c.Close()
	}
}

func (s *Session) handleUDPData(p []byte) {
	if len(p) < ConnIDLen {
		return
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	c := s.lookupUDP(id)
	if c == nil {
		// Unknown association: closed locally; drop silently.
		return
	}
	d := &UDPDatagram{Data: p[ConnIDLen:], From: c.Addr().asNetAddr()}
	if !c.deliver(d) {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrOverflow)))
		_ = c.Close()
	}
}

// handleUDPOpen is the agent-side handler: dial UDP to addr and relay
// datagrams. Reuses the same AcceptFunc as TCP (the agent decides how to
// dial based on the addr type).
func (s *Session) handleUDPOpen(p []byte) error {
	if len(p) < ConnIDLen {
		return errors.New("mux: UDP_OPEN: missing connID")
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	addr, err := decodeAddr(p[ConnIDLen:])
	if err != nil {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrProtocol)))
		return err
	}
	if s.cfg.AcceptUDPFunc == nil {
		_ = s.enqueueCtl(TypeErr, append(id[:], byte(ErrConnNotFound)))
		return nil
	}
	go s.acceptUDP(id, addr)
	return nil
}

// acceptUDP dials a UDP socket to addr on the agent and pipes datagrams.
func (s *Session) acceptUDP(id ConnID, addr Addr) {
	conn, err := s.cfg.AcceptUDPFunc(addr)
	if err != nil {
		_ = s.enqueueData(TypeErr, append(id[:], byte(ErrDialFailed)))
		return
	}
	c := newUDPConn(id, s, addr)
	s.registerUDP(c)
	okPayload := make([]byte, ConnIDLen)
	copy(okPayload, id[:])
	if err := s.enqueueData(TypeOpenOK, okPayload); err != nil {
		conn.Close()
		return
	}
	c.markReady()
	pipeUDP(c, conn)
}

func (s *Session) handleClose(p []byte) {
	if len(p) < ConnIDLen {
		return
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	if c := s.lookup(id); c != nil {
		c.markClosed(io.EOF)
		s.unregister(id)
		return
	}
	if c := s.lookupUDP(id); c != nil {
		c.markClosed(io.EOF)
		s.unregisterUDP(id)
	}
}

func (s *Session) handleErr(p []byte) {
	if len(p) < ConnIDLen {
		return
	}
	var id ConnID
	copy(id[:], p[:ConnIDLen])
	reason := ErrUnknown
	if len(p) > ConnIDLen {
		reason = Err(p[ConnIDLen])
	}
	err := fmt.Errorf("mux: remote error: %s", reason)
	if c := s.lookup(id); c != nil {
		c.markFailed(err)
		c.markClosed(err)
		s.unregister(id)
		return
	}
	if c := s.lookupUDP(id); c != nil {
		c.markFailed(err)
		c.markClosed(err)
		s.unregisterUDP(id)
	}
}

func (s *Session) pingLoop() {
	t := time.NewTicker(s.cfg.PingEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if err := s.enqueueCtlTimeout(TypePing, nil, pingWriteTimeout); err != nil {
				return
			}
			select {
			case <-s.pongCh:
			case <-time.After(s.cfg.PongTimeout):
				s.fail(fmt.Errorf("mux: pong timeout"))
				return
			case <-s.done:
				return
			}
		}
	}
}

func (s *Session) register(c *Conn) {
	s.connsMu.Lock()
	s.conns[c.id] = c
	s.connsMu.Unlock()
}

func (s *Session) unregister(id ConnID) {
	s.connsMu.Lock()
	delete(s.conns, id)
	s.connsMu.Unlock()
}

func (s *Session) lookup(id ConnID) *Conn {
	s.connsMu.Lock()
	c := s.conns[id]
	s.connsMu.Unlock()
	return c
}

// OpenUDP initiates a UDP association to addr (local/client side). Blocks until
// the peer acknowledges (OPEN_OK) or reports a failure (ERR).
func (s *Session) OpenUDP(addr Addr) (*UDPConn, error) {
	if s.closed.Load() {
		return nil, ErrSessionClosed
	}
	var id ConnID
	if _, err := readRand(id[:]); err != nil {
		return nil, err
	}
	c := newUDPConn(id, s, addr)
	s.registerUDP(c)

	payload := make([]byte, 0, ConnIDLen+1+maxAddrLen(addr)+2)
	payload = append(payload, id[:]...)
	payload = encodeAddr(payload, addr)
	if err := s.enqueueData(TypeUDPOpen, payload); err != nil {
		s.unregisterUDP(id)
		return nil, err
	}

	select {
	case <-c.ready:
		return c, nil
	case err := <-c.errCh:
		s.unregisterUDP(id)
		return nil, err
	case <-s.done:
		s.unregisterUDP(id)
		return nil, ErrSessionClosed
	}
}

func (s *Session) registerUDP(c *UDPConn) {
	s.udpMu.Lock()
	s.udpConns[c.id] = c
	s.udpMu.Unlock()
}

func (s *Session) unregisterUDP(id ConnID) {
	s.udpMu.Lock()
	delete(s.udpConns, id)
	s.udpMu.Unlock()
}

func (s *Session) lookupUDP(id ConnID) *UDPConn {
	s.udpMu.Lock()
	c := s.udpConns[id]
	s.udpMu.Unlock()
	return c
}

func (s *Session) closeAllConns() {
	s.connsMu.Lock()
	conns := make([]*Conn, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[ConnID]*Conn)
	s.connsMu.Unlock()
	for _, c := range conns {
		c.markClosed(ErrSessionClosed)
	}

	s.udpMu.Lock()
	udps := make([]*UDPConn, 0, len(s.udpConns))
	for _, c := range s.udpConns {
		udps = append(udps, c)
	}
	s.udpConns = make(map[ConnID]*UDPConn)
	s.udpMu.Unlock()
	for _, c := range udps {
		c.markClosed(ErrSessionClosed)
	}
}

func maxAddrLen(a Addr) int {
	switch a.Type {
	case AddrIPv4:
		return 4 + 2
	case AddrIPv6:
		return 16 + 2
	case AddrDomain:
		return 1 + len(a.Domain) + 2
	}
	return 0
}

// readRand fills b with random bytes (factored so tests can swap it).
var readRand = func(b []byte) (int, error) {
	return randReader.Read(b)
}

// randReader is set in conn_random.go (crypto/rand). Kept as a var for tests.
var randReader io.Reader = cryptoRandReader{}
