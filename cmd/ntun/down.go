package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"ntun-ng/internal/route"
	"ntun-ng/internal/state"
)

// runDown implements `ntun down [PROFILE]...`: read the state file, kill the
// running profiles' pids, remove routes, bring the TUN devices down, run the
// post-stop scripts. Without arguments every recorded profile is torn down
// (including any pre-multi-profile leftover). Idempotent — safe to run when
// nothing is up.
func runDown(args []string) int {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	want := fs.Args()

	set, err := state.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: read state: %v\n", err)
		return 1
	}
	if len(set) == 0 {
		fmt.Fprintln(os.Stderr, "ntun: no state file, nothing to tear down")
		return 0
	}

	targets := make([]string, 0, len(set))
	if len(want) == 0 {
		for name := range set {
			targets = append(targets, name)
		}
	} else {
		for _, w := range want {
			if _, ok := set[w]; !ok {
				known := make([]string, 0, len(set))
				for n := range set {
					known = append(known, n)
				}
				fmt.Fprintf(os.Stderr, "ntun: no state for profile %q (recorded: %v)\n", w, known)
				return 1
			}
			targets = append(targets, w)
		}
	}

	for _, name := range targets {
		teardownProfile(name, set[name])
		if err := state.Remove(name); err != nil {
			fmt.Fprintf(os.Stderr, "ntun: state remove %s: %v\n", name, err)
		}
	}
	fmt.Fprintln(os.Stderr, "ntun: torn down")
	return 0
}

// teardownProfile stops the process, removes routes, deletes the TUN device
// and runs the post-stop script of one profile (best-effort).
func teardownProfile(name string, st state.File) {
	// 1. Stop the running process (if alive). SIGTERM first, then SIGKILL.
	if st.PID > 0 && alive(st.PID) {
		fmt.Fprintf(os.Stderr, "ntun: [%s] stopping pid %d\n", name, st.PID)
		_ = syscall.Kill(st.PID, syscall.SIGTERM)
		if !waitDead(st.PID, 5*time.Second) {
			fmt.Fprintf(os.Stderr, "ntun: [%s] pid %d did not exit, sending SIGKILL\n", name, st.PID)
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
			_ = waitDead(st.PID, 5*time.Second)
		}
	}

	// 2. Remove routes (best-effort).
	if st.Device != "" && len(st.Routes) > 0 {
		m := route.NewManager(st.Device)
		if err := m.Remove(); err != nil {
			fmt.Fprintf(os.Stderr, "ntun: [%s] route cleanup: %v\n", name, err)
		}
	}

	// 3. Bring the TUN device down + delete it. Delegated to `ip`/`ifconfig`
	// for a clean cross-platform teardown without re-opening the device.
	if st.Device != "" {
		_ = bringDownDevice(st.Device)
	}

	// 4. Post-stop script.
	if st.PostStopScript != "" {
		fmt.Fprintf(os.Stderr, "ntun: [%s] running post_stop_script\n", name)
		cmd := exec.Command("sh", "-c", st.PostStopScript)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	}
}

// alive reports whether a process with the given pid exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// signal 0: existence check.
	return proc.Signal(syscall.Signal(0)) == nil
}

// waitDead polls until the pid is gone or timeout elapses.
func waitDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !alive(pid)
}

// bringDownDevice deletes the TUN interface via ip(8) (Linux) or ifconfig
// (macOS). Errors are ignored — the device may already be gone.
func bringDownDevice(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Try Linux `ip link delete`.
	cmd := exec.CommandContext(ctx, "ip", "link", "delete", name)
	if err := cmd.Run(); err == nil {
		return nil
	}
	// Fall back to macOS `ifconfig destroy`.
	cmd2 := exec.CommandContext(ctx, "ifconfig", name, "destroy")
	return cmd2.Run()
}
