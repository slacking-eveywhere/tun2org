//go:build darwin && arm64

package tun

import wg "golang.zx2c4.com/wireguard/tun"

// createTUN opens a macOS utun device (Apple Silicon / arm64 only). utun
// prepends a 4-byte protocol-family header to each packet; packetOffset
// accounts for it in the netstack link endpoint.
func createTUN(name string, mtu int) (wg.Device, error) {
	return wg.CreateTUN(name, mtu)
}

// packetOffset is 4 on macOS: utun packets carry a 4-byte protocol header.
func packetOffset() int { return 4 }

// writeOffset is the front margin the wireguard utun device needs before each
// written packet: it writes a 4-byte AF_INET/AF_INET6 header into it.
func writeOffset() int { return 4 }
