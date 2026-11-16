// Package config defines the JSON configuration for the ntun local client.
//
// The agent (ntun-agent) takes no config: it reads a mux stream from stdio.
// Only ntun needs a config file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Mode selects how ntun reaches the forward server.
type Mode string

const (
	// ModeDirect: SSH straight to forward_server:22 as <username>.
	ModeDirect Mode = "direct"
	// ModeBastion: SSH to bastion:22 as <username>@<forward_server> (the
	// relay username dance; the relay routes the session to forward_server).
	ModeBastion Mode = "bastion"
)

// Auth selects the SSH authentication method.
type Auth string

const (
	AuthPassword Auth = "password"
	AuthAgent    Auth = "agent"
)

// Config is the on-disk JSON configuration.
type Config struct {
	Username string `json:"username"`
	// Password is the plaintext password (when Auth == "password"). If empty
	// and Auth == "password", ntun prompts on the terminal.
	Password string `json:"password,omitempty"`

	// ForwardServer is the target host inside the SIS network
	// (e.g. bla.bli.blou).
	ForwardServer string `json:"forward_server"`
	// ForwardPort is the SSH port on the forward server (direct mode). Default 22.
	ForwardPort int `json:"forward_port,omitempty"`

	// Mode is direct or bastion. Default bastion.
	Mode Mode `json:"mode,omitempty"`
	Bastion string `json:"bastion,omitempty"`
	// BastionPort is the relay SSH port. Default 22.
	BastionPort int `json:"bastion_port,omitempty"`

	// Auth is "password" or "agent". Default password.
	Auth Auth `json:"auth,omitempty"`

	// HostKey is the base64-encoded SSH public host key for the dialed host.
	// Empty => insecure (warn). Mirrors legacy behaviour; pin via `ntun test`.
	HostKey string `json:"host_key,omitempty"`

	// AgentPath is the local ntun-agent binary to upload. Default "ntun-agent".
	AgentPath string `json:"agent_path,omitempty"`
	// RemoteAgentDir is the remote cache dir for the uploaded agent.
	// Default ~/.tun2org on the remote host.
	RemoteAgentDir string `json:"remote_agent_dir,omitempty"`

	// Routes are CIDRs routed through the TUN. IPv4 and IPv6 accepted.
	Routes []string `json:"routes"`
	// TunDevice is the TUN interface name. Default tun0.
	TunDevice string `json:"tun_device,omitempty"`
	// TunNetwork is the IPv4 address/CIDR for the TUN. Default 198.18.0.1/15.
	TunNetwork string `json:"tun_network,omitempty"`
	// TunNetworkV6 is the optional IPv6 address/CIDR for the TUN.
	TunNetworkV6 string `json:"tun_network_v6,omitempty"`
	// MTU is the TUN MTU. 0 = driver default.
	MTU int `json:"mtu,omitempty"`

	// Interface binds the outgoing SSH socket to a physical interface so
	// proxy traffic bypasses the TUN routes (avoids loop). Empty = auto
	// (default route interface). Not supported when ProxyCommand is set.
	Interface string `json:"interface,omitempty"`

	// ProxyCommand, when set, runs an arbitrary command to establish the TCP
	// connection to the SSH server instead of dialing directly (OpenSSH
	// ProxyCommand semantics, e.g. `ssh -W %h:%p jumper`). "%h" and "%p"
	// are replaced with the SSH host and port. Empty = direct TCP dial.
	ProxyCommand string `json:"proxy_command,omitempty"`

	// DNSHijack intercepts UDP/53 on the TUN and relays raw DNS to the agent.
	// Default true.
	DNSHijack *bool `json:"dns_hijack,omitempty"`
	// DNSRemote is the resolver the agent forwards hijacked DNS to. Empty =
	// forward server's resolver (phase 5).
	DNSRemote string `json:"dns_remote,omitempty"`
	// DNSListen is an optional local DNS server address (e.g. 127.0.0.1:5353)
	// that forwards over the mux. Empty = disabled (phase 5).
	DNSListen string `json:"dns_listen,omitempty"`

	// UDPTimeout is the idle timeout for UDP sessions in the netstack.
	// Default 30s. 0 = no timeout.
	UDPTimeout time.Duration `json:"udp_timeout,omitempty"`
	// TCPSendBufferSize / TCPReceiveBufferSize / TCPAutoTuning are passthrough
	// netstack TCP buffer knobs (phase 3).
	TCPSendBufferSize    string `json:"tcp_sndbuf,omitempty"`
	TCPReceiveBufferSize string `json:"tcp_rcvbuf,omitempty"`
	TCPAutoTuning        bool   `json:"tcp_auto_tuning,omitempty"`

	// TelnetAddressToUse / TelnetPortToUse are the optional upstream liveness
	// check target (legacy --test behaviour).
	TelnetAddressToUse string `json:"telnet_address_to_use,omitempty"`
	TelnetPortToUse    int    `json:"telnet_port_to_use,omitempty"`

	// PostStartScript / PostStopScript run on tunnel up/down (legacy).
	PostStartScript string `json:"post_start_script,omitempty"`
	PostStopScript  string `json:"post_stop_script,omitempty"`

	// LogLevel: debug|info|warn|error. Default info.
	LogLevel string `json:"log_level,omitempty"`

	// PingEvery is the mux keepalive interval. Default 30s.
	PingEvery time.Duration `json:"ping_every,omitempty"`

	// ReconnectMin / ReconnectMax bound the exponential reconnect backoff.
	// Defaults 1s / 30s.
	ReconnectMin time.Duration `json:"reconnect_min,omitempty"`
	ReconnectMax time.Duration `json:"reconnect_max,omitempty"`
}

// Defaults applies zero-value defaults.
func (c *Config) Defaults() {
	if c.Mode == "" {
		c.Mode = ModeBastion
	}
	if c.Auth == "" {
		c.Auth = AuthPassword
	}
	if c.Bastion == "" {
		c.Bastion = "relais-ssh.bli.blou"
	}
	if c.BastionPort == 0 {
		c.BastionPort = 22
	}
	if c.ForwardPort == 0 {
		c.ForwardPort = 22
	}
	if c.AgentPath == "" {
		c.AgentPath = "ntun-agent"
	}
	if c.RemoteAgentDir == "" {
		c.RemoteAgentDir = "~/.tun2org"
	}
	if c.TunDevice == "" {
		c.TunDevice = "tun0"
	}
	if c.TunNetwork == "" {
		c.TunNetwork = "198.18.0.1/15"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.PingEvery == 0 {
		c.PingEvery = 30 * time.Second
	}
	if c.ReconnectMin == 0 {
		c.ReconnectMin = 1 * time.Second
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = 30 * time.Second
	}
	if c.UDPTimeout == 0 {
		c.UDPTimeout = 30 * time.Second
	}
	if c.DNSHijack == nil {
		t := true
		c.DNSHijack = &t
	}
}

// Validate checks required fields after Defaults.
func (c *Config) Validate() error {
	if c.Username == "" {
		return errors.New("config: username is required")
	}
	if c.ForwardServer == "" {
		return errors.New("config: forward_server is required")
	}
	switch c.Mode {
	case ModeDirect, ModeBastion:
	default:
		return fmt.Errorf("config: invalid mode %q", c.Mode)
	}
	switch c.Auth {
	case AuthPassword, AuthAgent:
	default:
		return fmt.Errorf("config: invalid auth %q", c.Auth)
	}
	return nil
}

// SSHUser returns the SSH username for the configured mode.
//
// Bastion mode uses the relay dance: <user>@<forward_server>, which the
// relay routes to the forward server. Direct mode uses <user> verbatim.
func (c *Config) SSHUser() string {
	if c.Mode == ModeBastion {
		return c.Username + "@" + c.ForwardServer
	}
	return c.Username
}

// SSHHostPort returns the dial target (host:port) for the configured mode.
func (c *Config) SSHHostPort() string {
	if c.Mode == ModeBastion {
		return fmt.Sprintf("%s:%d", c.Bastion, c.BastionPort)
	}
	return fmt.Sprintf("%s:%d", c.ForwardServer, c.ForwardPort)
}

// MarshalJSON emits the duration fields as human-readable strings (e.g.
// "30s") instead of raw nanoseconds, matching the documented config format.
func (c Config) MarshalJSON() ([]byte, error) {
	type cfgAlias Config
	return json.Marshal(struct {
		cfgAlias
		UDPTimeout   string `json:"udp_timeout,omitempty"`
		PingEvery    string `json:"ping_every,omitempty"`
		ReconnectMin string `json:"reconnect_min,omitempty"`
		ReconnectMax string `json:"reconnect_max,omitempty"`
	}{
		cfgAlias:     cfgAlias(c),
		UDPTimeout:   time.Duration(c.UDPTimeout).String(),
		PingEvery:    time.Duration(c.PingEvery).String(),
		ReconnectMin: time.Duration(c.ReconnectMin).String(),
		ReconnectMax: time.Duration(c.ReconnectMax).String(),
	})
}

// Load reads and parses a JSON config file and applies Defaults + Validate.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// UnmarshalJSON decodes the config, allowing duration fields (udp_timeout,
// ping_every, reconnect_min, reconnect_max) to be written as JSON strings such
// as "30s". The duration fields are shadowed with RawMessage so the rest of the
// struct decodes normally.
func (c *Config) UnmarshalJSON(data []byte) error {
	type cfgAlias Config
	var aux struct {
		cfgAlias
		UDPTimeout   json.RawMessage `json:"udp_timeout"`
		PingEvery    json.RawMessage `json:"ping_every"`
		ReconnectMin json.RawMessage `json:"reconnect_min"`
		ReconnectMax json.RawMessage `json:"reconnect_max"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*c = Config(aux.cfgAlias)

	parseDuration := func(raw json.RawMessage) (time.Duration, error) {
		if len(raw) == 0 {
			return 0, nil
		}
		if raw[0] == '"' {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return 0, err
			}
			return time.ParseDuration(s)
		}
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, err
		}
		return time.Duration(n), nil
	}
	var err error
	if c.UDPTimeout, err = parseDuration(aux.UDPTimeout); err != nil {
		return fmt.Errorf("config: udp_timeout: %w", err)
	}
	if c.PingEvery, err = parseDuration(aux.PingEvery); err != nil {
		return fmt.Errorf("config: ping_every: %w", err)
	}
	if c.ReconnectMin, err = parseDuration(aux.ReconnectMin); err != nil {
		return fmt.Errorf("config: reconnect_min: %w", err)
	}
	if c.ReconnectMax, err = parseDuration(aux.ReconnectMax); err != nil {
		return fmt.Errorf("config: reconnect_max: %w", err)
	}
	return nil
}
