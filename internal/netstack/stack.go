package netstack

import (
	"errors"
	"fmt"
	"io"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// Handler receives accepted TCP/UDP connections from the netstack.
type Handler interface {
	HandleTCP(TCPConn)
	HandleUDP(UDPConn)
}

// TCPConn is an accepted TCP connection from the netstack.
type TCPConn interface {
	// Embedded so callers can io.Copy etc.
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	// LocalAddr/RemoteAddr of the original packet (target = local addr of TUN).
	ID() TransportEndpointID
}

// UDPConn is an accepted UDP connection from the netstack.
type UDPConn interface {
	io.Closer
	ReadFrom(p []byte) (n int, addr Addr, err error)
	WriteTo(p []byte, addr Addr) (n int, err error)
	ID() TransportEndpointID
}

// TransportEndpointID is the 4-tuple of an accepted connection.
type TransportEndpointID struct {
	LocalAddress  tcpip.Address
	LocalPort     uint16
	RemoteAddress tcpip.Address
	RemotePort    uint16
}

// Addr is a UDP peer address.
type Addr struct {
	IP   tcpip.Address
	Port uint16
}

// Config configures CreateStack.
type Config struct {
	Link    *LinkEndpoint
	Handler Handler
	Options []Option
}

// CreateStack builds a gVisor stack with ipv4+ipv6, tcp+udp+icmp, promiscuous
// + spoofing + default routes, and TCP/UDP forwarders wired to the handler.
func CreateStack(cfg Config) (*stack.Stack, error) {
	if cfg.Link == nil {
		return nil, errors.New("netstack: link is nil")
	}
	if cfg.Handler == nil {
		return nil, errors.New("netstack: handler is nil")
	}

	opts := []Option{WithDefault()}
	opts = append(opts, cfg.Options...)

	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			icmp.NewProtocol4,
			icmp.NewProtocol6,
		},
	})

	nicID := s.NextNICID()

	allOpts := append([]Option{}, opts...)
	allOpts = append(allOpts,
		withTCPHandler(s, nicID, cfg.Handler),
		withUDPHandler(s, nicID, cfg.Handler),
		withCreatingNIC(nicID, cfg.Link),
		withPromiscuousMode(nicID),
		withSpoofing(nicID),
		withRouteTable(nicID),
	)

	for _, opt := range allOpts {
		if err := opt(s); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

const (
	nicPromiscuousModeEnabled  = true
	nicSpoofingEnabled       = true
)

func withCreatingNIC(nicID tcpip.NICID, ep stack.LinkEndpoint) Option {
	return func(s *stack.Stack) error {
		if err := s.CreateNICWithOptions(nicID, ep, stack.NICOptions{}); err != nil {
			return fmt.Errorf("netstack: create NIC: %s", err)
		}
		return nil
	}
}

func withPromiscuousMode(nicID tcpip.NICID) Option {
	return func(s *stack.Stack) error {
		if err := s.SetPromiscuousMode(nicID, nicPromiscuousModeEnabled); err != nil {
			return fmt.Errorf("netstack: promiscuous: %s", err)
		}
		return nil
	}
}

func withSpoofing(nicID tcpip.NICID) Option {
	return func(s *stack.Stack) error {
		if err := s.SetSpoofing(nicID, nicSpoofingEnabled); err != nil {
			return fmt.Errorf("netstack: spoofing: %s", err)
		}
		return nil
	}
}

func withRouteTable(nicID tcpip.NICID) Option {
	return func(s *stack.Stack) error {
		s.SetRouteTable([]tcpip.Route{
			{Destination: header.IPv4EmptySubnet, NIC: nicID},
			{Destination: header.IPv6EmptySubnet, NIC: nicID},
		})
		return nil
	}
}
