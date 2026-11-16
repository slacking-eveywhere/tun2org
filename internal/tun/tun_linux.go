//go:build linux

package tun

import (
	"fmt"
	"os"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	wg "golang.zx2c4.com/wireguard/tun"
)

// createTUN opens a Linux IFF_TUN device (raw IP, no packet-info header)
// WITHOUT IFF_VNET_HDR and without offload negotiation.
//
// Reason: golang.zx2c4.com/wireguard/tun's CreateTUN always requests
// IFF_VNET_HDR and then enables TUNSETOFFLOAD (TSO). The kernel then hands
// GSO skbs of up to 64KiB to reads, which the wireguard reader splits into
// one buffer per segment — and it returns ErrTooManySegments ("too many
// segments") when handed fewer buffers than segments. ntun reads with a
// single buffer, so the first multi-segment skb routed through the TUN
// killed the dispatch loop: the stack went deaf while routes and the SSH
// session stayed up (the "everything dead but the tunnel looks alive" field
// report). Without IFF_VNET_HDR the kernel advertises no offloads and every
// read returns exactly one ≤MTU packet.
func createTUN(name string, mtu int) (wg.Device, error) {
	if name == "" {
		name = "tun0"
	}
	if mtu <= 0 {
		mtu = defaultMTU
	}

	fd, err := unix.Open(cloneDevicePath, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tun: open %s does not exist", cloneDevicePath)
		}
		return nil, fmt.Errorf("tun: open clone device: %w", err)
	}

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tun: TUNSETIFF %s: %w", name, err)
	}
	realName := ifr.Name()

	// Non-blocking + os.NewFile so the fd is registered with the Go runtime
	// poller: reads park in epoll (not a raw syscall) and Close reliably
	// unblocks a pending read. Same contract as wireguard/tun.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), cloneDevicePath)

	if err := setMTU(realName, mtu); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("tun: set mtu %d on %s: %w", mtu, realName, err)
	}
	realMTU := mtu
	if link, err := netlink.LinkByName(realName); err == nil && link.Attrs().MTU > 0 {
		realMTU = link.Attrs().MTU
	}

	return &rawTun{file: file, name: realName, mtu: realMTU}, nil
}

// cloneDevicePath is where new TUN fds come from on Linux.
const cloneDevicePath = "/dev/net/tun"

// rawTun is a minimal TUN device: one raw IP packet per Read/Write, no
// virtio headers, no GSO/GRO, no batching.
type rawTun struct {
	file *os.File
	name string
	mtu  int
}

var _ wg.Device = (*rawTun)(nil)

func (t *rawTun) Name() (string, error) { return t.name, nil }
func (t *rawTun) MTU() (int, error)     { return t.mtu, nil }
func (t *rawTun) BatchSize() int        { return 1 }

// Events is required by the wireguard Device interface; ntun never listens
// for device events, so a nil channel (receive blocks forever) is fine.
func (t *rawTun) Events() <-chan wg.Event { return nil }

// File exposes the underlying fd (unused by ntun, required by the
// interface).
func (t *rawTun) File() *os.File { return t.file }

func (t *rawTun) Read(buffs [][]byte, sizes []int, offset int) (int, error) {
	n, err := t.file.Read(buffs[0][offset:])
	if err != nil {
		return 0, err
	}
	sizes[0] = n
	return 1, nil
}

func (t *rawTun) Write(buffs [][]byte, offset int) (int, error) {
	return t.file.Write(buffs[0][offset:])
}

func (t *rawTun) Close() error { return t.file.Close() }

// packetOffset is 0 on Linux: IFF_TUN|IFF_NO_PI carries raw IP packets with
// no header.
func packetOffset() int { return 0 }

// writeOffset is 0 on Linux: no IFF_VNET_HDR means the device expects a bare
// IP packet on writes, with no front margin.
func writeOffset() int { return 0 }
