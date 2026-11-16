//go:build linux

package tun

import (
	"net/netip"

	"github.com/vishvananda/netlink"
)

func setAddress(name, cidr string) error {
	if _, err := netip.ParsePrefix(cidr); err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return err
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.AddrAdd(link, addr)
}

func setAddressV6(name, cidr string) error {
	return setAddress(name, cidr)
}

func setMTU(name string, mtu int) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkSetMTU(link, mtu)
}

func linkUp(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	return netlink.LinkSetUp(link)
}
