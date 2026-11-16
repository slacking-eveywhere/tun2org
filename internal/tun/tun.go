// Package tun opens and owns the TUN device used to capture traffic that must
// traverse the tunnel. It is a thin wrapper over golang.zx2c4.com/wireguard/tun
// (MIT), exposing the device as an io.ReadWriter for the netstack link
// endpoint and recording state for idempotent teardown.
//
// Supported platforms: Linux (all arches) and macOS/Apple Silicon (arm64).
// darwin/amd64 is intentionally unsupported (build-tagged out).
package tun

import (
	"fmt"
	"io"
	"strings"

	wg "golang.zx2c4.com/wireguard/tun"
)

// Device is a TUN device opened by Open. It doubles as the io.ReadWriter the
// netstack link endpoint reads from / writes to.
type Device struct {
	dev    wg.Device
	offset int
	woff   int // front margin the wireguard device needs before each written packet
	name   string
	mtu    int
}

// Open creates the named TUN device. mtu <= 0 means "use driver default".
//
// On Linux this is IFF_TUN (no packet info, raw IP). On macOS/utun a 4-byte
// protocol-family header is prepended to every packet; offset accounts for it.
func Open(name string, mtu int) (*Device, error) {
	if name == "" {
		name = "tun0"
	}
	if mtu <= 0 {
		mtu = defaultMTU
	}
	dev, err := createTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("tun: open %s: %w", name, err)
	}
	realName, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("tun: name: %w", err)
	}
	realMTU, err := dev.MTU()
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("tun: mtu: %w", err)
	}
	return &Device{
		dev:    dev,
		offset: packetOffset(),
		woff:   writeOffset(),
		name:   realName,
		mtu:    realMTU,
	}, nil
}

// Name returns the OS-assigned interface name (may differ from requested).
func (d *Device) Name() string { return d.name }

// MTU returns the device MTU in bytes.
func (d *Device) MTU() int { return d.mtu }

// Offset returns the per-OS packet header offset (0 on Linux, 4 on macOS utun).
// The netstack link endpoint needs it to skip the protocol-family header.
func (d *Device) Offset() int { return d.offset }

// Read reads one packet from the TUN into p. It implements io.Reader for the
// netstack link endpoint.
func (d *Device) Read(p []byte) (int, error) {
	buffs := [][]byte{p}
	sizes := []int{0}
	if _, err := d.dev.Read(buffs, sizes, d.offset); err != nil {
		return 0, err
	}
	return sizes[0], nil
}

// Write writes one packet to the TUN from p. It implements io.Writer.
//
// The wireguard device requires a platform header (Linux IFF_VNET_HDR virtio
// header, macOS utun protocol-family header) to precede each packet, and it
// writes that header itself into the front margin. Pass a buffer with that
// headroom so the device sees [header][IP] instead of a bare [IP] packet.
func (d *Device) Write(p []byte) (int, error) {
	buf := make([]byte, d.woff+len(p))
	copy(buf[d.woff:], p)
	buffs := [][]byte{buf}
	n, err := d.dev.Write(buffs, d.woff)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Close closes the underlying device.
func (d *Device) Close() error {
	if d == nil || d.dev == nil {
		return nil
	}
	return d.dev.Close()
}

// AsReadWriter returns d as an io.ReadWriter (trivial; Device already is one).
// Convenience for the netstack link endpoint constructor.
func (d *Device) AsReadWriter() io.ReadWriter { return d }

// ErrorIsPlatformUnsupported reports whether err is the platform-not-supported
// sentinel returned by createTUN on unsupported OS/arch combos.
func ErrorIsPlatformUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), "platform unsupported")
}

// defaultMTU is overridden per-OS via build-tagged files if needed.
var defaultMTU = 1500
