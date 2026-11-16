package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// Host key policy: trust on first use (OpenSSH known_hosts semantics).
//
// The explicit `host_key` config still pins a single key (see ssh.go). With
// no host_key set, the first connection to a host:port captures the server's
// host key into ~/.tun2org/known_hosts and every later connection must match
// it. A mismatch is a hard error (possible MITM), never a silent downgrade.

// knownHostsPath returns ~/.tun2org/known_hosts.
func knownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tun2org", "known_hosts"), nil
}

// loadKnownHosts parses the known_hosts file into host -> keys. A missing
// file yields an empty map; a malformed one is an error (fail closed).
func loadKnownHosts() (map[string][]ssh.PublicKey, error) {
	p, err := knownHostsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]ssh.PublicKey{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries := make(map[string][]ssh.PublicKey)
	for len(data) > 0 {
		marker, hosts, key, _, rest, err := ssh.ParseKnownHosts(data)
		if err != nil {
			return nil, fmt.Errorf("relay: parse %s: %w", p, err)
		}
		if marker == "" { // skip @revoked/@cert-authority entries
			for _, h := range hosts {
				entries[h] = append(entries[h], key)
			}
		}
		data = rest
	}
	return entries, nil
}

// appendKnownHost records host + key as one known_hosts line.
func appendKnownHost(host string, key ssh.PublicKey) error {
	p, err := knownHostsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(host + " " + string(ssh.MarshalAuthorizedKey(key))); err != nil {
		return err
	}
	return nil
}

// tofuHostKeyCallback returns an ssh.HostKeyCallback implementing
// trust-on-first-use against ~/.tun2org/known_hosts.
func tofuHostKeyCallback() (ssh.HostKeyCallback, error) {
	entries, err := loadKnownHosts()
	if err != nil {
		return nil, err
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		for _, known := range entries[hostname] {
			if bytes.Equal(known.Marshal(), key.Marshal()) {
				return nil
			}
		}
		if len(entries[hostname]) > 0 {
			return fmt.Errorf("relay: host key for %s changed (known_hosts mismatch — possible MITM; remove the entry only if you know why)", hostname)
		}
		if err := appendKnownHost(hostname, key); err != nil {
			return fmt.Errorf("relay: record host key for %s: %w", hostname, err)
		}
		return nil
	}, nil
}

// hostKeyCallback returns the callback for the configured host key policy:
// an explicit cfg.HostKey pins that key; otherwise TOFU.
func (d *sshDialer) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if d.cfg.HostKey == "" {
		return tofuHostKeyCallback()
	}
	// HostKey is an authorized_keys-style line ("ssh-ed25519 AAAA...").
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(d.cfg.HostKey))
	if err != nil {
		return nil, fmt.Errorf("relay: parse host_key: %w", err)
	}
	return ssh.FixedHostKey(pub), nil
}
