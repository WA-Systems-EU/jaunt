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
	"github.com/WA-Systems-EU/jaunt/internal/ui"
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

// InitOption tunes Init, InitForm and ApplyInit.
type InitOption func(*initOpts)
type initOpts struct {
	out       io.Writer
	sshConfig string
	steps     ui.Steps
}

// WithSSHConfig makes Init offer to append the host-key block to this ssh
// config file (default: no offer).
func WithSSHConfig(path string) InitOption { return func(o *initOpts) { o.sshConfig = path } }

// WithOutput sets where Init prints instructions (default os.Stdout).
func WithOutput(w io.Writer) InitOption { return func(o *initOpts) { o.out = w } }

// WithSteps sets where ApplyInit reports its writes (default: plain lines on
// the output writer).
func WithSteps(s ui.Steps) InitOption { return func(o *initOpts) { o.steps = s } }

func newInitOpts(opts []InitOption) initOpts {
	o := initOpts{out: os.Stdout}
	for _, f := range opts {
		f(&o)
	}
	return o
}

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
		if last = validToken(s); last != nil {
			continue
		}
		if last = check(s); last == nil {
			return s, nil
		}
	}
	return "", fmt.Errorf("%s rejected: %w", what, last)
}

// validToken checks the shape of a pasted API token.
func validToken(s string) error {
	if !safeToken.MatchString(s) {
		return errors.New("it must be non-empty and contain only letters, digits, '_' and '-' (copy just the token)")
	}
	return nil
}

// validUserName is the user-name rule shared by both init front-ends.
func validUserName(v string) error {
	if v == "" || cleanUser(v) != v {
		return fmt.Errorf("user must be lowercase letters, digits and dashes, up to 40 characters (got %q)", v)
	}
	return nil
}

// validTimezone is the timezone rule shared by both init front-ends.
func validTimezone(v string) error {
	if v == "" {
		return errors.New("a timezone is required")
	}
	if _, e := time.LoadLocation(v); e != nil {
		return fmt.Errorf("unknown timezone %q (use an IANA name such as Europe/Paris)", v)
	}
	return nil
}

// InitAnswers is everything init needs to write, whichever front-end asked.
type InitAnswers struct {
	User, SSHPublicKey, Timezone, Location string
	HetznerToken, TailscaleKey             string
	Profiles                               map[string]config.Profile
	SaveEnv, AddSSHConfig                  bool
}

// configFrom builds and validates the config the answers describe.
func configFrom(a InitAnswers) (config.Config, error) {
	cfg := config.Default()
	cfg.User, cfg.SSHPublicKey, cfg.Timezone = a.User, a.SSHPublicKey, a.Timezone
	if a.Location != "" {
		cfg.Location = a.Location
	}
	cfg.Profiles = a.Profiles
	return cfg, cfg.Validate()
}

// Init runs the line-by-line setup wizard (the plain front-end of init) and
// then writes everything through ApplyInit. Nothing is written before the last
// question is answered.
func Init(ctx context.Context, p Prompter, det Detected, lister func(token string) provider.TypeLister, meshCheck func(apiKey string) error, path string, opts ...InitOption) (config.Config, error) {
	o := newInitOpts(opts)
	if _, err := os.Stat(path); err == nil {
		ok, err := p.Confirm(fmt.Sprintf("%s already exists. Overwrite it?", path), false)
		if err != nil {
			return config.Config{}, err
		}
		if !ok {
			return config.Config{}, fmt.Errorf("%s left unchanged (delete it or answer yes to start over)", path)
		}
	}
	a := InitAnswers{Location: config.Default().Location}
	var err error
	if a.User, err = askValid(p, o.out, "User name (names every resource; lowercase letters, digits, dashes)", det.User, validUserName); err != nil {
		return config.Config{}, err
	}
	keyQ := "SSH public key (paste the line from ~/.ssh/*.pub)"
	if det.SSHPublicKey == "" {
		keyQ = "SSH public key: none found; in another terminal run `ssh-keygen -t ed25519`, then paste the contents of ~/.ssh/id_ed25519.pub"
	}
	if a.SSHPublicKey, err = askValid(p, o.out, keyQ, det.SSHPublicKey, validSSHKey); err != nil {
		return config.Config{}, err
	}
	if a.Timezone, err = askValid(p, o.out, "Timezone (IANA name such as Europe/Paris; all limit times use it)", det.Timezone, validTimezone); err != nil {
		return config.Config{}, err
	}
	var types []provider.ServerType
	if a.HetznerToken, err = askSecret(p, "Hetzner Cloud API token (from a dedicated project, with Read & Write permission; input hidden)", func(tok string) error {
		var e error
		types, e = lister(tok).ServerTypes(ctx)
		return e
	}, "Hetzner token"); err != nil {
		return config.Config{}, err
	}
	if a.TailscaleKey, err = askSecret(p, "Tailscale API key (input hidden)", meshCheck, "Tailscale API key"); err != nil {
		return config.Config{}, err
	}
	if a.Profiles, err = PickProfiles(types, a.Location); err != nil {
		return config.Config{}, err
	}
	cfg, err := configFrom(a)
	if err != nil {
		return cfg, err
	}
	envPath := EnvFilePath(path)
	if a.SaveEnv, err = p.Confirm(fmt.Sprintf("Save the tokens to the env file %s (mode 0600)?", envPath), true); err != nil {
		return cfg, err
	}
	if o.sshConfig != "" {
		if a.AddSSHConfig, err = p.Confirm(fmt.Sprintf("Add a jauntbox-%s block to %s (lets `herdr --remote` skip host-key prompts; the box is authenticated by Tailscale)?", cfg.User, o.sshConfig), true); err != nil {
			return cfg, err
		}
	}
	return ApplyInit(path, a, opts...)
}

// ApplyInit writes the config (never the tokens), the env file when SaveEnv,
// and the ssh config block when AddSSHConfig, reporting each as a step. It then
// prints the one step only the user can do: the Tailscale policy.
func ApplyInit(path string, a InitAnswers, opts ...InitOption) (config.Config, error) {
	o := newInitOpts(opts)
	cfg, err := configFrom(a)
	if err != nil {
		return cfg, err
	}
	steps := o.steps
	if steps == nil {
		steps = ui.NewPlain(o.out, time.Now)
	}
	defer steps.Close()
	fail := func(s ui.Step, err error) (config.Config, error) {
		s.Fail(err, "")
		return cfg, err
	}

	st := steps.Start("Write config")
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fail(st, err)
	}
	if err := writeFile0600(path, b); err != nil {
		return fail(st, err)
	}
	st.Detail(fmt.Sprintf("%s (profiles: small=%s, big=%s, huge=%s)", path,
		cfg.Profiles["small"].ServerType, cfg.Profiles["big"].ServerType, cfg.Profiles["huge"].ServerType))
	st.Done()

	envPath := EnvFilePath(path)
	hzLine := fmt.Sprintf("export %s='%s'", cfg.HetznerTokenEnv, a.HetznerToken)
	tsLine := fmt.Sprintf("export %s='%s'", cfg.TailscaleAPIKeyEnv, a.TailscaleKey)
	st = steps.Start("Write env file")
	if a.SaveEnv {
		if err := writeFile0600(envPath, []byte(hzLine+"\n"+tsLine+"\n")); err != nil {
			return fail(st, err)
		}
		st.Detail(envPath)
		st.Done()
		steps.Note(fmt.Sprintf("jauntbox reads it automatically; to use the tokens in your shell too: source %s", envPath))
	} else {
		st.Skip("not saved")
		steps.Note(fmt.Sprintf("Add these to your shell profile (they were not written anywhere):\n  %s\n  %s", hzLine, tsLine))
	}

	if o.sshConfig != "" {
		st = steps.Start("Add ssh config")
		if !a.AddSSHConfig {
			st.Skip("left unchanged")
			steps.Note(fmt.Sprintf("Add this to %s yourself:\n\n%s", o.sshConfig, SSHConfigBlock(cfg.User)))
		} else {
			added, warn, err := EnsureSSHConfig(o.sshConfig, cfg.User)
			if err != nil {
				return fail(st, err)
			}
			switch {
			case warn != "":
				st.Warn(warn)
				st.Done()
			case added:
				st.Detail(fmt.Sprintf("added the jauntbox-%s block to %s", cfg.User, o.sshConfig))
				st.Done()
			default:
				st.Skip(fmt.Sprintf("%s already has a Host jauntbox-%s entry; left unchanged", o.sshConfig, cfg.User))
			}
		}
	}
	steps.Close()
	fmt.Fprintf(o.out, "\nOne more step, once: in the Tailscale admin console go to Access controls and merge this\ninto the policy. Also REMOVE or narrow the default allow-all rule, or the box can reach\neverything on your tailnet.\n\n%s\n\n", mesh.ACLSnippet(cfg.User))
	fmt.Fprintln(o.out, "Ready. Next: jauntbox setup github, then jauntbox up")
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
	if strings.ContainsAny(v, "\r\n") || !(strings.HasPrefix(v, "ssh-") || strings.HasPrefix(v, "ecdsa-") || strings.HasPrefix(v, "sk-")) {
		return errors.New("expected a single line starting with ssh-, ecdsa- or sk- (the contents of a ~/.ssh/*.pub file)")
	}
	return nil
}
