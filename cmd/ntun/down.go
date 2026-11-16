package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"ntun-ng/internal/route"
	"ntun-ng/internal/state"
)

// runDown implements `ntun down`: read the state file (if any), kill the
// running pid, remove routes, bring the TUN down, run the post-stop script.
// Idempotent — safe to run when nothing is up.
func runDown(args []string) int {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if !state.Exists() {
		fmt.Fprintln(os.Stderr, "ntun: no state file, nothing to tear down")
		return 0
	}
	st, err := state.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: read state: %v\n", err)
		return 1
	}

	// 1. Stop the running process (if alive). SIGTERM first, then SIGKILL.
	if st.PID > 0 {
		if alive(st.PID) {
			fmt.Fprintf(os.Stderr, "ntun: stopping pid %d\n", st.PID)
			_ = syscall.Kill(st.PID, syscall.SIGTERM)
			if !waitDead(st.PID, 5*time.Second) {
				fmt.Fprintf(os.Stderr, "ntun: pid %d did not exit, sending SIGKILL\n", st.PID)
				_ = syscall.Kill(st.PID, syscall.SIGKILL)
				_ = waitDead(st.PID, 5*time.Second)
			}
		}
	}

	// 2. Remove routes (best-effort).
	if st.Device != "" && len(st.Routes) > 0 {
		m := route.NewManager(st.Device)
		if err := m.Remove(); err != nil {
			fmt.Fprintf(os.Stderr, "ntun: route cleanup: %v\n", err)
		}
	}

	// 3. Bring the TUN device down + delete it. Delegated to `ip`/`ifconfig`
	// for a clean cross-platform teardown without re-opening the device.
	if st.Device != "" {
		_ = bringDownDevice(st.Device)
	}

	// 4. Post-stop script.
	if st.PostStopScript != "" {
		fmt.Fprintln(os.Stderr, "ntun: running post_stop_script")
		cmd := exec.Command("sh", "-c", st.PostStopScript)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	}

	// 5. Remove the state file.
	_ = state.Remove()
	fmt.Fprintln(os.Stderr, "ntun: torn down")
	return 0
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

// keep strconv imported (used for pid formatting if expanded later).
var _ = strconv.Itoa
