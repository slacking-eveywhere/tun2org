package mux

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// udpRecvCap bounds the inbound UDP_DATA frames buffered per UDPConn.
const udpRecvCap = 128

// UDPDatagram is one inbound UDP datagram plus its source address (the peer
// the agent saw on its dialed socket).
type UDPDatagram struct {
	Data []byte
	From net.Addr
}

// UDPConn is a datagram-oriented connection over the mux (net.PacketConn).
//
// One UDPConn corresponds to one UDP_OPEN: a "connected" UDP association to a
// single target address on the agent side. The agent dials that target and
// relays datagrams both ways via UDP_DATA frames keyed by the same ConnID.
//
// It exists on both sides, keyed by the same ConnID. The local side calls
// Session.OpenUDP; the agent side creates its UDPConn on receipt of UDP_OPEN.
type UDPConn struct {
	id   ConnID
	sess *Session
	addr Addr // the target (local side) or the dialed peer (agent side)

	recvCh chan *UDPDatagram

	ready  chan struct{}
	errCh  chan error
	closed atomic.Bool
	done   chan struct{}
	err    error

	writeMu sync.Mutex
}

func newUDPConn(id ConnID, sess *Session, addr Addr) *UDPConn {
	return &UDPConn{
		id:     id,
		sess:   sess,
		addr:   addr,
		recvCh: make(chan *UDPDatagram, udpRecvCap),
		ready:  make(chan struct{}),
		errCh:  make(chan error, 1),
		done:   make(chan struct{}),
	}
}

// ID returns the ConnID.
func (c *UDPConn) ID() ConnID { return c.id }

// Addr returns the target/peer address.
func (c *UDPConn) Addr() Addr { return c.addr }

func (c *UDPConn) markReady() {
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
}

func (c *UDPConn) markFailed(err error) {
	select {
	case c.errCh <- err:
	default:
	}
}

func (c *UDPConn) markClosed(err error) {
	if c.closed.Swap(true) {
		return
	}
	c.err = err
	close(c.done)
}

// deliver enqueues an inbound datagram. false = overflow/closed.
func (c *UDPConn) deliver(d *UDPDatagram) bool {
	if c.closed.Load() {
		return false
	}
	select {
	case c.recvCh <- d:
		return true
	default:
		return false
	}
}

// ReadFrom implements net.PacketConn. The returned addr is the association
// target (the datagram source is not separately tracked per-packet).
// Drain-first: buffered datagrams are returned before the terminal error,
// mirroring Conn.Read (data immediately followed by CLOSE must not truncate).
func (c *UDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case d := <-c.recvCh:
		n := copy(p, d.Data)
		return n, c.addr.asNetAddr(), nil
	default:
	}

	select {
	case d := <-c.recvCh:
		n := copy(p, d.Data)
		return n, c.addr.asNetAddr(), nil
	case <-c.done:
		select {
		case d := <-c.recvCh:
			n := copy(p, d.Data)
			return n, c.addr.asNetAddr(), nil
		default:
			return 0, nil, c.err
		}
	}
}

// WriteTo implements net.PacketConn. The addr is ignored (the association is
// connected); data is sent as a UDP_DATA frame.
func (c *UDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, ErrSessionClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	payload := make([]byte, 0, ConnIDLen+len(p))
	payload = append(payload, c.id[:]...)
	payload = append(payload, p...)
	if err := c.sess.enqueueData(TypeUDPData, payload); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes the association and best-effort notifies the peer. The
// CLOSE goes on the data queue so it stays behind this association's queued
// UDP_DATA frames.
func (c *UDPConn) Close() error {
	c.markClosed(io.EOF)
	c.sess.unregisterUDP(c.id)
	_ = c.sess.enqueueData(TypeClose, c.id[:])
	return nil
}

func (c *UDPConn) LocalAddr() net.Addr  { return c.addr.asNetAddr() }
func (c *UDPConn) SetDeadline(time.Time) error      { return nil }
func (c *UDPConn) SetReadDeadline(time.Time) error  { return nil }
func (c *UDPConn) SetWriteDeadline(time.Time) error { return nil }

// asNetAddr converts the mux Addr to a *net.UDPAddr.
func (a Addr) asNetAddr() net.Addr {
	var ip net.IP
	switch a.Type {
	case AddrIPv4:
		ip = net.IP(a.IPv4[:])
	case AddrIPv6:
		ip = net.IP(a.IPv6[:])
	case AddrDomain:
		ip = net.ParseIP(a.Domain)
	}
	return &net.UDPAddr{IP: ip, Port: int(a.Port)}
}

var _ net.PacketConn = (*UDPConn)(nil)
