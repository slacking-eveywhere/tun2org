package relay

import (
	"context"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestExpandProxyCommand(t *testing.T) {
	cases := []struct {
		in, host, port, want string
	}{
		{"ssh -W %h:%p jumper", "195.154.160.42", "60001", "ssh -W 195.154.160.42:60001 jumper"},
		{"nc %h %p", "10.0.0.1", "22", "nc 10.0.0.1 22"},
		{"ssh -p %p %h", "2001:db8::1", "2222", "ssh -p 2222 2001:db8::1"},
		{"percent %%h %%p", "h", "p", "percent %h %p"},
		{"no tokens", "h", "p", "no tokens"},
	}
	for _, c := range cases {
		if got := expandProxyCommand(c.in, c.host, c.port); got != c.want {
			t.Errorf("expandProxyCommand(%q, %q, %q) = %q, want %q", c.in, c.host, c.port, got, c.want)
		}
	}
}

// TestProxyConnEcho verifies proxyConn wires a process's stdin/stdout as a
// net.Conn: bytes written come back via `cat`, and Close reaps the process.
func TestProxyConnEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := dialProxyCommand(ctx, "cat", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("dialProxyCommand: %v", err)
	}
	defer conn.Close()

	payload := "echo through the pipe"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("echo = %q, want %q", buf, payload)
	}

	// Close must terminate the child process, so a subsequent read errors.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected error reading from closed proxy conn")
	}
}

// TestDialProxyCommandContextCancellation verifies that cancelling the context
// kills the proxy process, which unblocks any in-flight read.
func TestDialProxyCommandContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := dialProxyCommand(ctx, "cat", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("dialProxyCommand: %v", err)
	}

	cancel() // context cancelled → proxy process killed
	_ = conn.Close()

	buf := make([]byte, 16)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected read to fail after context cancellation / close")
	}
}

// TestProxyConnCloseKillsProcessGroup verifies that Close reaps the whole
// proxy process group, so a child spawned by the proxy (e.g. ssh) can't be
// stranded to keep the transport open after shutdown.
func TestProxyConnCloseKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := dialProxyCommand(ctx, "sleep 100", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("dialProxyCommand: %v", err)
	}
	pc := conn.(*proxyConn)
	pid := pc.cmd.Process.Pid

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the process group must be gone (ESRCH).
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Fatalf("process group %d still alive after Close", pid)
	}
	_ = ctx
}

// TestProxyConnSatisfiesNetConn asserts proxyConn implements net.Conn.
var _ net.Conn = (*proxyConn)(nil)
