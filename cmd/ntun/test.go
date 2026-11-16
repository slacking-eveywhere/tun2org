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
	"ntun-ng/internal/logx"
	"ntun-ng/internal/mux"
	"ntun-ng/internal/relay"
)

// runTest implements `ntun test`: connectivity probe over the real path —
// config, SSH auth, proxy_command, agent upload, mux PING/PONG, and an
// optional TCP dial through the tunnel — for every selected profile.
// Exit 0 = healthy, 1 = at least one profile failed, 2 = usage/config
// error. Replaces the legacy expect-based --test.
func runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to JSON config")
	target := fs.String("target", "", "optional host:port to dial through the tunnel (agent-side reachability)")
	timeout := fs.Duration("timeout", 45*time.Second, "per-profile probe timeout")
	var profiles multiFlag
	fs.Var(&profiles, "profile", "test only this profile (repeatable; default: all)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	profs, err := config.LoadFile(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if err := config.ValidateAll(profs); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	names, err := selectProfiles(profs, profiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}

	// Prompt for passwords up-front so prompts never interleave.
	cfgs := make([]*config.Config, 0, len(names))
	for i, n := range names {
		cfg, _ := profs.Get(n)
		if cfg.Auth == config.AuthPassword && cfg.Password == "" {
			pw, err := readPassword(profileTag(i, cfg.Profile) + " Password: ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "ntun: read password: %v\n", err)
				return 2
			}
			cfg.Password = pw
		}
		cfgs = append(cfgs, cfg)
	}

	failed := make([]string, 0, len(cfgs))
	for i, cfg := range cfgs {
		log := profileLogger(i, cfg.Profile)
		log("testing %s as %s (auth %s)", cfg.SSHHostPort(), cfg.Username, cfg.Auth)
		if err := probeProfile(cfg, log, *target, *timeout); err != nil {
			log("FAIL: %v", err)
			failed = append(failed, cfg.Profile)
			continue
		}
		log("ok")
	}

	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "ntun: failed profile(s): %v\n", failed)
		return 1
	}
	fmt.Fprintln(os.Stderr, "ntun: all profiles ok")
	return 0
}

// probeProfile runs the full probe sequence for one profile: SSH auth,
// proxy_command, agent upload, mux PING/PONG, and the optional target dial.
func probeProfile(cfg *config.Config, log logx.Logger, target string, timeout time.Duration) error {
	if log == nil {
		log = logx.Discard
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	wasKnown := relay.IsKnownHost(cfg.SSHHostPort())

	d, err := relay.NewDialer(cfg)
	if err != nil {
		return err
	}
	rwc, err := d.DialSession(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer rwc.Close()

	sess := mux.NewSession(rwc, mux.SessionConfig{})
	go sess.Serve()
	if err := sess.Ping(10 * time.Second); err != nil {
		return fmt.Errorf("mux ping: %w", err)
	}
	log("session ok (mux ping/pong)")

	if target != "" {
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			return fmt.Errorf("--target: %v", err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("--target: bad port %q", portStr)
		}
		c, err := sess.Open(mux.FromHostPort(host, uint16(port)))
		if err != nil {
			return fmt.Errorf("target %s unreachable through the tunnel: %w", target, err)
		}
		_ = c.Close()
		log("target %s reachable through the tunnel", target)
	}

	if wasKnown {
		log("host key: pinned (known_hosts match)")
	} else {
		log("host key: captured on this run into %s (TOFU)", knownHostsDisplay())
	}
	return nil
}

// knownHostsDisplay returns the known_hosts path or "<unknown>" on error.
func knownHostsDisplay() string {
	p, err := relay.KnownHostsPath()
	if err != nil {
		return "<unknown>"
	}
	return p
}
