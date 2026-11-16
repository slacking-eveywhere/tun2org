package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
)

// Profiles is the parsed multi-profile config file: an ordered set of named
// Config values. Order follows the JSON file so log colors and prompts are
// stable (encoding/json maps lose key order, so decoding is done by token
// walk).
type Profiles struct {
	names   []string
	configs map[string]*Config
}

// Names returns the profile names in file order.
func (p *Profiles) Names() []string {
	out := make([]string, len(p.names))
	copy(out, p.names)
	return out
}

// Get returns the config of a profile.
func (p *Profiles) Get(name string) (*Config, bool) {
	c, ok := p.configs[name]
	return c, ok
}

// Len returns the number of profiles.
func (p *Profiles) Len() int { return len(p.names) }

// LoadFile reads a multi-profile config file and applies per-profile
// Defaults + Validate. Cross-profile collisions are checked by ValidateAll.
//
// Schema: { "<profile>": { ...Config... }, ... }. A flat single-profile
// document is not accepted — there is exactly one schema.
func LoadFile(path string) (*Profiles, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf(
			"config: parse %s: top level must be a JSON object mapping profile names to configs, e.g. {\"my_server\": {\"username\": ...}}", path)
	}

	// Reject flat pre-multi-profile configs with a helpful message
	// (otherwise they surface as a confusing decode error under the first
	// key, e.g. profile "username").
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err == nil {
		for _, k := range []string{"username", "forward_server", "password", "auth", "routes", "tun_device", "proxy_command"} {
			if _, ok := probe[k]; ok {
				return nil, fmt.Errorf(
					"config: %s: flat single-profile config is not supported: wrap it under a profile name, e.g. {\"my_server\": { ...current content... }}", path)
			}
		}
	}

	p := &Profiles{configs: map[string]*Config{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
		name, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("config: parse %s: profile name must be a string", path)
		}
		if !validProfileName(name) {
			return nil, fmt.Errorf(
				"config: %s: invalid profile name %q: use 1-32 characters of [a-zA-Z0-9_-]", path, name)
		}
		if _, dup := p.configs[name]; dup {
			return nil, fmt.Errorf("config: %s: duplicate profile %q", path, name)
		}

		c := &Config{}
		if err := dec.Decode(c); err != nil {
			return nil, fmt.Errorf("config: %s: profile %q: %w", path, name, err)
		}
		c.Profile = name
		c.Defaults()
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("config: %s: profile %q: %w", path, name, err)
		}
		p.names = append(p.names, name)
		p.configs[name] = c
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if len(p.names) == 0 {
		return nil, fmt.Errorf("config: %s: no profiles defined", path)
	}
	return p, nil
}

// ValidateAll checks cross-profile collisions: TUN devices must be unique,
// and tun networks (v4/v6) and routes must be pairwise disjoint across
// profiles (including routes landing inside another profile's tun network).
// dns_listen addresses must be unique. dns_hijack is not restricted: it
// intercepts UDP/53 on its own device only. Several profiles may share the
// same forward_server (separate sessions).
func ValidateAll(p *Profiles) error {
	owner := map[string]string{}
	for _, n := range p.names {
		dev := p.configs[n].TunDevice
		if prev, ok := owner[dev]; ok {
			return fmt.Errorf(
				"config: tun_device %q used by profiles %q and %q: every profile needs a distinct tun_device", dev, prev, n)
		}
		owner[dev] = n
	}

	for i := 0; i < len(p.names); i++ {
		for j := i + 1; j < len(p.names); j++ {
			if err := pairDisjoint(p.configs[p.names[i]], p.names[i], p.configs[p.names[j]], p.names[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// pairDisjoint checks all address collisions between two profiles.
func pairDisjoint(a *Config, an string, b *Config, bn string) error {
	aNet := tunPrefixes(a)
	bNet := tunPrefixes(b)
	aRoutes := routePrefixes(a)
	bRoutes := routePrefixes(b)

	if x, y, ok := firstOverlap(aNet, bNet); ok {
		return fmt.Errorf("config: profiles %q and %q: tun_network %s overlaps %s", an, bn, x.String(), y.String())
	}
	if x, y, ok := firstOverlap(aRoutes, bRoutes); ok {
		return fmt.Errorf("config: profiles %q and %q: route %s overlaps route %s", an, bn, x.String(), y.String())
	}
	if x, y, ok := firstOverlap(aRoutes, bNet); ok {
		return fmt.Errorf("config: profiles %q and %q: route %s overlaps tun_network %s", an, bn, x.String(), y.String())
	}
	if x, y, ok := firstOverlap(bRoutes, aNet); ok {
		return fmt.Errorf("config: profiles %q and %q: route %s overlaps tun_network %s", bn, an, x.String(), y.String())
	}
	if a.DNSListen != "" && a.DNSListen == b.DNSListen {
		return fmt.Errorf("config: profiles %q and %q: dns_listen %s used twice", an, bn, a.DNSListen)
	}
	return nil
}

// tunPrefixes parses the (optional) v4 and v6 tun networks of c.
func tunPrefixes(c *Config) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{c.TunNetwork, c.TunNetworkV6} {
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// routePrefixes parses the routes of c. Invalid CIDRs were already rejected
// per-profile at route-add time; unparseable entries are skipped here.
func routePrefixes(c *Config) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(c.Routes))
	for _, s := range c.Routes {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// firstOverlap returns the first overlapping prefix pair, if any.
func firstOverlap(as, bs []netip.Prefix) (netip.Prefix, netip.Prefix, bool) {
	for _, a := range as {
		for _, b := range bs {
			if a.Overlaps(b) {
				return a, b, true
			}
		}
	}
	return netip.Prefix{}, netip.Prefix{}, false
}

// validProfileName reports whether name is a valid profile key: 1-32
// characters of [a-zA-Z0-9_-]. The name lands in log prefixes and state
// bookkeeping, so it stays conservative.
func validProfileName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
