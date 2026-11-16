package netstack

import (
	"net"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func withUDPHandler(s *stack.Stack, _ tcpip.NICID, h Handler) Option {
	return func(s *stack.Stack) error {
		fwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
			var (
				wq waiter.Queue
				id = r.ID()
			)
			ep, err := r.CreateEndpoint(&wq)
			if err != nil {
				return
			}
			conn := &udpConn{
				UDPConn: gonet.NewUDPConn(&wq, ep),
				id: TransportEndpointID{
					LocalAddress:  id.LocalAddress,
					LocalPort:     id.LocalPort,
					RemoteAddress: id.RemoteAddress,
					RemotePort:    id.RemotePort,
				},
			}
			// Off the dispatch path, same as the TCP forwarder (option.go).
			go h.HandleUDP(conn)
		})
		s.SetTransportProtocolHandler(udp.ProtocolNumber, fwd.HandlePacket)
		return nil
	}
}

// udpConn implements UDPConn.
type udpConn struct {
	*gonet.UDPConn
	id TransportEndpointID
}

func (c *udpConn) ID() TransportEndpointID { return c.id }

// ReadFrom / WriteTo satisfy UDPConn; gonet.UDPConn already provides them but
// with a net.Addr. We adapt to our Addr type.
func (c *udpConn) ReadFrom(p []byte) (int, Addr, error) {
	n, addr, err := c.UDPConn.ReadFrom(p)
	if err != nil {
		return n, Addr{}, err
	}
	return n, addrFromNet(addr), nil
}

func (c *udpConn) WriteTo(p []byte, a Addr) (int, error) {
	return c.UDPConn.WriteTo(p, netUDPAddr(a))
}

// addrFromNet converts a net.Addr (as returned by gonet.UDPConn.ReadFrom) to
// our Addr. Non-UDP or nil addrs yield an empty Addr.
func addrFromNet(a net.Addr) Addr {
	ua, ok := a.(*net.UDPAddr)
	if !ok || ua == nil {
		return Addr{}
	}
	return Addr{IP: tcpip.AddrFromSlice(ua.IP), Port: uint16(ua.Port)}
}

// netUDPAddr converts our Addr into a *net.UDPAddr, which is the concrete type
// gonet.UDPConn.WriteTo type-asserts on.
func netUDPAddr(a Addr) *net.UDPAddr {
	return &net.UDPAddr{IP: net.IP(a.IP.AsSlice()), Port: int(a.Port)}
}

var _ UDPConn = (*udpConn)(nil)
