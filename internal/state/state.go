// Package state records the runtime state of a running ntun instance to a
// file so that `ntun down` can tear down the TUN device + routes + post-stop
// script even after the process has crashed (or been killed -9).
//
// The state file is JSON, written atomically (temp + rename) at startup and
// updated as routes are added. On clean shutdown it is removed; `ntun down`
// reads it (or a stale one) and performs idempotent cleanup.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File holds the recorded runtime state.
type File struct {
	// Version of the state format.
	Version int `json:"version"`
	// PID of the running ntun process (0 if unknown).
	PID int `json:"pid"`
	// StartedAt is when the tunnel came up.
	StartedAt time.Time `json:"started_at"`
	// Device is the TUN interface name.
	Device string `json:"device"`
	// Routes are the CIDRs that were added to the kernel for this device.
	Routes []string `json:"routes"`
	// PostStopScript is the script to run on teardown (may be empty).
	PostStopScript string `json:"post_stop_script,omitempty"`
}

// Path returns the default state file location:
// ~/.tun2org/state.json (created lazily).
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tun2org", "state.json"), nil
}

// Write writes the state file atomically (temp file + rename). It also ensures
// the parent directory exists.
func Write(f File) (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("state: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", fmt.Errorf("state: marshal: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("state: write tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", fmt.Errorf("state: rename: %w", err)
	}
	return p, nil
}

// Read loads the state file. Returns a wrapped ErrNotExist if missing.
func Read() (File, error) {
	p, err := Path()
	if err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("state: parse %s: %w", p, err)
	}
	return f, nil
}

// Remove deletes the state file (called on clean shutdown).
func Remove() error {
	p, err := Path()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Exists reports whether a state file is present.
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
