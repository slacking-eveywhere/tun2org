package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ntun-ng/internal/config"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// wrap puts a single profile body under one profile name.
func wrap(body string) string {
	return "{\"p\": " + body + "}"
}

func TestLoadMinimal(t *testing.T) {
	p := writeTemp(t, "config.json", wrap(`{
		"username": "alice",
		"forward_server": "bla.bli.blou"
	}`))
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if profs.Len() != 1 {
		t.Fatalf("Len = %d, want 1", profs.Len())
	}
	c, ok := profs.Get("p")
	if !ok {
		t.Fatal("profile p missing")
	}
	if c.Profile != "p" {
		t.Errorf("Profile = %q, want p", c.Profile)
	}
	if c.Username != "alice" {
		t.Errorf("Username = %q", c.Username)
	}
	if c.Auth != config.AuthPassword {
		t.Errorf("Auth default = %q, want password", c.Auth)
	}
	if c.PingEvery != 30*time.Second {
		t.Errorf("PingEvery = %v", c.PingEvery)
	}
	if c.DNSHijack == nil || !*c.DNSHijack {
		t.Errorf("DNSHijack default should be true")
	}
	if c.TunDevice != "tun0" {
		t.Errorf("TunDevice default = %q, want tun0", c.TunDevice)
	}
}

func TestLoadProfilesOrderPreserved(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"zulu":    {"username":"u","forward_server":"a"},
		"alpha":   {"username":"u","forward_server":"b"},
		"mike":    {"username":"u","forward_server":"c"}
	}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got := strings.Join(profs.Names(), ",")
	if got != "zulu,alpha,mike" {
		t.Errorf("Names = %q, want file order zulu,alpha,mike", got)
	}
}

func TestLoadFlatConfigRejected(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username": "alice",
		"forward_server": "bla.bli.blou"
	}`)
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("expected top-level-object error, got %v", err)
	}
}

func TestLoadEmptyObjectRejected(t *testing.T) {
	p := writeTemp(t, "config.json", `{}`)
	if _, err := config.LoadFile(p); err == nil || !strings.Contains(err.Error(), "no profiles") {
		t.Fatalf("expected no-profiles error, got %v", err)
	}
}

func TestLoadDuplicateProfileRejected(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"p": {"username":"u","forward_server":"a"},
		"p": {"username":"u","forward_server":"b"}
	}`)
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestLoadBadProfileNameRejected(t *testing.T) {
	for _, name := range []string{"has space", "", "with/slash", "with.dot"} {
		p := writeTemp(t, "config.json", `{"`+name+`": {"username":"u","forward_server":"a"}}`)
		if _, err := config.LoadFile(p); err == nil {
			t.Errorf("profile name %q accepted", name)
		}
	}
}

func TestLoadBadProfileValueRejected(t *testing.T) {
	p := writeTemp(t, "config.json", `{"p": 42}`)
	if _, err := config.LoadFile(p); err == nil || !strings.Contains(err.Error(), `profile "p"`) {
		t.Fatalf("expected profile decode error, got %v", err)
	}
}

func TestLoadMissingUsernameMentionsProfile(t *testing.T) {
	p := writeTemp(t, "config.json", `{"p": {"forward_server":"h"}}`)
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("expected username error, got %v", err)
	}
}

func TestLoadBadJSON(t *testing.T) {
	p := writeTemp(t, "config.json", `{not json`)
	if _, err := config.LoadFile(p); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSSHHostPortDefaultPort(t *testing.T) {
	p := writeTemp(t, "config.json", wrap(`{
		"username": "alice",
		"forward_server": "bla.bli.blou"
	}`))
	profs, _ := config.LoadFile(p)
	c, _ := profs.Get("p")
	if got := c.SSHHostPort(); got != "bla.bli.blou:22" {
		t.Errorf("SSHHostPort = %q, want bla.bli.blou:22", got)
	}
}

func TestLoadProxyCommand(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"pc": {
			"username":"root","forward_server":"195.154.160.42","forward_port":60001,
			"proxy_command":"ssh -W %h:%p jumper.example.com"
		}
	}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	c, _ := profs.Get("pc")
	if c.ProxyCommand != "ssh -W %h:%p jumper.example.com" {
		t.Fatalf("ProxyCommand = %q", c.ProxyCommand)
	}
	if c.SSHHostPort() != "195.154.160.42:60001" {
		t.Fatalf("SSHHostPort = %q", c.SSHHostPort())
	}
	if c.Username != "root" {
		t.Fatalf("Username = %q", c.Username)
	}
}

func TestLoadDuration(t *testing.T) {
	p := writeTemp(t, "config.json", wrap(`{
		"username":"u","forward_server":"h",
		"ping_every":"15s","reconnect_min":"2s","reconnect_max":"1m"
	}`))
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	c, _ := profs.Get("p")
	if c.PingEvery != 15*time.Second || c.ReconnectMin != 2*time.Second || c.ReconnectMax != time.Minute {
		t.Fatalf("durations = %v %v %v", c.PingEvery, c.ReconnectMin, c.ReconnectMax)
	}
}

func TestLoadIPv6Routes(t *testing.T) {
	p := writeTemp(t, "config.json", wrap(`{
		"username":"u","forward_server":"h",
		"routes":["10.120.1.10/32","fd00:dead:beef::/48","::1/128"],
		"tun_network_v6":"fdfe::1/64",
		"dns_hijack":true,
		"dns_remote":"10.120.1.10:53"
	}`))
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	c, _ := profs.Get("p")
	if len(c.Routes) != 3 {
		t.Fatalf("routes = %d, want 3", len(c.Routes))
	}
	if c.Routes[1] != "fd00:dead:beef::/48" {
		t.Errorf("route[1] = %q", c.Routes[1])
	}
	if c.TunNetworkV6 != "fdfe::1/64" {
		t.Errorf("TunNetworkV6 = %q", c.TunNetworkV6)
	}
	if c.DNSRemote != "10.120.1.10:53" {
		t.Errorf("DNSRemote = %q", c.DNSRemote)
	}
}
