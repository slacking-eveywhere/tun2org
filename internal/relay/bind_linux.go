//go:build linux

package relay

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindToInterfaceOS binds conn's fd to the named interface via SO_BINDTODEVICE
// so the SSH transport never routes through the TUN device.
func bindToInterfaceOS(conn net.Conn, iface string) error {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("relay: conn is not a syscall.Conn")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return fmt.Errorf("relay: raw conn: %w", err)
	}
	var setErr error
	err = rc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
	})
	if err != nil {
		return fmt.Errorf("relay: control fd: %w", err)
	}
	if setErr != nil {
		return fmt.Errorf("relay: SO_BINDTODEVICE %s: %w", iface, setErr)
	}
	return nil
}
