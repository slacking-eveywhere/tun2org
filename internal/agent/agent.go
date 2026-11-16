// Package agent runs the remote side of the ntun mux. It reads framed
// messages from stdin/stdout (an SSH session) and, for each OPEN, dials the
// requested target on the local network and pipes data both ways.
//
// It is deliberately tiny: no gVisor, no TUN, no config. The whole package
// depends only on the mux protocol and net.Dial, so the resulting binary is
// small enough to re-upload to the forward server on each connect.
package agent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"ntun-ng/internal/mux"
)

// Config configures an agent run.
type Config struct {
	// Dialer dials the target (TCP). Defaults to a net.Dialer with sane timeouts.
	Dialer interface {
		Dial(network, addr string) (net.Conn, error)
	}
	// UDPDialer dials the target (UDP). Defaults to a net.Dialer. If nil and a
	// UDP_OPEN arrives, the agent uses a connected UDP socket via net.Dial.
	UDPDialer interface {
		Dial(network, addr string) (net.Conn, error)
	}

	// Log receives human-readable status lines. nil = discard.
	Log Logger

	// PingEvery is the mux keepalive interval. 0 = 30s default.
	PingEvery time.Duration
}

// Logger is the minimal logging interface used by the agent.
type Logger interface {
	Printf(format string, args ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func (c *Config) logger() Logger {
	if c.Log != nil {
		return c.Log
	}
	return discardLogger{}
}

func (c *Config) udpDialer() interface {
	Dial(network, addr string) (net.Conn, error)
} {
	if c.UDPDialer != nil {
		return c.UDPDialer
	}
	return &net.Dialer{Timeout: 10 * time.Second}
}

func (c *Config) dialer() interface {
	Dial(network, addr string) (net.Conn, error)
} {
	if c.Dialer != nil {
		return c.Dialer
	}
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
}

// Run serves the mux over rwc until the stream closes or an error occurs.
// It is the entire remote-agent entry point.
func Run(rwc io.ReadWriteCloser, cfg Config) error {
	log := cfg.logger()
	ping := cfg.PingEvery
	if ping == 0 {
		ping = 30 * time.Second
	}

	accept := func(addr mux.Addr) (net.Conn, error) {
		network := "tcp"
		if addr.Type == mux.AddrIPv6 {
			network = "tcp6"
		} else if addr.Type == mux.AddrIPv4 {
			network = "tcp4"
		}
		log.Printf("agent: dial %s %s", network, addr.String())
		return cfg.dialer().Dial(network, addr.String())
	}
	acceptUDP := func(addr mux.Addr) (net.Conn, error) {
		network := "udp"
		if addr.Type == mux.AddrIPv6 {
			network = "udp6"
		} else if addr.Type == mux.AddrIPv4 {
			network = "udp4"
		}
		log.Printf("agent: dial %s %s", network, addr.String())
		return cfg.udpDialer().Dial(network, addr.String())
	}

	sess := mux.NewSession(rwc, mux.SessionConfig{
		AcceptFunc:    accept,
		AcceptUDPFunc: acceptUDP,
		PingEvery:     ping,
	})
	if err := sess.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("agent: %w", err)
	}
	return nil
}

// RunStdio is a convenience for the ntun-agent binary: it runs the agent over
// os.Stdin/os.Stdout and closes them on exit.
func RunStdio(cfg Config) error {
	rwc := stdioConn{}
	if err := Run(rwc, cfg); err != nil {
		return err
	}
	return nil
}

// stdioConn adapts os.Stdin/Stdout (separate fds) into a single
// io.ReadWriteCloser. Closing it does not close the process's real stdio
// (the process is about to exit anyway), but the agent self-cleans its
// uploaded file on exit (handled in cmd/ntun-agent).
type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdioConn) Close() error                { return nil }
