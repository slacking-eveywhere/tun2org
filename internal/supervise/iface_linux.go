//go:build linux

package supervise

import (
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

// defaultInterfaceNetlink returns the interface holding the IPv4 default route
// on Linux, via a netlink route dump. Returns ("", false) if unavailable so the
// portable net-based fallback is used.
func defaultInterfaceNetlink() (string, bool) {
	rl, err := netlink.RouteList(nil, nl.FAMILY_V4)
	if err != nil {
		return "", false
	}
	for _, r := range rl {
		// Default route: zero destination.
		if r.Dst == nil || r.Dst.IP.IsUnspecified() {
			if r.LinkIndex > 0 {
				if link, err := netlink.LinkByIndex(r.LinkIndex); err == nil {
					return link.Attrs().Name, true
				}
			}
		}
	}
	return "", false
}
