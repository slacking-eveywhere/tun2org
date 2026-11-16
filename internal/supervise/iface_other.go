//go:build !linux

package supervise

// defaultInterfaceNetlink is unavailable off-Linux; the portable
// defaultInterfaceNet fallback is used instead.
func defaultInterfaceNetlink() (string, bool) { return "", false }
