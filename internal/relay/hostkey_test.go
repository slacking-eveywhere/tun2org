package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ntun-ng/internal/config"
)

// newTestHostKey generates a host key pair for tests.
func newTestHostKey(t *testing.T) (ssh.PublicKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer.PublicKey(), signer
}

func setTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestTOFUFirstUseCaptures(t *testing.T) {
	setTempHome(t)
	key, _ := newTestHostKey(t)

	cb, err := tofuHostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if err := cb("srv:22", nil, key); err != nil {
		t.Fatalf("first use rejected: %v", err)
	}

	p, err := knownHostsPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("known_hosts not written: %v", err)
	}
	if !strings.Contains(string(data), "srv:22 ") {
		t.Fatalf("known_hosts line = %q, want srv:22 entry", data)
	}

	// Second use with the same key must pass.
	cb2, err := tofuHostKeyCallback()
	if err != nil {
		t.Fatalf("callback 2: %v", err)
	}
	if err := cb2("srv:22", nil, key); err != nil {
		t.Fatalf("match rejected: %v", err)
	}
}

func TestTOFUMismatchFails(t *testing.T) {
	setTempHome(t)
	key1, _ := newTestHostKey(t)
	key2, _ := newTestHostKey(t)

	if err := appendKnownHost("srv:22", key1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cb, err := tofuHostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	err = cb("srv:22", nil, key2)
	if err == nil || !strings.Contains(err.Error(), "MITM") {
		t.Fatalf("mismatch err = %v, want MITM error", err)
	}
}

func TestTOFUBadKnownHostsFails(t *testing.T) {
	dir := setTempHome(t)
	if err := os.MkdirAll(dir+"/.tun2org", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/.tun2org/known_hosts", []byte("garbage line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tofuHostKeyCallback(); err == nil {
		t.Fatal("expected error for malformed known_hosts, got nil")
	}
}

// startSSHServer runs a minimal in-process ssh server with the given host
// key and returns its address. Over a real TCP listener: net.Pipe would
// deadlock the ssh version exchange (both sides write their banner before
// reading, and pipe writes block until consumed).
func startSSHServer(t *testing.T, signer ssh.Signer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scfg := &ssh.ServerConfig{NoClientAuth: true}
				scfg.AddHostKey(signer)
				sconn, _, _, err := ssh.NewServerConn(c, scfg)
				if err != nil {
					return
				}
				_ = sconn.Close()
			}(c)
		}
	}()
	return ln.Addr().String()
}

// sshHandshakeTo dials addr and runs the ssh client handshake, keying
// known_hosts under hostname (the callback compares by hostname).
func sshHandshakeTo(t *testing.T, addr, hostname string) error {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	cb, err := tofuHostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            "u",
		Auth:            []ssh.AuthMethod{ssh.Password("x")},
		HostKeyCallback: cb,
		Timeout:         5 * time.Second,
	}
	_, _, _, err = ssh.NewClientConn(conn, hostname, cfg)
	return err
}

// TestTOFUHandshake runs the real ssh client against an in-process ssh
// server: first handshake captures the server key, a second one with the
// same key passes, and a different key under the same hostname fails.
func TestTOFUHandshake(t *testing.T) {
	setTempHome(t)
	_, signer1 := newTestHostKey(t)
	_, signer2 := newTestHostKey(t)

	addr1 := startSSHServer(t, signer1)
	addr2 := startSSHServer(t, signer2)

	// First use: capture.
	if err := sshHandshakeTo(t, addr1, "srv1"); err != nil {
		t.Fatalf("first handshake: %v", err)
	}
	// Same key again: pass.
	if err := sshHandshakeTo(t, addr1, "srv1"); err != nil {
		t.Fatalf("second handshake same key: %v", err)
	}
	// Different key under the same hostname: MITM failure.
	err := sshHandshakeTo(t, addr2, "srv1")
	if err == nil || !strings.Contains(err.Error(), "MITM") {
		t.Fatalf("different key err = %v, want MITM error", err)
	}
}

// TestExplicitHostKeyPins verifies the config host_key path still pins.
func TestExplicitHostKeyPins(t *testing.T) {
	key, _ := newTestHostKey(t)
	cfg := &config.Config{HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	d := &sshDialer{cfg: cfg}
	cb, err := d.hostKeyCallback()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	other, _ := newTestHostKey(t)
	if err := cb("srv:22", nil, other); err == nil {
		t.Fatal("pinned callback accepted a different key")
	}
	if err := cb("srv:22", nil, key); err != nil {
		t.Fatalf("pinned callback rejected the pinned key: %v", err)
	}
}
