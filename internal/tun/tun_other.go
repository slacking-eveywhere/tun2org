//go:build (darwin && amd64) || (!linux && !darwin)

package tun

import (
	"errors"

	wg "golang.zx2c4.com/wireguard/tun"
)

// createTUN refuses to build/run on unsupported platforms. ntun-ng targets
// Linux (any arch) and macOS/Apple Silicon (arm64) only.
func createTUN(name string, mtu int) (wg.Device, error) {
	return nil, errors.New("tun: platform unsupported (need linux or darwin/arm64)")
}

func packetOffset() int { return 0 }

func writeOffset() int { return 0 }
