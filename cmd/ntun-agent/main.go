// Command ntun-agent is the lightweight remote side of the ntun tunnel.
//
// It reads a framed mux stream from stdin and writes to stdout, dialing TCP
// targets on the local network and piping data. It is meant to be uploaded
// and exec'd by ntun over an SSH session where AllowTcpForwarding is off.
//
// Phase 1 (this binary): functional mux agent, stdlib-only, no flags beyond
// -version. Later phases add UDP/DNS relay and remote cleanup.
package main

import (
	"flag"
	"fmt"
	"os"

	"ntun-ng/internal/agent"
	"ntun-ng/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s (agent)\n", version.String())
		return
	}

	cfg := agent.Config{
		Log: stdlog{},
	}
	if err := agent.RunStdio(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.AgentName, err)
		os.Exit(1)
	}
}

type stdlog struct{}

func (stdlog) Printf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
