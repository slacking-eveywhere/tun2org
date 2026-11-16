package netstack

import (
	"fmt"
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Option configures the stack.
type Option func(*stack.Stack) error

// toErr converts a tcpip.Error (which does not implement the standard error
// interface since gVisor dropped Error() string) into a Go error.
func toErr(e tcpip.Error) error {
	if e == nil {
		return nil
	}
	return fmt.Errorf("%s", e)
}

const (
	defaultWndSize       = 0
	maxConnAttempts      = 2 << 10
	tcpKeepaliveCount    = 9
	tcpKeepaliveIdle     = 60 * time.Second
	tcpKeepaliveInterval = 30 * time.Second
)

// WithDefault applies sane TCP/ICMP defaults to the stack.
func WithDefault() Option {
	return func(s *stack.Stack) error {
		opts := []Option{
			WithDefaultTTL(64),
			WithForwarding(true),
			WithICMPBurst(50),
			WithTCPCongestionControl("reno"),
			WithTCPDelay(false),
			WithTCPModerateReceiveBuffer(false),
			WithTCPSACKEnabled(true),
			WithTCPRecovery(tcpip.TCPRACKLossDetection),
		}
		for _, o := range opts {
			if err := o(s); err != nil {
				return err
			}
		}
		return nil
	}
}

func WithDefaultTTL(ttl uint8) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.DefaultTTLOption(ttl)
		if err := toErr(s.SetNetworkProtocolOption(ipv4.ProtocolNumber, &opt)); err != nil {
			return err
		}
		return toErr(s.SetNetworkProtocolOption(ipv6.ProtocolNumber, &opt))
	}
}

func WithForwarding(v bool) Option {
	return func(s *stack.Stack) error {
		if err := toErr(s.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, v)); err != nil {
			return err
		}
		return toErr(s.SetForwardingDefaultAndAllNICs(ipv6.ProtocolNumber, v))
	}
}

func WithICMPBurst(burst int) Option {
	return func(s *stack.Stack) error {
		s.SetICMPBurst(burst)
		return nil
	}
}

func WithTCPCongestionControl(cc string) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.CongestionControlOption(cc)
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

func WithTCPDelay(v bool) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.TCPDelayEnabled(v)
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

func WithTCPModerateReceiveBuffer(v bool) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.TCPModerateReceiveBufferOption(v)
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

func WithTCPSACKEnabled(v bool) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.TCPSACKEnabled(v)
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

func WithTCPRecovery(v tcpip.TCPRecovery) Option {
	return func(s *stack.Stack) error {
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &v))
	}
}

// WithTCPSendBufferSizeRange sets the TCP send buffer size range.
func WithTCPSendBufferSizeRange(min, def, max int) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.TCPSendBufferSizeRangeOption{Min: min, Default: def, Max: max}
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

// WithTCPReceiveBufferSizeRange sets the TCP receive buffer size range.
func WithTCPReceiveBufferSizeRange(min, def, max int) Option {
	return func(s *stack.Stack) error {
		opt := tcpip.TCPReceiveBufferSizeRangeOption{Min: min, Default: def, Max: max}
		return toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &opt))
	}
}

func withTCPHandler(s *stack.Stack, _ tcpip.NICID, h Handler) Option {
	return func(s *stack.Stack) error {
		fwd := tcp.NewForwarder(s, defaultWndSize, maxConnAttempts, func(r *tcp.ForwarderRequest) {
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
		s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
		return nil
	}
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
