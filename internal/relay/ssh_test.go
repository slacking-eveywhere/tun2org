package relay

import (
	"strings"
	"testing"

	"ntun-ng/internal/config"
)

// TestRemoteAgentPathAbsolute verifies the cache path is a concrete absolute
// path with no shell variables, so the ensureAgent commands (which shell-quote
// it) and sess.Start (which does not) always refer to the same file.
func TestRemoteAgentPathAbsolute(t *testing.T) {
	tests := []struct {
		name      string
		remoteDir string
		want      string
	}{
		{"default", "~/.tun2org", "/root/.tun2org/ntun-agent-abcd1234"},
		{"home-alias", "$HOME/.tun2org", "/root/.tun2org/ntun-agent-abcd1234"},
		{"tilde-root", "~", "/root/ntun-agent-abcd1234"},
		{"absolute", "/opt/ntun", "/opt/ntun/ntun-agent-abcd1234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &sshDialer{
				cfg:      &config.Config{RemoteAgentDir: tc.remoteDir},
				agentSum: "abcd1234",
			}
			got := d.remoteAgentPath("/root")
			if got != tc.want {
				t.Fatalf("remoteAgentPath(%q) = %q, want %q", tc.remoteDir, got, tc.want)
			}
			if strings.Contains(got, "$HOME") || strings.Contains(got, "~") {
				t.Fatalf("remoteAgentPath(%q) contains an unexpanded shell variable: %q", tc.remoteDir, got)
			}
		})
	}
}
