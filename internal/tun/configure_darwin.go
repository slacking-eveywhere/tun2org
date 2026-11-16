//go:build darwin && arm64

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func setAddress(name, cidr string) error {
	// ifconfig <dev> inet <cidr>  (macOS accepts addr/prefix directly)
	out, err := exec.Command("ifconfig", name, "inet", cidr).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func setAddressV6(name, cidr string) error {
	out, err := exec.Command("ifconfig", name, "inet6", cidr).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func setMTU(name string, mtu int) error {
	out, err := exec.Command("ifconfig", name, "mtu", fmt.Sprintf("%d", mtu)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func linkUp(name string) error {
	out, err := exec.Command("ifconfig", name, "up").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
