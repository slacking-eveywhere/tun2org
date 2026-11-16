package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ntun-ng/internal/state"
)

// TestWriteReadRoundTrip writes a state file and reads it back. We override the
// home dir via HOME so it lands in temp.
func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// On some platforms UserHomeDir reads $HOME; ensure it does.
	if _, err := os.UserHomeDir(); err != nil {
		t.Skipf("UserHomeDir: %v", err)
	}

	in := state.File{
		Version:        1,
		PID:            12345,
		StartedAt:      time.Now().Truncate(time.Second),
		Device:         "tun0",
		Routes:         []string{"10.120.1.10/32", "fd00::/48"},
		PostStopScript: "docker stop reverse-tunnel",
		ConfigPath:     "/home/u/.tun2org/config.json",
	}
	p, err := state.Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(p) != filepath.Join(dir, ".tun2org") {
		t.Fatalf("path %s not under expected dir", p)
	}

	out, err := state.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.PID != in.PID || out.Device != in.Device || len(out.Routes) != len(in.Routes) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", out, in)
	}
	if out.Routes[1] != "fd00::/48" {
		t.Errorf("route[1] = %q", out.Routes[1])
	}

	if !state.Exists() {
		t.Fatal("Exists false after Write")
	}
	if err := state.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if state.Exists() {
		t.Fatal("Exists true after Remove")
	}
}

// TestReadMissing returns a not-exist error (no crash).
func TestReadMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := state.Read()
	if err == nil {
		t.Fatal("expected error reading missing state")
	}
}
