//go:build !linux

package relay

import "net"

// bindToInterfaceOS is a no-op on non-Linux (macOS binds via the dialer's
// dialcontext + Interface; TUN routing avoids loops differently there).
func bindToInterfaceOS(_ net.Conn, _ string) error { return nil }
