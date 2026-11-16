package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ntun-ng/internal/state"
)

// testHome points HOME at a temp dir so tests never touch the real
// ~/.tun2org.
func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestWriteReadRemovePerProfile(t *testing.T) {
	testHome(t)

	if err := state.Write("internal", state.File{
		Version: 1, PID: 111, Device: "ntun0", Routes: []string{"10.0.0.0/8"},
		StartedAt: time.Unix(1000, 0),
	}); err != nil {
		t.Fatalf("Write internal: %v", err)
	}
	if err := state.Write("external", state.File{
		Version: 1, PID: 222, Device: "ntun1", Routes: []string{"172.16.0.0/12"},
	}); err != nil {
		t.Fatalf("Write external: %v", err)
	}

	s, err := state.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(s) != 2 {
		t.Fatalf("len = %d, want 2", len(s))
	}
	if s["internal"].PID != 111 || s["internal"].Device != "ntun0" {
		t.Errorf("internal = %+v", s["internal"])
	}
	if s["external"].PID != 222 {
		t.Errorf("external.PID = %d", s["external"].PID)
	}

	if err := state.Remove("internal"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	s, err = state.Read()
	if err != nil {
		t.Fatalf("Read 2: %v", err)
	}
	if len(s) != 1 {
		t.Fatalf("after removing one profile len = %d, want 1", len(s))
	}
	if _, ok := s["external"]; !ok {
		t.Fatal("external profile lost")
	}

	// Removing the last profile deletes the file.
	if err := state.Remove("external"); err != nil {
		t.Fatalf("Remove 2: %v", err)
	}
	p, _ := state.Path()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("state file should be gone after last Remove, stat err = %v", err)
	}
}

func TestProfilesList(t *testing.T) {
	home := testHome(t)
	_ = state.Write("zulu", state.File{Version: 1})
	_ = state.Write("alpha", state.File{Version: 1})

	names, err := state.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zulu" {
		t.Fatalf("Profiles = %v, want [alpha zulu]", names)
	}
	_ = home
}

func TestReadLegacyFlatState(t *testing.T) {
	home := testHome(t)
	dir := filepath.Join(home, ".tun2org")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"pid":42,"started_at":"2024-01-01T00:00:00Z","device":"tun0","routes":["10.0.0.0/8"]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := state.Read()
	if err != nil {
		t.Fatalf("Read legacy: %v", err)
	}
	if len(s) != 1 {
		t.Fatalf("len = %d, want 1", len(s))
	}
	f, ok := s[state.LegacyProfile]
	if !ok {
		t.Fatalf("expected key %q, got %v", state.LegacyProfile, s)
	}
	if f.PID != 42 || f.Device != "tun0" || len(f.Routes) != 1 {
		t.Fatalf("legacy file = %+v", f)
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	testHome(t)
	s, err := state.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("len = %d, want 0", len(s))
	}
}

func TestWriteEmptyProfileRejected(t *testing.T) {
	testHome(t)
	if err := state.Write("", state.File{Version: 1}); err == nil {
		t.Fatal("empty profile name accepted")
	}
}
