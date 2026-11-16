package tunnel

import (
	"testing"

	"ntun-ng/internal/mux"
)

func TestDNSTargetHijack(t *testing.T) {
	tun := &Tunnel{
		opts: Options{DNSHijack: true, DNSRemote: "10.120.1.10:53"},
	}
	dst := mux.FromHostPort("8.8.8.8", 53)
	got := tun.dnsTarget(dst)
	if got.Type != mux.AddrIPv4 {
		t.Fatalf("type = %d, want ipv4", got.Type)
	}
	if got.String() != "10.120.1.10:53" {
		t.Fatalf("hijack target = %q, want 10.120.1.10:53", got.String())
	}
}

func TestDNSTargetNoHijack(t *testing.T) {
	tun := &Tunnel{opts: Options{DNSHijack: false, DNSRemote: "10.120.1.10:53"}}
	dst := mux.FromHostPort("8.8.8.8", 53)
	got := tun.dnsTarget(dst)
	// No hijack: original destination preserved.
	if got.String() != "8.8.8.8:53" {
		t.Fatalf("target = %q, want 8.8.8.8:53", got.String())
	}
}

func TestDNSTargetNonDNSPort(t *testing.T) {
	tun := &Tunnel{
		opts: Options{DNSHijack: true, DNSRemote: "10.120.1.10:53"},
	}
	// Port 1234 is not DNS: not redirected even with hijack on.
	dst := mux.FromHostPort("8.8.8.8", 1234)
	got := tun.dnsTarget(dst)
	if got.String() != "8.8.8.8:1234" {
		t.Fatalf("target = %q, want 8.8.8.8:1234", got.String())
	}
}

func TestDNSTargetNoRemoteRelays(t *testing.T) {
	// Hijack on but no remote resolver: relay to original (assumes reachable
	// through the tunnel, the forward server's network).
	tun := &Tunnel{opts: Options{DNSHijack: true, DNSRemote: ""}}
	dst := mux.FromHostPort("8.8.8.8", 53)
	got := tun.dnsTarget(dst)
	if got.String() != "8.8.8.8:53" {
		t.Fatalf("target = %q, want 8.8.8.8:53", got.String())
	}
}

func TestDNSTargetIPv6(t *testing.T) {
	tun := &Tunnel{
		opts: Options{DNSHijack: true, DNSRemote: "fd00::53"},
	}
	dst := mux.FromHostPort("2001:4860:4860::8888", 53)
	got := tun.dnsTarget(dst)
	if got.Type != mux.AddrIPv6 {
		t.Fatalf("type = %d, want ipv6", got.Type)
	}
	if got.String() != "[fd00::53]:53" {
		t.Fatalf("hijack target = %q, want [fd00::53]:53", got.String())
	}
}

func TestSplitHostPortDefault(t *testing.T) {
	h, p := splitHostPortDefault("10.120.1.10:53", 5353)
	if h != "10.120.1.10" || p != 53 {
		t.Fatalf("got %q %d", h, p)
	}
	h, p = splitHostPortDefault("noport", 5353)
	if h != "noport" || p != 5353 {
		t.Fatalf("default got %q %d", h, p)
	}
}
