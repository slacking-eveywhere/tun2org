// Package mux is a binary, length-prefixed connection multiplexer over any
// io.ReadWriteCloser (typically an SSH session stdin/stdout pipe).
//
// Wire format (all integers big-endian):
//
//	Frame:
//	  [4 bytes] payload length (uint32, <= MaxFrameLen)
//	  [1 byte ] frame type
//	  [payload]
//
//	OPEN     (0x01) payload: [16 connID][1 addrType][addr][2 port]
//	OPEN_OK  (0x02) payload: [16 connID]
//	DATA     (0x03) payload: [16 connID][data...]
//	CLOSE    (0x04) payload: [16 connID]
//	ERR      (0x05) payload: [16 connID][1 reason]
//	PING     (0x06) payload: (empty)
//	PONG     (0x07) payload: (empty)
//	UDP_OPEN (0x08) payload: [16 connID][1 addrType][addr][2 port]
//	UDP_DATA (0x09) payload: [16 connID][data...]
//
// addr encodings:
//
//	AddrIPv4   (1): 4 bytes
//	AddrDomain (3): 1 byte length + bytes
//	AddrIPv6   (4): 16 bytes
package mux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// Frame type identifiers.
type FrameType byte

const (
	TypeOpen    FrameType = 0x01
	TypeOpenOK  FrameType = 0x02
	TypeData    FrameType = 0x03
	TypeClose   FrameType = 0x04
	TypeErr     FrameType = 0x05
	TypePing    FrameType = 0x06
	TypePong    FrameType = 0x07
	TypeUDPOpen FrameType = 0x08
	TypeUDPData FrameType = 0x09
)

// Address type identifiers embedded in OPEN/UDP_OPEN payloads.
const (
	AddrIPv4   byte = 1
	AddrDomain byte = 3
	AddrIPv6   byte = 4
)

const (
	// ConnIDLen is the length of a connection identifier in bytes.
	ConnIDLen = 16

	// MaxDataLen is the largest application-data chunk carried in a single
	// DATA frame. Larger writes are split into MaxDataLen frames.
	MaxDataLen = 64 * 1024

	// MaxFrameLen is the maximum payload length of any frame. DATA payloads
	// are [16 connID][<= MaxDataLen data], so this is ConnIDLen + MaxDataLen.
	MaxFrameLen = ConnIDLen + MaxDataLen

	// headerLen is the fixed frame header: 4 bytes length + 1 byte type.
	headerLen = 5
)

// Err is a protocol-level error reason carried in ERR frames.
type Err byte

const (
	ErrUnknown Err = iota
	ErrDialFailed
	ErrConnNotFound
	ErrOverflow
	ErrProtocol
)

func (e Err) String() string {
	switch e {
	case ErrDialFailed:
		return "dial failed"
	case ErrConnNotFound:
		return "connection not found"
	case ErrOverflow:
		return "recv buffer overflow"
	case ErrProtocol:
		return "protocol error"
	default:
		return "unknown error"
	}
}

// ConnID is a 16-byte connection identifier.
type ConnID [ConnIDLen]byte

// Addr is a tunneled destination address (host + port) in a neutral form.
// It implements net.Addr.
type Addr struct {
	Type   byte
	IPv4   [4]byte
	IPv6   [16]byte
	Domain string
	Port   uint16
}

// Network implements net.Addr.
func (a Addr) Network() string { return "tcp" }

// String returns host:port in canonical form.
func (a Addr) String() string {
	var host string
	switch a.Type {
	case AddrIPv4:
		host = net.IP(a.IPv4[:]).String()
	case AddrIPv6:
		host = net.IP(a.IPv6[:]).String()
	case AddrDomain:
		host = a.Domain
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", a.Port))
}

// FromHostPort resolves a host:port string into an Addr. Literal IPs become
// AddrIPv4/AddrIPv6; everything else becomes AddrDomain (resolution happens
// remotely, on the agent side, which is the whole point of tunneling).
func FromHostPort(host string, port uint16) Addr {
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return Addr{Type: AddrIPv4, IPv4: [4]byte(ip4), Port: port}
		}
		ip6 := ip.To16()
		var v6 [16]byte
		copy(v6[:], ip6)
		return Addr{Type: AddrIPv6, IPv6: v6, Port: port}
	}
	return Addr{Type: AddrDomain, Domain: host, Port: port}
}

// Frame is a decoded mux frame.
type Frame struct {
	Type    FrameType
	Payload []byte
}

// readFrame reads a single frame from r. The returned Payload slice is owned
// by the caller and safe to retain.
func readFrame(r io.Reader) (Frame, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(hdr[:4])
	if length > MaxFrameLen {
		return Frame{}, fmt.Errorf("mux: frame too large: %d (max %d)", length, MaxFrameLen)
	}
	typ := FrameType(hdr[4])
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Type: typ, Payload: payload}, nil
}

// writeFrame writes a single frame to w. It is not concurrency-safe; the
// Session serializes writes through its writer loop.
func writeFrame(w io.Writer, typ FrameType, payload []byte) error {
	if len(payload) > MaxFrameLen {
		return fmt.Errorf("mux: payload too large: %d (max %d)", len(payload), MaxFrameLen)
	}
	var hdr [headerLen]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(payload)))
	hdr[4] = byte(typ)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// encodeAddr writes the addr portion (without connID) into dst, growing it.
// Layout: [1 addrType][addr][2 port].
func encodeAddr(b []byte, a Addr) []byte {
	b = append(b, a.Type)
	switch a.Type {
	case AddrIPv4:
		b = append(b, a.IPv4[:]...)
	case AddrIPv6:
		b = append(b, a.IPv6[:]...)
	case AddrDomain:
		b = append(b, byte(len(a.Domain)))
		b = append(b, a.Domain...)
	}
	b = binary.BigEndian.AppendUint16(b, a.Port)
	return b
}

// decodeAddr parses the addr portion (without connID) from p.
func decodeAddr(p []byte) (Addr, error) {
	if len(p) < 1 {
		return Addr{}, errors.New("mux: empty addr")
	}
	a := Addr{Type: p[0]}
	p = p[1:]
	switch a.Type {
	case AddrIPv4:
		if len(p) < 4+2 {
			return Addr{}, fmt.Errorf("mux: short ipv4 addr")
		}
		copy(a.IPv4[:], p[:4])
		p = p[4:]
	case AddrIPv6:
		if len(p) < 16+2 {
			return Addr{}, fmt.Errorf("mux: short ipv6 addr")
		}
		copy(a.IPv6[:], p[:16])
		p = p[16:]
	case AddrDomain:
		if len(p) < 1 {
			return Addr{}, fmt.Errorf("mux: short domain addr")
		}
		dl := int(p[0])
		p = p[1:]
		if len(p) < dl+2 {
			return Addr{}, fmt.Errorf("mux: short domain body")
		}
		a.Domain = string(p[:dl])
		p = p[dl:]
	default:
		return Addr{}, fmt.Errorf("mux: unknown addr type %d", a.Type)
	}
	if len(p) < 2 {
		return Addr{}, fmt.Errorf("mux: missing port")
	}
	a.Port = binary.BigEndian.Uint16(p[:2])
	return a, nil
}
