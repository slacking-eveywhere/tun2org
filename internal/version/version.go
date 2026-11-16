// Package version holds build metadata for ntun-ng.
package version

import "fmt"

var (
	// Version is the binary version, set at build time or by ldflags.
	Version = "dev"
	// Commit is the git commit (plus -dirty marker) the binary was built
	// from, set by install.sh build via -ldflags.
	Commit = "unknown"
	// AgentName is the remote agent binary name.
	AgentName = "ntun-agent"
)

// String returns a human-readable version string.
func String() string {
	if Commit == "unknown" {
		return fmt.Sprintf("ntun-ng %s", Version)
	}
	return fmt.Sprintf("ntun-ng %s (%s)", Version, Commit)
}
