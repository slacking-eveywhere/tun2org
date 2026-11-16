// Package route adds and removes the kernel IP routes that direct tunneled
// traffic into the TUN device. Linux uses netlink (no shelling out); macOS
// uses the `route` command. Both need privileges (CAP_NET_ADMIN / sudo).
package route

import (
	"fmt"
	"strings"
)

// Route is a single configured route to be added/removed.
type Route struct {
	// CIDR is the destination in CIDR form (IPv4 or IPv6), e.g. "10.120.1.10/32".
	CIDR string
	// Device is the interface the route points to (the TUN name).
	Device string
}

// Manager owns a set of added routes for idempotent teardown.
type Manager struct {
	dev    string
	routes []Route
}

// NewManager returns a Manager bound to the given device name.
func NewManager(device string) *Manager {
	return &Manager{dev: device}
}

// Added returns the routes currently installed.
func (m *Manager) Added() []Route { return m.routes }

// Add installs all routes. Routes already present for the device are skipped.
// Returns the routes it actually added (for later Remove).
func (m *Manager) Add(cidrs []string) ([]Route, error) {
	var added []Route
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		r := Route{CIDR: c, Device: m.dev}
		if err := addRoute(r); err != nil {
			// Best-effort: if the route already exists, keep going.
			if isExistsErr(err) {
				continue
			}
			return added, fmt.Errorf("route: add %s: %w", c, err)
		}
		added = append(added, r)
	}
	m.routes = append(m.routes, added...)
	return added, nil
}

// Remove removes all previously-added routes. Errors are collected but do not
// abort the loop (teardown must be best-effort).
func (m *Manager) Remove() error {
	var firstErr error
	for i := len(m.routes) - 1; i >= 0; i-- {
		r := m.routes[i]
		if err := delRoute(r); err != nil && !isNotExistsErr(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("route: del %s: %w", r.CIDR, err)
			}
		}
	}
	m.routes = nil
	return firstErr
}
