// Package supervise provides the runtime supervision pieces that keep a long-
// running ntun session healthy: default-interface change detection (to rebind
// the SSH socket away from a stale interface) and the top-level orchestrator
// that runs the tunnel until signalled.
//
// Phase 6: interface watcher (netlink route changes on Linux, periodic poll on
// macOS), plus a stop signal source. The state-file-based `ntun down` cleanup
// lives in package state.
package supervise

import (
	"context"
	"net"
	"sync"
	"time"
)

// IfaceWatcher reports the name of the current default-route interface and
// fires a callback when it changes. The default implementation polls (works on
// every platform); Linux overrides it with a netlink route subscription for
// lower latency (see iface_linux.go).
type IfaceWatcher interface {
	// Watch blocks until ctx is cancelled, invoking onChange each time the
	// detected default interface name changes (and once at startup).
	Watch(ctx context.Context, onChange func(iface string)) error
}

// pollWatcher detects the default interface by inspecting the routing table
// periodically. It is the fallback implementation (macOS, and Linux if the
// netlink subscription is unavailable).
type pollWatcher struct {
	interval time.Duration
}

// NewPollWatcher returns an IfaceWatcher that polls every interval.
func NewPollWatcher(interval time.Duration) IfaceWatcher {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &pollWatcher{interval: interval}
}

func (w *pollWatcher) Watch(ctx context.Context, onChange func(string)) error {
	cur := defaultInterface()
	onChange(cur)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			next := defaultInterface()
			if next != "" && next != cur {
				cur = next
				onChange(cur)
			}
		}
	}
}

// defaultInterface returns the name of the interface holding the default IPv4
// route. It prefers a netlink lookup (Linux) and falls back to the net
// package's route scan.
func defaultInterface() string {
	if name, ok := defaultInterfaceNetlink(); ok {
		return name
	}
	return defaultInterfaceNet()
}

// defaultInterfaceNet is the portable fallback: scan routes via the net
// package. It returns "" if none found.
func defaultInterfaceNet() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	// Prefer the first non-loopback, non-tun interface that is up and has a
	// default route. net.Interfaces() ordering is stable enough for a poll.
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		return ifc.Name
	}
	return ""
}

// RebindTracker remembers the last interface a dialer was bound to so callers
// can detect whether a rebind is actually needed. The first Set only records
// the baseline (no change is reported), so the watcher's initial report at
// startup doesn't spuriously rebind.
type RebindTracker struct {
	mu   sync.Mutex
	cur  string
	init bool
}

// Set records the current bound interface. Returns true if it changed
// (excluding the first call, which just establishes the baseline).
func (r *RebindTracker) Set(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.init {
		r.cur = name
		r.init = true
		return false
	}
	if name == r.cur {
		return false
	}
	r.cur = name
	return true
}
