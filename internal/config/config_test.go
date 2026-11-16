package config_test

import (
	"encoding/json"
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

func TestLoadMinimal(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username": "alice",
		"forward_server": "bla.bli.blou"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Username != "alice" {
		t.Errorf("Username = %q", c.Username)
	}
	if c.Mode != config.ModeBastion {
		t.Errorf("Mode default = %q, want bastion", c.Mode)
	}
	if c.Auth != config.AuthPassword {
		t.Errorf("Auth default = %q, want password", c.Auth)
	}
	if c.Bastion != "relais-ssh.bli.blou" {
		t.Errorf("Bastion default = %q", c.Bastion)
	}
	if c.PingEvery != 30*time.Second {
		t.Errorf("PingEvery = %v", c.PingEvery)
	}
	if c.DNSHijack == nil || !*c.DNSHijack {
		t.Errorf("DNSHijack default should be true")
	}
}

func TestLoadDirect(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username": "alice",
		"forward_server": "bla.bli.blou",
		"mode": "direct"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SSHUser() != "alice" {
		t.Errorf("direct SSHUser = %q, want alice", c.SSHUser())
	}
	want := "bla.bli.blou:22"
	if c.SSHHostPort() != want {
		t.Errorf("direct SSHHostPort = %q, want %q", c.SSHHostPort(), want)
	}
}

func TestLoadBastionUserDance(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username": "alice",
		"forward_server": "bla.bli.blou",
		"mode": "bastion",
		"bastion": "relais-ssh.bli.blou"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.SSHUser(); got != "alice@bla.bli.blou" {
		t.Errorf("bastion SSHUser = %q", got)
	}
	if got := c.SSHHostPort(); got != "relais-ssh.bli.blou:22" {
		t.Errorf("bastion SSHHostPort = %q", got)
	}
}

func TestLoadMissingUsername(t *testing.T) {
	p := writeTemp(t, "config.json", `{"forward_server":"h"}`)
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("expected username error, got %v", err)
	}
}

func TestLoadInvalidMode(t *testing.T) {
	p := writeTemp(t, "config.json", `{"username":"u","forward_server":"h","mode":"teleport"}`)
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("expected mode error, got %v", err)
	}
}

func TestLoadBadJSON(t *testing.T) {
	p := writeTemp(t, "config.json", `{not json`)
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadDuration(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username":"u","forward_server":"h",
		"ping_every":"15s"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PingEvery != 15*time.Second {
		t.Errorf("PingEvery = %v", c.PingEvery)
	}
}

func TestLoadDurationMarshalRoundTrip(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username":"u","forward_server":"h",
		"ping_every":"15s","reconnect_min":"500ms","reconnect_max":"5s"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PingEvery != 15*time.Second ||
		c.ReconnectMin != 500*time.Millisecond || c.ReconnectMax != 5*time.Second {
		t.Fatalf("durations = %v %v %v", c.PingEvery, c.ReconnectMin, c.ReconnectMax)
	}
	// Marshal back should emit string durations, not raw nanoseconds.
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{"\"15s\"", "\"500ms\"", "\"5s\""} {
		if !strings.Contains(s, want) {
			t.Errorf("marshal output missing %s: %s", want, s)
		}
	}
	// Round-trip back through Load.
	c2, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load 2: %v", err)
	}
	if c2.PingEvery != c.PingEvery || c2.ReconnectMin != c.ReconnectMin || c2.ReconnectMax != c.ReconnectMax {
		t.Fatalf("round-trip mismatch")
	}
}

func TestLoadProxyCommand(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username":"root","forward_server":"195.154.160.42","forward_port":60001,
		"mode":"direct","proxy_command":"ssh -W %h:%p srr1-lb-prd-si01"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProxyCommand != "ssh -W %h:%p srr1-lb-prd-si01" {
		t.Fatalf("ProxyCommand = %q", c.ProxyCommand)
	}
	if c.SSHHostPort() != "195.154.160.42:60001" {
		t.Fatalf("SSHHostPort = %q", c.SSHHostPort())
	}
	if c.SSHUser() != "root" {
		t.Fatalf("SSHUser = %q", c.SSHUser())
	}
}

func TestLoadIPv6Routes(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"username":"u","forward_server":"h",
		"routes":["10.120.1.10/32","fd00:dead:beef::/48","::1/128"],
		"tun_network_v6":"fdfe::1/64",
		"dns_hijack":true,
		"dns_remote":"10.120.1.10:53"
	}`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
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
	if c.DNSHijack == nil || !*c.DNSHijack {
		t.Errorf("DNSHijack = %v, want true", c.DNSHijack)
	}
}
