package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"ntun-ng/internal/config"
	"ntun-ng/internal/mux"
	"ntun-ng/internal/relay"
)

// runTest implements `ntun test`: connectivity probe over the real path —
// config, SSH auth, proxy_command, agent upload, mux PING/PONG, and an
// optional TCP dial through the tunnel. Exit 0 = healthy, 1 = failure,
// 2 = usage/config error. Replaces the legacy expect-based --test.
func runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to JSON config")
	target := fs.String("target", "", "optional host:port to dial through the tunnel (agent-side reachability)")
	timeout := fs.Duration("timeout", 45*time.Second, "overall probe timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if cfg.Auth == config.AuthPassword && cfg.Password == "" {
		pw, err := readPassword("Password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ntun: read password: %v\n", err)
			return 2
		}
		cfg.Password = pw
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	fmt.Fprintf(os.Stderr, "ntun: testing %s as %s (%s/%s)\n",
		cfg.SSHHostPort(), cfg.SSHUser(), cfg.Mode, cfg.Auth)

	wasKnown := relay.IsKnownHost(cfg.SSHHostPort())

	d, err := relay.NewDialer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 1
	}
	rwc, err := d.DialSession(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: connect: %v\n", err)
		return 1
	}
	defer rwc.Close()

	sess := mux.NewSession(rwc, mux.SessionConfig{})
	go sess.Serve()
	if err := sess.Ping(10 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: mux ping: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "ntun: session ok (mux ping/pong)")

	if *target != "" {
		host, portStr, err := net.SplitHostPort(*target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ntun: --target: %v\n", err)
			return 2
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			fmt.Fprintf(os.Stderr, "ntun: --target: bad port %q\n", portStr)
			return 2
		}
		c, err := sess.Open(mux.FromHostPort(host, uint16(port)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ntun: target %s unreachable through the tunnel: %v\n", *target, err)
			return 1
		}
		_ = c.Close()
		fmt.Fprintf(os.Stderr, "ntun: target %s reachable through the tunnel\n", *target)
	}

	if wasKnown {
		fmt.Fprintln(os.Stderr, "ntun: host key: pinned (known_hosts match)")
	} else {
		fmt.Fprintf(os.Stderr, "ntun: host key: captured on this run into %s (TOFU)\n", knownHostsDisplay())
	}
	return 0
}

// knownHostsDisplay returns the known_hosts path or "<unknown>" on error.
func knownHostsDisplay() string {
	p, err := relay.KnownHostsPath()
	if err != nil {
		return "<unknown>"
	}
	return p
}
