//go:build darwin && arm64

package route

import (
	"fmt"
	"os/exec"
	"strings"
)

func addRoute(r Route) error {
	// route -n add -net <cidr> -interface <dev>
	out, err := exec.Command("route", "-n", "add", "-net", r.CIDR, "-interface", r.Device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func delRoute(r Route) error {
	out, err := exec.Command("route", "-n", "delete", "-net", r.CIDR, "-interface", r.Device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func isExistsErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already in table")
}

func isNotExistsErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not in table")
}
