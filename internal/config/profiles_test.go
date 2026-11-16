package config_test

import (
	"strings"
	"testing"

	"ntun-ng/internal/config"
)

// two profiles with disjoint settings — the valid baseline.
func twoProfiles(t *testing.T) *config.Profiles {
	t.Helper()
	p := writeTemp(t, "config.json", `{
		"internal_server": {
			"username":"u","forward_server":"a",
			"routes":["10.120.0.0/16"],
			"tun_device":"ntun0","tun_network":"198.18.0.1/15"
		},
		"external_server": {
			"username":"u","forward_server":"b",
			"routes":["172.16.0.0/12"],
			"tun_device":"ntun1","tun_network":"198.20.0.1/15"
		}
	}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	return profs
}

func TestValidateAllDisjointOK(t *testing.T) {
	if err := config.ValidateAll(twoProfiles(t)); err != nil {
		t.Fatalf("disjoint profiles rejected: %v", err)
	}
}

func TestValidateAllSingleProfileOK(t *testing.T) {
	p := writeTemp(t, "config.json", `{"only": {"username":"u","forward_server":"a"}}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if err := config.ValidateAll(profs); err != nil {
		t.Fatalf("single profile rejected: %v", err)
	}
}

func TestValidateAllDuplicateTunDevice(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"tun9"},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"198.20.0.1/15","tun_device":"tun9"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "tun_device") {
		t.Fatalf("expected tun_device collision, got %v", err)
	}
}

func TestValidateAllDefaultTunCollisions(t *testing.T) {
	// Both profiles default tun0 + 198.18.0.1/15 -> caught (device first).
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"]},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"]}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil {
		t.Fatal("default tun_device collision not caught")
	}
	if !strings.Contains(err.Error(), "tun_device") {
		t.Fatalf("want tun_device error, got %v", err)
	}
}

func TestValidateAllTunNetworkOverlap(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"10.1.0.1/16","tun_device":"ntun0"},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"10.1.9.1/24","tun_device":"ntun1"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "tun_network") {
		t.Fatalf("expected tun_network overlap, got %v", err)
	}
}

func TestValidateAllTunNetworkV6Overlap(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"ntun0","tun_network_v6":"fdfe:aa::1/48"},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"198.20.0.1/15","tun_device":"ntun1","tun_network_v6":"fdfe:aa::1/48"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "tun_network") {
		t.Fatalf("expected v6 tun_network overlap, got %v", err)
	}
}

func TestValidateAllRouteOverlap(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.1.0.0/16"],"tun_network":"198.18.0.1/15","tun_device":"ntun0"},
		"b": {"username":"u","forward_server":"h","routes":["10.1.9.0/24"],"tun_network":"198.20.0.1/15","tun_device":"ntun1"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("expected route overlap, got %v", err)
	}
}

func TestValidateAllRouteInsideOtherTunNetwork(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.120.0.0/16"],"tun_network":"198.18.0.1/15","tun_device":"ntun0"},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"10.120.9.1/24","tun_device":"ntun1"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "overlaps tun_network") {
		t.Fatalf("expected route-vs-tun_network overlap, got %v", err)
	}
}

func TestValidateAllV4RouteVsV6TunNoFalsePositive(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"ntun0"},
		"b": {"username":"u","forward_server":"h","routes":["fd00::/8"],"tun_network":"198.20.0.1/15","tun_device":"ntun1","tun_network_v6":"fdfe:aa::1/48"}
	}`)
	profs, _ := config.LoadFile(p)
	if err := config.ValidateAll(profs); err != nil {
		t.Fatalf("v4/v6 should not overlap: %v", err)
	}
}

func TestValidateAllSameForwardServerOK(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"same.example.com","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"ntun0"},
		"b": {"username":"u","forward_server":"same.example.com","routes":["172.16.0.0/12"],"tun_network":"198.20.0.1/15","tun_device":"ntun1"}
	}`)
	profs, _ := config.LoadFile(p)
	if err := config.ValidateAll(profs); err != nil {
		t.Fatalf("same forward_server should be allowed: %v", err)
	}
}

func TestValidateAllDNSHijackTwiceOK(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"ntun0","dns_hijack":true},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"198.20.0.1/15","tun_device":"ntun1","dns_hijack":true}
	}`)
	profs, _ := config.LoadFile(p)
	if err := config.ValidateAll(profs); err != nil {
		t.Fatalf("dns_hijack on two devices should be allowed: %v", err)
	}
}

func TestValidateAllDNSListenCollision(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"],"tun_network":"198.18.0.1/15","tun_device":"ntun0","dns_listen":"127.0.0.1:5353"},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"],"tun_network":"198.20.0.1/15","tun_device":"ntun1","dns_listen":"127.0.0.1:5353"}
	}`)
	profs, _ := config.LoadFile(p)
	err := config.ValidateAll(profs)
	if err == nil || !strings.Contains(err.Error(), "dns_listen") {
		t.Fatalf("expected dns_listen collision, got %v", err)
	}
}
