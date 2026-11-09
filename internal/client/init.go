package client

import (
	"context"
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

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

// Prompter is the interactive surface of the init wizard.
type Prompter interface {
	Ask(question, def string) (string, error)
	Secret(question string) (string, error)
	Confirm(question string, def bool) (bool, error)
}

// Detected holds the values init proposes to the user.
type Detected struct{ User, SSHPublicKey, Timezone string }

var safeToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var nonDNS = regexp.MustCompile(`[^a-z0-9-]`)
var dashes = regexp.MustCompile(`-+`)

// cleanUser makes a DNS-label-safe user name (lowercase, [a-z0-9-], max 40).
func cleanUser(s string) string {
	s = nonDNS.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(dashes.ReplaceAllString(s, "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// Detect guesses the user, SSH public key and timezone. localZone returns the
// local IANA zone name, or "" when unknown.
func Detect(home string, env func(string) string, localZone func() string) Detected {
	d := Detected{User: cleanUser(env("USER")), Timezone: localZone()}
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			d.SSHPublicKey = strings.TrimSpace(string(b))
			break
		}
	}
	return d
}

// LocalZone returns $TZ, else the IANA name behind the /etc/localtime link, else "".
func LocalZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if i := strings.Index(target, "zoneinfo/"); i >= 0 {
		return target[i+len("zoneinfo/"):]
	}
	return ""
}

// PickProfiles chooses small/big/huge: the smallest non-deprecated amd64 type
// available in location with at least 16/32/64 GB of memory, cheapest monthly
// price in that location first (then fewest GB, then name). Types without a
// price for the location are skipped.
func PickProfiles(types []provider.ServerType, location string) (map[string]config.Profile, error) {
	out := map[string]config.Profile{}
	for _, size := range []struct {
		name string
		gb   float64
	}{{"small", 16}, {"big", 32}, {"huge", 64}} {
		var cands []provider.ServerType
		for _, t := range types {
			if t.Deprecated || t.Arch != "amd64" || t.MemoryGB < size.gb {
				continue
			}
			if _, ok := t.PriceMonthly[location]; !ok {
				continue
			}
			for _, l := range t.Locations {
				if l == location {
					cands = append(cands, t)
					break
				}
			}
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("no amd64 server type with >= %g GB in %s", size.gb, location)
		}
		sort.Slice(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if pa, pb := a.PriceMonthly[location], b.PriceMonthly[location]; pa != pb {
				return pa < pb
			}
			if a.MemoryGB != b.MemoryGB {
				return a.MemoryGB < b.MemoryGB
			}
			return a.Name < b.Name
		})
		out[size.name] = config.Profile{ServerType: cands[0].Name, Arch: "amd64"}
	}
	return out, nil
}

// InitOption tunes Init.
type InitOption func(*initOpts)
type initOpts struct{ out io.Writer }

// WithOutput sets where Init prints instructions (default os.Stdout).
func WithOutput(w io.Writer) InitOption { return func(o *initOpts) { o.out = w } }

// EnvFilePath is the token env file that sits next to the config file.
func EnvFilePath(configPath string) string { return filepath.Join(filepath.Dir(configPath), "env") }

func askSecret(p Prompter, question string, check func(string) error, what string) (string, error) {
	var last error
	for range 2 {
		s, err := p.Secret(question)
		if err != nil {
			return "", err
		}
		s = strings.TrimSpace(s)
		if !safeToken.MatchString(s) {
			last = errors.New("it must be non-empty and contain only letters, digits, '_' and '-' (copy just the token)")
			continue
		}
		if last = check(s); last == nil {
			return s, nil
		}
	}
	return "", fmt.Errorf("%s rejected: %w", what, last)
}

// Init runs the setup wizard and writes the config (never the tokens) to path.
func Init(ctx context.Context, p Prompter, det Detected, lister func(token string) provider.TypeLister, meshCheck func(apiKey string) error, path string, opts ...InitOption) (config.Config, error) {
	o := initOpts{out: os.Stdout}
	for _, f := range opts {
		f(&o)
	}
	if _, err := os.Stat(path); err == nil {
		ok, err := p.Confirm(fmt.Sprintf("%s already exists. Overwrite it?", path), false)
		if err != nil {
			return config.Config{}, err
		}
		if !ok {
			return config.Config{}, fmt.Errorf("%s left unchanged (delete it or answer yes to start over)", path)
		}
	}
	cfg := config.Default()
	var err error
	if cfg.User, err = askValid(p, o.out, "User name (names every resource; lowercase letters, digits, dashes)", det.User, func(v string) error {
		if v == "" || cleanUser(v) != v {
			return fmt.Errorf("user must be lowercase letters, digits and dashes, up to 40 characters (got %q)", v)
		}
		return nil
	}); err != nil {
		return cfg, err
	}
	keyQ := "SSH public key (paste the line from ~/.ssh/*.pub)"
	if det.SSHPublicKey == "" {
		keyQ = "SSH public key: none found; in another terminal run `ssh-keygen -t ed25519`, then paste the contents of ~/.ssh/id_ed25519.pub"
	}
	if cfg.SSHPublicKey, err = askValid(p, o.out, keyQ, det.SSHPublicKey, validSSHKey); err != nil {
		return cfg, err
	}
	if cfg.Timezone, err = askValid(p, o.out, "Timezone (IANA name such as Europe/Paris; all limit times use it)", det.Timezone, func(v string) error {
		if v == "" {
			return errors.New("a timezone is required")
		}
		if _, e := time.LoadLocation(v); e != nil {
			return fmt.Errorf("unknown timezone %q (use an IANA name such as Europe/Paris)", v)
		}
		return nil
	}); err != nil {
		return cfg, err
	}
	var types []provider.ServerType
	hz, err := askSecret(p, "Hetzner Cloud API token (from a dedicated project, with Read & Write permission; input hidden)", func(tok string) error {
		var e error
		types, e = lister(tok).ServerTypes(ctx)
		return e
	}, "Hetzner token")
	if err != nil {
		return cfg, err
	}
	ts, err := askSecret(p, "Tailscale API key (input hidden)", meshCheck, "Tailscale API key")
	if err != nil {
		return cfg, err
	}
	if cfg.Profiles, err = PickProfiles(types, cfg.Location); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := writeFile0600(path, b); err != nil {
		return cfg, err
	}
	fmt.Fprintf(o.out, "\nWrote %s (profiles: small=%s, big=%s, huge=%s).\n", path,
		cfg.Profiles["small"].ServerType, cfg.Profiles["big"].ServerType, cfg.Profiles["huge"].ServerType)

	envPath := EnvFilePath(path)
	save, err := p.Confirm(fmt.Sprintf("Save the tokens to the env file %s (mode 0600)?", envPath), true)
	if err != nil {
		return cfg, err
	}
	hzLine := fmt.Sprintf("export %s='%s'", cfg.HetznerTokenEnv, hz)
	tsLine := fmt.Sprintf("export %s='%s'", cfg.TailscaleAPIKeyEnv, ts)
	if save {
		if err := writeFile0600(envPath, []byte(hzLine+"\n"+tsLine+"\n")); err != nil {
			return cfg, err
		}
		fmt.Fprintf(o.out, "Saved. jauntbox reads it automatically; to use the tokens in your shell too: source %s\n", envPath)
	} else {
		fmt.Fprintf(o.out, "Add these to your shell profile (they were not written anywhere):\n  %s\n  %s\n", hzLine, tsLine)
	}
	fmt.Fprintf(o.out, "\nOne more step, once: in the Tailscale admin console go to Access controls and merge this\ninto the policy. Also REMOVE or narrow the default allow-all rule, or the box can reach\neverything on your tailnet.\n\n%s\n\n", mesh.ACLSnippet(cfg.User))
	fmt.Fprintln(o.out, "Then:\n  jauntbox up small\n  jauntbox ssh")
	return cfg, nil
}

func writeFile0600(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // WriteFile keeps the mode of an existing file
}

// ReadEnvFile parses `export NAME='value'` lines written by Init.
func ReadEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `'"`)
	}
	return out, nil
}

// askValid asks until check accepts the answer.
func askValid(p Prompter, out io.Writer, q, def string, check func(string) error) (string, error) {
	for {
		v, err := p.Ask(q, def)
		if err != nil {
			return "", err
		}
		v = strings.TrimSpace(v)
		if err := check(v); err != nil {
			fmt.Fprintln(out, "  invalid:", err)
			continue
		}
		return v, nil
	}
}

func validSSHKey(v string) error {
	if strings.ContainsAny(v, "\r\n") || !(strings.HasPrefix(v, "ssh-") || strings.HasPrefix(v, "ecdsa-")) {
		return errors.New("expected a single line starting with ssh- or ecdsa- (the contents of a ~/.ssh/*.pub file)")
	}
	return nil
}
