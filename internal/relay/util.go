package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentHash returns the first 8 hex chars of sha256(data), used as a stable
// cache version tag in the remote agent filename.
func agentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:8]
}

// agentSigners connects to the local ssh-agent ($SSH_AUTH_SOCK) and returns
// its available signers.
func agentSigners() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("dial agent socket: %w", err)
	}
	a := agent.NewClient(conn)
	signers, err := a.Signers()
	if err != nil {
		return nil, fmt.Errorf("list signers: %w", err)
	}
	return signers, nil
}

// bindToInterface binds conn's underlying fd to the named interface on Linux
// (SO_BINDTODEVICE) so the SSH socket never routes through the TUN we created.
// On non-Linux it is a no-op (handled by the _ / _linux split below).
func bindToInterface(conn net.Conn, iface string) error {
	return bindToInterfaceOS(conn, iface)
}

// shellQuote single-quotes a string for safe inclusion in a remote shell
// command. This is the classic single-quote-escape idiom.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// parentDir returns the directory portion of a remote path, using path (which
// matches POSIX shell semantics on the remote host).
func parentDir(p string) string {
	return path.Dir(p)
}

// bytesReader returns a new io.Reader over b (kept as a helper so ssh.go does
// not import bytes directly for a one-liner).
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
