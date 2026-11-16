// Package state records the runtime state of running ntun profiles to a
// single file so that `ntun down` can tear down TUN devices + routes +
// post-stop scripts even after the process has crashed (or been killed -9).
//
// The state file is one JSON object keyed by profile name:
//
//	{ "internal_server": { "version": 1, "pid": ..., ... }, ... }
//
// It is written atomically (temp + rename) and guarded by a process-wide
// mutex, since all profiles of one ntun process share it. Pre-multi-profile
// state files (flat, with top-level pid/routes) are still readable so an
// old binary's leftovers can be torn down; they surface as profile
// "legacy".
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// File holds the recorded runtime state of one profile.
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

// Set maps profile name to its runtime state. Zero value is usable.
type Set map[string]File

// LegacyProfile is the profile name a pre-multi-profile state file is
// surfaced as.
const LegacyProfile = "legacy"

var mu sync.Mutex

// Path returns the default state file location:
// ~/.tun2org/state.json (created lazily).
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tun2org", "state.json"), nil
}

// Read loads the state file. Missing file = empty Set, nil error.
func Read() (Set, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Set{}, nil
	}
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", p, err)
	}
	// Pre-multi-profile flat file: {"version":1,"pid":123,...}.
	var pid int
	if _, hasRoutes := probe["routes"]; hasRoutes && probe["pid"] != nil {
		if json.Unmarshal(probe["pid"], &pid) == nil {
			var f File
			if err := json.Unmarshal(data, &f); err != nil {
				return nil, fmt.Errorf("state: parse %s: %w", p, err)
			}
			return Set{LegacyProfile: f}, nil
		}
	}
	var s Set
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", p, err)
	}
	if s == nil {
		s = Set{}
	}
	return s, nil
}

// Write merges f under profile into the state file (atomic temp + rename).
func Write(profile string, f File) error {
	if profile == "" {
		return fmt.Errorf("state: empty profile name")
	}
	mu.Lock()
	defer mu.Unlock()
	s, err := Read()
	if err != nil {
		return err
	}
	s[profile] = f
	return writeSet(s)
}

// Remove deletes profile from the state file. The file itself is deleted
// once the last profile is gone. Idempotent.
func Remove(profile string) error {
	mu.Lock()
	defer mu.Unlock()
	s, err := Read()
	if err != nil {
		return err
	}
	delete(s, profile)
	if len(s) == 0 {
		p, err := Path()
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeSet(s)
}

// Profiles lists the profiles with recorded state (sorted).
func Profiles() ([]string, error) {
	s, err := Read()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func writeSet(s Set) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("state: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("state: write tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}
