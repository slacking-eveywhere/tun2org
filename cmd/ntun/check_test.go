package main

import (
	"os"

	"ntun-ng/internal/config"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCheckOK(t *testing.T) {
	p := writeCfg(t, `{
		"internal_server": {
			"username":"u","forward_server":"10.0.0.5","auth":"password",
			"routes":["10.120.0.0/16"],"tun_device":"ntun0","tun_network":"198.18.0.1/15"
		},
		"external_server": {
			"username":"u","forward_server":"172.16.0.9","forward_port":2222,"auth":"agent",
			"routes":["172.16.0.0/12"],"tun_device":"ntun1","tun_network":"198.20.0.1/15"
		}
	}`)
	if rc := runCheck([]string{"--config", p}); rc != 0 {
		t.Fatalf("runCheck rc = %d, want 0", rc)
	}
}

func TestRunCheckSingleProfileSelection(t *testing.T) {
	p := writeCfg(t, `{
		"internal_server": {"username":"u","forward_server":"10.0.0.5"},
		"external_server": {"username":"u","forward_server":"172.16.0.9","forward_port":2222,
			"tun_device":"ntun1","tun_network":"198.20.0.1/15","routes":["172.16.0.0/12"]}
	}`)
	if rc := runCheck([]string{"--config", p, "--profile", "external_server"}); rc != 0 {
		t.Fatalf("runCheck rc = %d, want 0", rc)
	}
	if rc := runCheck([]string{"--config", p, "--profile", "nope"}); rc != 2 {
		t.Fatalf("runCheck unknown profile rc = %d, want 2", rc)
	}
}

func TestRunCheckCollision(t *testing.T) {
	p := writeCfg(t, `{
		"a": {"username":"u","forward_server":"h","routes":["10.0.0.0/8"]},
		"b": {"username":"u","forward_server":"h","routes":["172.16.0.0/12"]}
	}`)
	if rc := runCheck([]string{"--config", p}); rc != 2 {
		t.Fatalf("runCheck rc = %d, want 2 (default tun collision)", rc)
	}
}

func TestRunCheckFlatConfigRejected(t *testing.T) {
	p := writeCfg(t, `{"username":"u","forward_server":"h"}`)
	if rc := runCheck([]string{"--config", p}); rc != 2 {
		t.Fatalf("runCheck rc = %d, want 2 (flat config)", rc)
	}
}

func TestRunCheckBadJSON(t *testing.T) {
	p := writeCfg(t, `{not json`)
	if rc := runCheck([]string{"--config", p}); rc != 2 {
		t.Fatalf("runCheck rc = %d, want 2", rc)
	}
}

func TestSelectProfilesDefaultAllOrder(t *testing.T) {
	p := writeCfg(t, `{
		"zulu": {"username":"u","forward_server":"a"},
		"alpha": {"username":"u","forward_server":"b"}
	}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := selectProfiles(profs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "zulu,alpha" {
		t.Fatalf("got %v", got)
	}
}

func TestSelectProfilesUnknown(t *testing.T) {
	p := writeCfg(t, `{"a": {"username":"u","forward_server":"h"}}`)
	profs, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectProfiles(profs, []string{"ghost"}); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
