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
  ntun up    [--config PATH] [--username USER] [--forward-server HOST] [--mode direct|bastion] [--auth password|agent]
  ntun down
  ntun test [--config PATH] [--target HOST:PORT] [--timeout DURATION]
  ntun config init [--config PATH]
  ntun config edit [--config PATH]
  ntun version
`, version.String())
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return home + "/.tun2org/config.json"
}

func runUp(args []string) int {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to JSON config")
	username := fs.String("username", "", "override config.username")
	forward := fs.String("forward-server", "", "override config.forward_server")
	mode := fs.String("mode", "", "override config.mode (direct|bastion)")
	auth := fs.String("auth", "", "override config.auth (password|agent)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if *username != "" {
		cfg.Username = *username
	}
	if *forward != "" {
		cfg.ForwardServer = *forward
	}
	if *mode != "" {
		cfg.Mode = config.Mode(*mode)
	}
	if *auth != "" {
		cfg.Auth = config.Auth(*auth)
	}
	if err := cfg.Validate(); err != nil {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "ntun: shutting down")
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "ntun: connecting to %s as %s (%s/%s)\n",
		cfg.SSHHostPort(), cfg.SSHUser(), cfg.Mode, cfg.Auth)
	if err := app.Run(ctx, cfg, logx.ToWriter(os.Stderr)); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 1
	}
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
