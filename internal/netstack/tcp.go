package netstack

import (
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	defaultWndSize       = 0
	maxConnAttempts      = 2 << 10
	tcpKeepaliveCount    = 9
	tcpKeepaliveIdle     = 60 * time.Second
	tcpKeepaliveInterval = 30 * time.Second
)

// tcpForwarder accepts every inbound TCP connection from the stack and hands
// it to the handler.
func tcpForwarder(s *stack.Stack, h Handler) *tcp.Forwarder {
	return tcp.NewForwarder(s, defaultWndSize, maxConnAttempts, func(r *tcp.ForwarderRequest) {
		var (
			wq waiter.Queue
			id = r.ID()
		)
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true) // RST: prevent half-open leak
			return
		}
		r.Complete(false)
		setTCPSockOpts(ep)
		conn := &tcpConn{
			TCPConn: gonet.NewTCPConn(&wq, ep),
			id: TransportEndpointID{
				LocalAddress:  id.LocalAddress,
				LocalPort:     id.LocalPort,
				RemoteAddress: id.RemoteAddress,
				RemotePort:    id.RemotePort,
			},
		}
		// The forwarder callback runs on the stack's dispatch goroutine
		// (the TUN read loop); the handler dials the mux which can block
		// (relay reconnect, OPEN_OK round trip). Run it off the dispatch
		// path so one wedged connection cannot stall the whole TUN.
		go h.HandleTCP(conn)
	})
}

// setTCPSockOpts enables keepalive on the accepted endpoint.
func setTCPSockOpts(ep tcpip.Endpoint) {
	ep.SocketOptions().SetKeepAlive(true)
	idle := tcpip.KeepaliveIdleOption(tcpKeepaliveIdle)
	_ = ep.SetSockOpt(&idle)
	interval := tcpip.KeepaliveIntervalOption(tcpKeepaliveInterval)
	_ = ep.SetSockOpt(&interval)
	_ = ep.SetSockOptInt(tcpip.KeepaliveCountOption, tcpKeepaliveCount)
}

// tcpConn wraps gonet.TCPConn and carries the transport endpoint id.
type tcpConn struct {
	*gonet.TCPConn
	id TransportEndpointID
}

func (c *tcpConn) ID() TransportEndpointID { return c.id }

// Compile-time interface checks.
var (
	_ TCPConn  = (*tcpConn)(nil)
	_ net.Conn = (*tcpConn)(nil)
)
