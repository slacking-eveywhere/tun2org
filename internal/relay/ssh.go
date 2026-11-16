package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"ntun-ng/internal/config"

	"golang.org/x/crypto/ssh"
)

// sshDialer is the production Dialer: SSH dials the configured target,
// uploads (and caches) the ntun-agent binary, then execs it over a session
// whose stdin/stdout become the mux transport.
type sshDialer struct {
	cfg *config.Config

	// agentBin is the bytes of the local ntun-agent; read once and reused on
	// reconnect so we don't re-read the file each attempt.
	agentBin []byte
	agentSum string // hex sha256[:8], used in the remote cache filename
}

// NewDialer builds a Dialer from config. It does not connect yet.
func NewDialer(cfg *config.Config) (Dialer, error) {
	data, err := os.ReadFile(cfg.AgentPath)
	if err != nil {
		return nil, fmt.Errorf("relay: read agent %s: %w", cfg.AgentPath, err)
	}
	return &sshDialer{
		cfg:      cfg,
		agentBin: data,
		agentSum: agentHash(data),
	}, nil
}

// DialSession connects, ensures the agent is uploaded, and execs it.
func (d *sshDialer) DialSession(ctx context.Context) (io.ReadWriteCloser, error) {
	client, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}

	home, err := d.remoteHome(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("relay: remote home: %w", err)
	}
	remotePath := d.remoteAgentPath(home)
	if err := d.ensureAgent(client, remotePath); err != nil {
		client.Close()
		return nil, fmt.Errorf("relay: upload agent: %w", err)
	}

	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("relay: new session: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, fmt.Errorf("relay: stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		stdin.Close()
		sess.Close()
		client.Close()
		return nil, fmt.Errorf("relay: stdout pipe: %w", err)
	}
	sess.Stderr = os.Stderr // surface agent logs

	if err := sess.Start(remotePath); err != nil {
		stdin.Close()
		sess.Close()
		client.Close()
		return nil, fmt.Errorf("relay: start agent: %w", err)
	}

	return &sessionRWC{
		client: client,
		sess:   sess,
		stdin:  stdin,
		stdout: stdout,
	}, nil
}

// connect dials the SSH server with the configured auth + mode.
func (d *sshDialer) connect(ctx context.Context) (*ssh.Client, error) {
	authMethods, err := d.authMethods()
	if err != nil {
		return nil, err
	}
	hkcb, err := d.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	sshCfg := &ssh.ClientConfig{
		User:            d.cfg.SSHUser(),
		Auth:            authMethods,
		HostKeyCallback: hkcb,
		Timeout:         20 * time.Second,
	}
	addr := d.cfg.SSHHostPort()

	conn, err := d.dialSSHTransport(ctx, addr)
	if err != nil {
		return nil, err
	}
	// Optionally bind to a physical interface to avoid routing the SSH
	// socket back through the TUN (the very loop we are creating). Only
	// possible for a real TCP socket; a proxy transport has no socket of
	// its own to bind.
	if d.cfg.Interface != "" {
		if tc, ok := conn.(*net.TCPConn); ok {
			if err := bindToInterfaceOS(tc, d.cfg.Interface); err != nil {
				conn.Close()
				return nil, fmt.Errorf("relay: bind interface %s: %w", d.cfg.Interface, err)
			}
		} else if d.cfg.ProxyCommand == "" {
			conn.Close()
			return nil, fmt.Errorf("relay: cannot bind interface on this transport")
		}
	}
	return sshHandshake(ctx, conn, addr, sshCfg)
}

// sshHandshake performs the SSH handshake over conn, bounded by sshDialTimeout.
// Closing conn on ctx cancellation or timeout unblocks the pending handshake,
// so a wedged peer can't stall shutdown (and for a proxy transport, closing
// conn kills the proxy child).
func sshHandshake(ctx context.Context, conn net.Conn, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	type result struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{ssh.NewClient(c, chans, reqs), nil}
	}()
	timer := time.NewTimer(sshDialTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.client, r.err
	case <-ctx.Done():
		_ = conn.Close()
		<-ch // wait for the handshake goroutine to observe the close
		return nil, ctx.Err()
	case <-timer.C:
		_ = conn.Close()
		<-ch
		return nil, fmt.Errorf("relay: ssh handshake %s: timeout after %s", addr, sshDialTimeout)
	}
}

// dialSSHTransport returns a net.Conn to the SSH server, either by a direct
// TCP dial or — when cfg.ProxyCommand is set — by running the command and
// bridging its stdin/stdout (OpenSSH ProxyCommand semantics, e.g. `ssh -W
// %h:%p jumper`).
func (d *sshDialer) dialSSHTransport(ctx context.Context, addr string) (net.Conn, error) {
	if d.cfg.ProxyCommand != "" {
		conn, err := dialProxyCommand(ctx, d.cfg.ProxyCommand, addr)
		if err != nil {
			return nil, fmt.Errorf("relay: proxy %s: %w", addr, err)
		}
		return conn, nil
	}
	dialer := &net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("relay: dial %s: %w", addr, err)
	}
	return conn, nil
}

// sshDialTimeout bounds direct TCP dials and parts of the proxy setup.
const sshDialTimeout = 20 * time.Second

// dialProxyCommand starts cmdStr (via `sh -c`) and wires its stdin/stdout as a
// net.Conn. %h and %p in cmdStr are replaced with addr's host and port. The
// process is killed if ctx is cancelled.
func dialProxyCommand(ctx context.Context, cmdStr, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split %q: %w", addr, err)
	}
	expanded := expandProxyCommand(cmdStr, host, port)
	cmd := exec.Command("sh", "-c", expanded)
	// Run the proxy in its own process group so shutdown can kill the whole
	// tree (sh + any ssh/nc it forks) instead of stranding a child that keeps
	// the transport open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("run %q: %w", expanded, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("run %q: %w", expanded, err)
	}
	cmd.Stderr = os.Stderr // surface the proxy's own diagnostics (e.g. ssh -v)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", expanded, err)
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-stop:
		}
	}()
	return &proxyConn{cmd: cmd, stdin: stdin, stdout: stdout, stop: stop}, nil
}

// killProcessGroup sends SIGKILL to cmd's process group (cmd + descendants).
// The proxy runs with Setpgid, so a negative pid targets the whole group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// expandProxyCommand substitutes OpenSSH ProxyCommand tokens: %h (host), %p
// (port), %% (literal percent).
func expandProxyCommand(cmdStr, host, port string) string {
	repl := strings.NewReplacer("%h", host, "%p", port, "%%", "%")
	return repl.Replace(cmdStr)
}

// proxyConn adapts an exec.Cmd's stdin/stdout into a net.Conn so the SSH
// handshake can run over it, exactly like OpenSSH ProxyCommand (ssh -W / nc).
// Deadlines and address calls are no-ops.
type proxyConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stop   chan struct{}
	once   sync.Once
}

func (c *proxyConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *proxyConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *proxyConn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.stop)
		err = c.stdin.Close()
		// Wait a moment for the proxy to exit on EOF/close, then kill the whole
		// process group so no child (e.g. a forked ssh) is stranded.
		done := make(chan struct{})
		go func() { defer close(done); _ = c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(time.Second):
			killProcessGroup(c.cmd)
			<-done
		}
	})
	return err
}

func (c *proxyConn) LocalAddr() net.Addr              { return proxyAddr("local") }
func (c *proxyConn) RemoteAddr() net.Addr             { return proxyAddr("remote") }
func (c *proxyConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *proxyConn) SetWriteDeadline(time.Time) error { return nil }

// proxyAddr is a placeholder net.Addr for proxied transports.
type proxyAddr string

func (p proxyAddr) Network() string { return "proxy" }
func (p proxyAddr) String() string  { return string(p) }

// authMethods builds the ssh.AuthMethod list for the configured Auth.
func (d *sshDialer) authMethods() ([]ssh.AuthMethod, error) {
	switch d.cfg.Auth {
	case config.AuthPassword:
		pw := d.cfg.Password
		// Some relay uses keyboard-interactive and may prompt more than once
		// (relay double-prompt). Answer every question with the password.
		ki := ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			ans := make([]string, len(questions))
			for i := range questions {
				ans[i] = pw
			}
			return ans, nil
		})
		return []ssh.AuthMethod{ki, ssh.Password(pw)}, nil
	case config.AuthAgent:
		signers, err := agentSigners()
		if err != nil {
			return nil, fmt.Errorf("relay: ssh-agent: %w", err)
		}
		if len(signers) == 0 {
			return nil, errors.New("relay: ssh-agent has no keys")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, nil
	default:
		return nil, fmt.Errorf("relay: unsupported auth %q", d.cfg.Auth)
	}
}

// hostKeyCallback returns a pinned or insecure callback.
func (d *sshDialer) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if d.cfg.HostKey == "" {
		// Mirrors legacy behaviour (ssh.InsecureIgnoreHostKey). Pin via `ntun test`.
		return ssh.InsecureIgnoreHostKey(), nil
	}
	// HostKey is an authorized_keys-style line ("ssh-ed25519 AAAA...") for the
	// dialed host.
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(d.cfg.HostKey))
	if err != nil {
		return nil, fmt.Errorf("relay: parse host_key: %w", err)
	}
	return ssh.FixedHostKey(pub), nil
}

// remoteAgentPath returns the absolute cache path on the forward server, given
// the remote user's (already resolved) home directory. The result contains no
// shell variables: the same string is shell-quoted in ensureAgent's check and
// upload commands and handed to sess.Start for exec, so they always reference
// the same file. (The old code left a literal `$HOME/` in the path; ensureAgent
// single-quoted it but sess.Start didn't, so the upload landed in a *literal*
// `$HOME` subdirectory while exec looked under /root — "No such file or
// directory".)
func (d *sshDialer) remoteAgentPath(home string) string {
	dir := strings.TrimRight(d.cfg.RemoteAgentDir, "/")
	switch {
	case dir == "" || dir == "~" || dir == "$HOME":
		return fmt.Sprintf("%s/ntun-agent-%s", home, d.agentSum)
	case strings.HasPrefix(dir, "~/"):
		return fmt.Sprintf("%s/%s/ntun-agent-%s", home, strings.TrimPrefix(dir, "~/"), d.agentSum)
	case strings.HasPrefix(dir, "$HOME/"):
		return fmt.Sprintf("%s/%s/ntun-agent-%s", home, strings.TrimPrefix(dir, "$HOME/"), d.agentSum)
	default:
		// Already an absolute path; use it verbatim.
		return fmt.Sprintf("%s/ntun-agent-%s", dir, d.agentSum)
	}
}

// remoteHome resolves the remote user's home directory over a throwaway
// session. Resolving it once per connect lets the cache path be a concrete
// absolute path everywhere, so `$HOME` can never be expanded in one place and
// left literal (quoted) in another.
func (d *sshDialer) remoteHome(client *ssh.Client) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.Output(`printf %s "$HOME"`)
	if err != nil {
		return "", fmt.Errorf("resolve remote home: %w", err)
	}
	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", errors.New("resolve remote home: empty $HOME")
	}
	return home, nil
}

// ensureAgent uploads the agent binary if it is not already cached remotely.
func (d *sshDialer) ensureAgent(client *ssh.Client, remotePath string) error {
	// Fast path: existence check.
	check, err := client.NewSession()
	if err != nil {
		return err
	}
	if err := check.Run(fmt.Sprintf("test -x %s", shellQuote(remotePath))); err == nil {
		check.Close()
		return nil
	}
	check.Close()

	// Slow path: upload via `cat > path && chmod +x path`.
	up, err := client.NewSession()
	if err != nil {
		return err
	}
	defer up.Close()
	up.Stdin = bytes.NewReader(d.agentBin)
	if err := up.Run(fmt.Sprintf("mkdir -p %s && cat > %s && chmod +x %s",
		shellQuote(parentDir(remotePath)),
		shellQuote(remotePath),
		shellQuote(remotePath),
	)); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	return nil
}

// sessionRWC adapts an SSH session's stdin/stdout pipes into a single
// io.ReadWriteCloser. Close tears down the session and the underlying client.
type sessionRWC struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader

	closeOnce sync.Once
}

func (s *sessionRWC) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *sessionRWC) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *sessionRWC) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		// Closing stdin signals the agent to exit; wait then tear down.
		firstErr = s.stdin.Close()
		_ = s.sess.Wait()
		_ = s.sess.Close()
		_ = s.client.Close()
	})
	return firstErr
}
