// Package config loads and validates the jauntbox client configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Profile maps a profile name to a Hetzner server type.
type Profile struct {
	ServerType string `yaml:"server_type" json:"server_type"`
	Arch       string `yaml:"arch" json:"arch"`
}

// Limits are the lifetime limits enforced by the on-box supervisor.
type Limits struct {
	MaxUptime         Duration `yaml:"max_uptime" json:"max_uptime"`
	EveningCutoff     string   `yaml:"evening_cutoff" json:"evening_cutoff"`
	IdleDay           Duration `yaml:"idle_day" json:"idle_day"`
	IdleNight         Duration `yaml:"idle_night" json:"idle_night"`
	NightHours        string   `yaml:"night_hours" json:"night_hours"`
	HumanActiveWindow Duration `yaml:"human_active_window" json:"human_active_window"`
	WrapUpGrace       Duration `yaml:"wrapup_grace" json:"wrapup_grace"`
}

// Agent configures agent runs on the box.
type Agent struct {
	MaxDuration Duration `yaml:"max_duration" json:"max_duration"`
	// HostPorts are TCP ports on the box that agents may reach (host.jauntbox:<port>).
	HostPorts []int `yaml:"host_ports" json:"host_ports,omitempty"`
}

// CheckHostPorts validates a list of host ports an agent may reach; field
// names the source (a config key or flag) in errors.
func CheckHostPorts(field string, ports []int) error {
	seen := map[int]bool{}
	for _, p := range ports {
		switch {
		case p < 1 || p > 65535:
			return fmt.Errorf("%s: port %d is out of range (want 1-65535)", field, p)
		case p == 22:
			return fmt.Errorf("%s: port 22 is the box's SSH port; agents may not reach it", field)
		case seen[p]:
			return fmt.Errorf("%s: port %d is listed twice", field, p)
		}
		seen[p] = true
	}
	return nil
}

// GitHub configures the forge integration.
type GitHub struct {
	Org               string   `yaml:"org"`
	ProtectedPatterns []string `yaml:"protected_patterns"`
	// AllowUnprotected accepts repos GitHub will not protect (rulesets on a
	// private repo need a paid plan): agents can then push to their default branch.
	AllowUnprotected bool `yaml:"allow_unprotected"`
}

// Config is the client configuration file.
type Config struct {
	User               string             `yaml:"user"`
	SSHPublicKey       string             `yaml:"ssh_public_key"`
	Location           string             `yaml:"location"`
	Image              string             `yaml:"image"`
	Timezone           string             `yaml:"timezone"`
	DefaultProfile     string             `yaml:"default_profile"`
	BoxBinary          string             `yaml:"box_binary"`
	VolumeSizeGB       int                `yaml:"volume_size_gb"`
	Profiles           map[string]Profile `yaml:"profiles"`
	Limits             Limits             `yaml:"limits"`
	HetznerTokenEnv    string             `yaml:"hetzner_token_env"`
	TailscaleAPIKeyEnv string             `yaml:"tailscale_api_key_env"`
	GitHub             GitHub             `yaml:"github"`
	Agent              Agent              `yaml:"agent"`
}

// Default returns the built-in defaults. The server types are unverified
// placeholders (checked in a later task).
func Default() Config {
	return Config{
		Location:       "fsn1",
		Image:          "ubuntu-26.04",
		DefaultProfile: "small",
		VolumeSizeGB:   100,
		Profiles: map[string]Profile{
			"small": {ServerType: "cpx42", Arch: "amd64"},
			"big":   {ServerType: "ccx33", Arch: "amd64"},
			"huge":  {ServerType: "ccx43", Arch: "amd64"},
		},
		Limits: Limits{
			MaxUptime:         Duration(10 * time.Hour),
			EveningCutoff:     "19:00",
			IdleDay:           Duration(2 * time.Hour),
			IdleNight:         Duration(time.Hour),
			NightHours:        "22:00-07:00",
			HumanActiveWindow: Duration(30 * time.Minute),
			WrapUpGrace:       Duration(5 * time.Minute),
		},
		Agent:              Agent{MaxDuration: Duration(8 * time.Hour)},
		HetznerTokenEnv:    "HCLOUD_TOKEN",
		TailscaleAPIKeyEnv: "TAILSCALE_API_KEY",
	}
}

// Load reads path over Default() and validates the result.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)                                             // a typo'd key must fail, naming the key
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) { // io.EOF = empty file
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	for name, p := range c.Profiles {
		if p.Arch == "" {
			p.Arch = "amd64"
			c.Profiles[name] = p
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	hhmm     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	hhmmSpan = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// Validate checks the config for missing or invalid values.
func (c Config) Validate() error {
	if c.User == "" {
		return fmt.Errorf("user is required (set `user:` in the config)")
	}
	if len(c.User) > 40 || !dnsLabel.MatchString(c.User) {
		return fmt.Errorf("user must be lowercase letters, digits and dashes (got %q)", c.User)
	}
	if c.Timezone == "" {
		return fmt.Errorf("timezone is required (an IANA name such as Europe/Paris)")
	}
	if _, err := c.Loc(); err != nil {
		return err
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return fmt.Errorf("default_profile %q not in profiles (have: %v)", c.DefaultProfile, c.profileNames())
	}
	for _, name := range c.profileNames() {
		p := c.Profiles[name]
		if p.Arch != "amd64" {
			return fmt.Errorf("profile %q: arch %q not supported in v1 (amd64 only)", name, p.Arch)
		}
		if p.ServerType == "" {
			return fmt.Errorf("profile %q: server_type is required", name)
		}
	}
	if !hhmm.MatchString(c.Limits.EveningCutoff) {
		return fmt.Errorf("invalid evening_cutoff %q (want HH:MM, e.g. \"19:00\")", c.Limits.EveningCutoff)
	}
	if !hhmmSpan.MatchString(c.Limits.NightHours) {
		return fmt.Errorf("invalid night_hours %q (want HH:MM-HH:MM, e.g. \"22:00-07:00\")", c.Limits.NightHours)
	}
	if d := time.Duration(c.Agent.MaxDuration); d <= 0 || d > 24*time.Hour {
		return fmt.Errorf("agent.max_duration must be greater than 0 and at most 24h (got %s)", d)
	}
	if err := CheckHostPorts("agent.host_ports", c.Agent.HostPorts); err != nil {
		return err
	}
	for _, p := range c.GitHub.ProtectedPatterns {
		if p == "" {
			return fmt.Errorf("protected_patterns: entries must not be empty")
		}
		if rest, ok := strings.CutPrefix(p, "refs/"); ok {
			return fmt.Errorf("protected_patterns: write %q, not %q", strings.TrimPrefix(rest, "heads/"), p)
		}
	}
	return nil
}

func (c Config) profileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Loc returns the configured IANA timezone.
func (c Config) Loc() (*time.Location, error) {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q (use an IANA name such as Europe/Paris): %w", c.Timezone, err)
	}
	return loc, nil
}

// DefaultPath is $XDG_CONFIG_HOME/jauntbox/config.yaml, falling back to
// ~/.config/jauntbox/config.yaml. It errors when neither is known, rather than
// returning a path relative to the current directory.
func DefaultPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jauntbox", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the default config path (%w); set XDG_CONFIG_HOME or pass --config", err)
	}
	return filepath.Join(home, ".config", "jauntbox", "config.yaml"), nil
}
