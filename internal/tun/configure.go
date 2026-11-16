package tun

import "fmt"

// Configure sets the interface address(es) and brings the link up. This is OS-
// specific (netlink on Linux, ifconfig on macOS). mtu <= 0 leaves it unchanged.
func (d *Device) Configure(addrCIDR, addrV6CIDR string, mtu int) error {
	if addrCIDR != "" {
		if err := setAddress(d.name, addrCIDR); err != nil {
			return fmt.Errorf("tun: set addr %s: %w", addrCIDR, err)
		}
	}
	if addrV6CIDR != "" {
		if err := setAddressV6(d.name, addrV6CIDR); err != nil {
			return fmt.Errorf("tun: set v6 addr %s: %w", addrV6CIDR, err)
		}
	}
	if mtu > 0 {
		if err := setMTU(d.name, mtu); err != nil {
			return fmt.Errorf("tun: set mtu %d: %w", mtu, err)
		}
	}
	if err := linkUp(d.name); err != nil {
		return fmt.Errorf("tun: link up: %w", err)
	}
	return nil
}
