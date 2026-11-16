// Package netstack wires a TUN device (io.ReadWriter) to a gVisor userspace
// TCP/IP stack and dispatches accepted TCP/UDP connections to a handler.
//
// This is a clean reimplementation of the ~200 lines of glue that tun2socks
// (GPL-3.0) places on top of gvisor (Apache-2.0). No tun2socks code is
// vendored; the new binary stays MIT.
package netstack

import (
	"errors"
	"fmt"

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
	ReadFrom(p []byte) (n int, addr Addr, err error)
	WriteTo(p []byte, addr Addr) (n int, err error)
	Close() error
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
	if err := s.CreateNICWithOptions(nicID, cfg.Link, stack.NICOptions{}); err != nil {
		s.Close()
		return nil, fmt.Errorf("netstack: create NIC: %s", err)
	}
	// Promiscuous + spoofing: deliver packets for any destination address
	// (remote services live on routed networks, not on the TUN subnet).
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("netstack: promiscuous: %s", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		s.Close()
		return nil, fmt.Errorf("netstack: spoofing: %s", err)
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	if err := applyStackDefaults(s); err != nil {
		s.Close()
		return nil, err
	}

	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder(s, cfg.Handler).HandlePacket)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder(s, cfg.Handler).HandlePacket)
	return s, nil
}

// applyStackDefaults sets the sane TCP/ICMP defaults tun2socks used.
func applyStackDefaults(s *stack.Stack) error {
	ttl := tcpip.DefaultTTLOption(64)
	if err := toErr(s.SetNetworkProtocolOption(ipv4.ProtocolNumber, &ttl)); err != nil {
		return err
	}
	if err := toErr(s.SetNetworkProtocolOption(ipv6.ProtocolNumber, &ttl)); err != nil {
		return err
	}
	if err := toErr(s.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, true)); err != nil {
		return err
	}
	if err := toErr(s.SetForwardingDefaultAndAllNICs(ipv6.ProtocolNumber, true)); err != nil {
		return err
	}
	s.SetICMPBurst(50)
	cc := tcpip.CongestionControlOption("reno")
	if err := toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &cc)); err != nil {
		return err
	}
	delay := tcpip.TCPDelayEnabled(false)
	if err := toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &delay)); err != nil {
		return err
	}
	moderate := tcpip.TCPModerateReceiveBufferOption(false)
	if err := toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &moderate)); err != nil {
		return err
	}
	sack := tcpip.TCPSACKEnabled(true)
	if err := toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)); err != nil {
		return err
	}
	recovery := tcpip.TCPRACKLossDetection
	if err := toErr(s.SetTransportProtocolOption(tcp.ProtocolNumber, &recovery)); err != nil {
		return err
	}
	return nil
}

// toErr converts a tcpip.Error (which does not implement the standard error
// interface since gVisor dropped Error() string) into a Go error.
func toErr(e tcpip.Error) error {
	if e == nil {
		return nil
	}
	return fmt.Errorf("%s", e)
}
