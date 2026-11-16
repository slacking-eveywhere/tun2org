// Package netstack wires a TUN device (io.ReadWriter) to a gVisor userspace
// TCP/IP stack and dispatches accepted TCP/UDP connections to a handler.
//
// This is a clean reimplementation of the ~200 lines of glue that tun2socks
// (GPL-3.0) places on top of gvisor (Apache-2.0). No tun2socks code is
// vendored; the new binary stays MIT.
package netstack

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// LinkEndpoint is the io.ReadWriter-backed link endpoint. It reads inbound
// packets from rw and writes outbound packets to rw via a gVisor channel
// endpoint.
type LinkEndpoint struct {
	*channel.Endpoint

	rw   io.ReadWriter
	mtu  uint32
	off  int
	once sync.Once
	wg   sync.WaitGroup

	cancel context.CancelFunc
}

// NewLinkEndpoint wraps rw as a stack.LinkEndpoint.
func NewLinkEndpoint(rw io.ReadWriter, mtu uint32, offset int) (*LinkEndpoint, error) {
	if mtu == 0 {
		return nil, errors.New("netstack: mtu is zero")
	}
	if rw == nil {
		return nil, errors.New("netstack: rw is nil")
	}
	if offset < 0 {
		return nil, errors.New("netstack: offset is negative")
	}
	return &LinkEndpoint{
		Endpoint: channel.New(defaultOutQueueLen, mtu, ""),
		rw:       rw,
		mtu:      mtu,
		off:      offset,
	}, nil
}

// Attach launches the read/write loops the first time a dispatcher is set, and
// stops the outbound loop when the stack detaches (dispatcher == nil). Detach is
// the stack's signal to halt the loops: Stack.Wait() blocks on LinkEndpoint.Wait()
// *before* it runs LinkEndpoint.Close(), so the loops must be stoppable here.
func (e *LinkEndpoint) Attach(dispatcher stack.NetworkDispatcher) {
	if dispatcher == nil {
		e.Endpoint.Attach(nil)
		e.cancelLoops()
		return
	}
	e.Endpoint.Attach(dispatcher)
	e.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.wg.Add(2)
		go func() { defer e.wg.Done(); e.dispatchLoop(ctx) }()
		go func() { defer e.wg.Done(); e.outboundLoop(ctx) }()
	})
}

// Wait blocks until both loops exit (after Close or detach + device close).
func (e *LinkEndpoint) Wait() { e.wg.Wait() }

// Close stops the loops: cancels the outbound loop's context and closes the
// channel endpoint so a blocked ReadContext also unblocks. The underlying rw
// must be closed separately to unblock a pending Read in the dispatch loop.
func (e *LinkEndpoint) Close() {
	e.cancelLoops()
	e.Endpoint.Close()
}

func (e *LinkEndpoint) cancelLoops() {
	if e.cancel != nil {
		e.cancel()
	}
}

// dispatchLoop reads packets from rw and injects them into the stack.
// A read error mid-run is fatal for the whole tunnel (the stack goes deaf:
// no ping, no connections, while routes and the relay session stay up), so it
// is logged loudly; app.Run watches Wait() and tears the tunnel down.
func (e *LinkEndpoint) dispatchLoop(ctx context.Context) {
	pktSize := int(e.mtu) + e.off
	for {
		data := make([]byte, pktSize)
		n, err := e.rw.Read(data)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("netstack: tun read failed: %v — TUN dispatch loop exiting, tunnel is now deaf", err)
			}
			return
		}
		if n <= e.off || n-e.off > int(e.mtu) {
			continue
		}
		if !e.IsAttached() {
			continue
		}
		payload := data[e.off:n]
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(payload),
		})
		switch header.IPVersion(payload) {
		case header.IPv4Version:
			e.InjectInbound(header.IPv4ProtocolNumber, pkt)
		case header.IPv6Version:
			e.InjectInbound(header.IPv6ProtocolNumber, pkt)
		}
		pkt.DecRef()
	}
}

// outboundLoop drains packets the stack wants to send and writes them to rw.
func (e *LinkEndpoint) outboundLoop(ctx context.Context) {
	for {
		pkt := e.ReadContext(ctx)
		if pkt == nil {
			if ctx.Err() == nil {
				log.Printf("netstack: outbound loop exited with live context — stack can no longer send")
			}
			return
		}
		e.writePacket(pkt)
	}
}

func (e *LinkEndpoint) writePacket(pkt *stack.PacketBuffer) tcpip.Error {
	defer pkt.DecRef()
	buf := pkt.ToBuffer()
	defer buf.Release()
	if e.off != 0 {
		v := buffer.NewViewWithData(make([]byte, e.off))
		_ = buf.Prepend(v)
	}
	if _, err := e.rw.Write(buf.Flatten()); err != nil {
		return &tcpip.ErrInvalidEndpointState{}
	}
	return nil
}

const defaultOutQueueLen = 1 << 10
