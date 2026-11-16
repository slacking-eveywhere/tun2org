package mux

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWriteReadFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello world")
	if err := writeFrame(&buf, TypeData, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	f, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if f.Type != TypeData {
		t.Fatalf("type = %x, want %x", f.Type, TypeData)
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Fatalf("payload = %q, want %q", f.Payload, payload)
	}
}

func TestWriteReadFrameEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, TypePing, nil); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	f, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if f.Type != TypePing {
		t.Fatalf("type = %x, want %x", f.Type, TypePing)
	}
	if len(f.Payload) != 0 {
		t.Fatalf("payload len = %d, want 0", len(f.Payload))
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	var hdr [headerLen]byte
	binary.BigEndian.PutUint32(hdr[:4], MaxFrameLen+1)
	hdr[4] = byte(TypeData)
	buf.Write(hdr[:])
	if _, err := readFrame(&buf); err == nil {
		t.Fatal("expected error for oversized frame, got nil")
	}
}

func TestWriteFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	big := make([]byte, MaxFrameLen+1)
	if err := writeFrame(&buf, TypeData, big); err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
}

func TestEncodeDecodeAddrIPv4(t *testing.T) {
	a := Addr{Type: AddrIPv4, IPv4: [4]byte{10, 0, 0, 1}, Port: 443}
	enc := encodeAddr(nil, a)
	got, err := decodeAddr(enc)
	if err != nil {
		t.Fatalf("decodeAddr: %v", err)
	}
	if got.Type != a.Type || got.IPv4 != a.IPv4 || got.Port != a.Port {
		t.Fatalf("got %+v, want %+v", got, a)
	}
}

func TestEncodeDecodeAddrIPv6(t *testing.T) {
	a := Addr{Type: AddrIPv6, Port: 8080}
	for i := range a.IPv6 {
		a.IPv6[i] = byte(i)
	}
	enc := encodeAddr(nil, a)
	got, err := decodeAddr(enc)
	if err != nil {
		t.Fatalf("decodeAddr: %v", err)
	}
	if got.IPv6 != a.IPv6 || got.Port != a.Port {
		t.Fatalf("got %+v, want %+v", got, a)
	}
}

func TestEncodeDecodeAddrDomain(t *testing.T) {
	a := Addr{Type: AddrDomain, Domain: "jenkins-ci.cnes.fr", Port: 443}
	enc := encodeAddr(nil, a)
	got, err := decodeAddr(enc)
	if err != nil {
		t.Fatalf("decodeAddr: %v", err)
	}
	if got.Domain != a.Domain || got.Port != a.Port {
		t.Fatalf("got %+v, want %+v", got, a)
	}
}

func TestFromHostPort(t *testing.T) {
	cases := []struct {
		in   string
		typ  byte
		port uint16
	}{
		{"10.120.42.215", AddrIPv4, 443},
		{"::1", AddrIPv6, 80},
		{"jenkins-ci.bli.blou", AddrDomain, 443},
	}
	for _, c := range cases {
		a := FromHostPort(c.in, c.port)
		if a.Type != c.typ {
			t.Errorf("FromHostPort(%q).Type = %d, want %d", c.in, a.Type, c.typ)
		}
		if a.Port != c.port {
			t.Errorf("FromHostPort(%q).Port = %d, want %d", c.in, a.Port, c.port)
		}
		if a.String() == "" {
			t.Errorf("FromHostPort(%q).String empty", c.in)
		}
	}
}

func TestAddrString(t *testing.T) {
	a := FromHostPort("10.120.42.215", 443)
	if a.String() != "10.120.42.215:443" {
		t.Fatalf("String = %q", a.String())
	}
}
