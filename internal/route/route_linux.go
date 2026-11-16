//go:build linux

package route

import (
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

func addRoute(r Route) error {
	dst, err := netip.ParsePrefix(r.CIDR)
	if err != nil {
		return err
	}
	link, err := netlink.LinkByName(r.Device)
	if err != nil {
		return err
	}
	nlroute := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: dst.Masked().Addr().AsSlice(), Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen())},
	}
	return netlink.RouteAdd(nlroute)
}

func delRoute(r Route) error {
	dst, err := netip.ParsePrefix(r.CIDR)
	if err != nil {
		return err
	}
	link, err := netlink.LinkByName(r.Device)
	if err != nil {
		return err
	}
	nlroute := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: dst.Masked().Addr().AsSlice(), Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen())},
	}
	return netlink.RouteDel(nlroute)
}

func isExistsErr(err error) bool {
	return err != nil && (err.Error() == "file exists")
}

func isNotExistsErr(err error) bool {
	return err != nil && (err.Error() == "no such process")
}
