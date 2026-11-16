// Command ntun is the local client of the ntun tunnel: TUN device, gVisor
// netstack, SSH relay, and the local side of the mux.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"ntun-ng/internal/app"
	"ntun-ng/internal/config"
	"ntun-ng/internal/logx"
	"ntun-ng/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println(version.String())
			return
		case "up":
			os.Exit(runUp(os.Args[2:]))
		case "down":
			os.Exit(runDown(os.Args[2:]))
		case "test":
			os.Exit(runTest(os.Args[2:]))
		case "check":
			os.Exit(runCheck(os.Args[2:]))
		case "config":
			os.Exit(runConfig(os.Args[2:]))
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintf(os.Stderr, `%s

Usage:
  ntun up    [--config PATH] [--profile NAME]... [--username USER] [--forward-server HOST] [--auth password|agent]
  ntun down  [PROFILE]...
  ntun test  [--config PATH] [--profile NAME]... [--target HOST:PORT] [--timeout DURATION]
  ntun check [--config PATH] [--profile NAME]...
  ntun config init|edit [--config PATH]
  ntun version

The config is a JSON object of profiles, one tunnel per profile:
  {"internal_server": {...}, "external_server": {...}}
`, version.String())
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return home + "/.tun2org/config.json"
}

// multiFlag collects repeated string flag values (--profile a --profile b).
type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ",") }
func (f *multiFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// selectProfiles resolves a --profile selection (empty = all) against the
// loaded file, preserving file order.
func selectProfiles(p *config.Profiles, want []string) ([]string, error) {
	if len(want) == 0 {
		return p.Names(), nil
	}
	names := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, w := range want {
		if _, ok := p.Get(w); !ok {
			return nil, fmt.Errorf("no such profile %q (available: %s)", w, strings.Join(p.Names(), ", "))
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		names = append(names, w)
	}
	return names, nil
}

// profileLogger builds the per-profile logger: "[abc]: msg", the tag colored
// with the profile's palette color when stderr is a color terminal.
func profileLogger(i int, name string) logx.Logger {
	base := logx.ToWriter(os.Stderr)
	tag := logx.Tag(name)
	if logx.ColorEnabled(os.Stderr) {
		return logx.Colorize(base, tag, logx.Palette[i%len(logx.Palette)])
	}
	return logx.Prefix(base, tag)
}

// profileTag returns the colored (when supported) tag for prompts.
func profileTag(i int, name string) string {
	tag := logx.Tag(name)
	if logx.ColorEnabled(os.Stderr) {
		return fmt.Sprintf("\x1b[%dm%s\x1b[0m", logx.Palette[i%len(logx.Palette)], tag)
	}
	return tag
}

func runUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to JSON config")
	username := fs.String("username", "", "override config.username in every selected profile")
	forward := fs.String("forward-server", "", "override config.forward_server in every selected profile")
	auth := fs.String("auth", "", "override config.auth in every selected profile (password|agent)")
	var profiles multiFlag
	fs.Var(&profiles, "profile", "run only this profile (repeatable; default: all)")
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

	cfgs := make([]*config.Config, 0, len(names))
	for _, n := range names {
		cfg, _ := profs.Get(n)
		if *username != "" {
			cfg.Username = *username
		}
		if *forward != "" {
			cfg.ForwardServer = *forward
		}
		if *auth != "" {
			cfg.Auth = config.Auth(*auth)
		}
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "ntun: profile %q: %v\n", n, err)
			return 2
		}
		cfgs = append(cfgs, cfg)
	}

	// Prompt for passwords sequentially before spawning so prompts never
	// interleave with tunnel startup logs.
	for i, cfg := range cfgs {
		if cfg.Auth == config.AuthPassword && cfg.Password == "" {
			pw, err := readPassword(profileTag(i, cfg.Profile) + " Password: ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "ntun: read password: %v\n", err)
				return 2
			}
			cfg.Password = pw
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "ntun: shutting down")
		cancel()
	}()

	// One tunnel per profile, docker compose style. A fatal error in any
	// profile tears the whole run down (predictable all-or-nothing state).
	errs := make([]error, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Add(1)
		go func(i int, cfg *config.Config) {
			defer wg.Done()
			log := profileLogger(i, cfg.Profile)
			log("connecting to %s as %s (auth %s)", cfg.SSHHostPort(), cfg.Username, cfg.Auth)
			if err := app.Run(ctx, cfg, log); err != nil && !errors.Is(err, context.Canceled) {
				errs[i] = err
				log("tunnel exited: %v", err)
				cancel()
			}
		}(i, cfg)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
			return 1
		}
	}
	return 0
}

// runCheck implements `ntun check`: load + validate the config (schema,
// per-profile fields, cross-profile collisions) and print a summary. No
// tunnel is started. Exit 0 = OK, 2 = config error.
func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to JSON config")
	var profiles multiFlag
	fs.Var(&profiles, "profile", "check only this profile (repeatable; default: all)")
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

	fmt.Printf("%-24s %-10s %-18s %-7s %-26s %s\n", "PROFILE", "DEVICE", "TUN_NETWORK", "ROUTES", "FORWARD", "AUTH")
	for _, n := range names {
		c, _ := profs.Get(n)
		fmt.Printf("%-24s %-10s %-18s %-7d %-26s %s\n",
			n, c.TunDevice, c.TunNetwork, len(c.Routes), c.SSHHostPort(), c.Auth)
	}
	s := ""
	if len(names) != 1 {
		s = "s"
	}
	fmt.Printf("config OK (%d profile%s)\n", len(names), s)
	return 0
}

// runConfig handles `ntun config init|edit [--config PATH]`. It scans the args
// manually (instead of flag.Set) so --config works both before and after the
// subcommand, which Go's flag package would otherwise stop at.
func runConfig(args []string) int {
	var cfgPath string
	var sub string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			cfgPath = strings.TrimPrefix(a, "--config=")
		case a == "--help" || a == "-h":
			fmt.Fprintln(os.Stderr, "usage: ntun config init|edit [--config PATH]")
			return 2
		case sub == "" && !strings.HasPrefix(a, "-"):
			sub = a
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "ntun: unknown config flag %q\n", a)
			return 2
		}
	}
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}
	if sub == "" {
		fmt.Fprintln(os.Stderr, "usage: ntun config init|edit [--config PATH]")
		return 2
	}
	switch sub {
	case "init":
		return configInit(cfgPath)
	case "edit":
		return configEdit(cfgPath)
	default:
		fmt.Fprintf(os.Stderr, "ntun: unknown config subcommand %q\n", sub)
		return 2
	}
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := readPasswordFromTerminal()
	if err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stderr)
	return pw, nil
}

// runConfig helpers and terminal password reading live in cli_helpers.go.
