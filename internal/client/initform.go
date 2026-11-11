package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
)

// SSHKeys lists the ~/.ssh/*.pub files under home, sorted by name, except that
// the key Detect would propose (ed25519, then ecdsa, then rsa) comes first.
func SSHKeys(home string) []string {
	all, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
	sort.Strings(all)
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		first := filepath.Join(home, ".ssh", name)
		for i, p := range all {
			if p == first {
				return append([]string{p}, append(all[:i:i], all[i+1:]...)...)
			}
		}
	}
	return all
}

// defaultLocation is the form's location default: where the home volume
// already lives, else fsn1.
func defaultLocation(found bool, loc string) string {
	if found && loc != "" {
		return loc
	}
	return config.Default().Location
}

// volumeNote warns when the chosen location differs from the existing volume's.
func volumeNote(found bool, volLoc, chosen string) string {
	if !found || volLoc == "" || volLoc == chosen {
		return ""
	}
	return fmt.Sprintf("your home volume is in %s; volumes can't move, so the next up would fail", volLoc)
}

// typeLocations is the sorted union of the locations of the current types.
func typeLocations(types []provider.ServerType) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range types {
		if t.Deprecated {
			continue
		}
		for _, l := range t.Locations {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	sort.Strings(out)
	return out
}

// typeOptionLabel is one server-type line: name, size, shared or dedicated
// (the ccx family is dedicated), monthly price in the location.
func typeOptionLabel(t provider.ServerType, location string) string {
	kind := "shared"
	if strings.HasPrefix(t.Name, "ccx") {
		kind = "dedicated"
	}
	return fmt.Sprintf("%s  %d vCPU · %g GB  %s  €%.2f/mo", t.Name, t.Cores, t.MemoryGB, kind, t.PriceMonthly[location])
}

type typeOption struct{ Name, Label string }

// typeOptionsFor lists the types a profile can use in location: amd64, not
// deprecated, orderable there and priced, smallest memory first, then price.
func typeOptionsFor(types []provider.ServerType, location string) []typeOption {
	var cands []provider.ServerType
	for _, t := range types {
		if t.Deprecated || t.Arch != "amd64" {
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
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.MemoryGB != b.MemoryGB {
			return a.MemoryGB < b.MemoryGB
		}
		if pa, pb := a.PriceMonthly[location], b.PriceMonthly[location]; pa != pb {
			return pa < pb
		}
		return a.Name < b.Name
	})
	out := make([]typeOption, len(cands))
	for i, t := range cands {
		out[i] = typeOption{t.Name, typeOptionLabel(t, location)}
	}
	return out
}

// keyChoice is one entry of the ssh key select: the key text, or "" to paste.
type keyChoice struct{ label, key string }

// keyChoices reads the usable public keys under home.
func keyChoices(home string) []keyChoice {
	var out []keyChoice
	for _, p := range SSHKeys(home) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		k := strings.TrimSpace(string(b))
		if validSSHKey(k) != nil {
			continue
		}
		out = append(out, keyChoice{label: filepath.Base(p) + "  " + shortKey(k), key: k})
	}
	return out
}

func shortKey(k string) string {
	f := strings.Fields(k)
	if len(f) >= 3 {
		return f[0] + " … " + f[len(f)-1]
	}
	return f[0] + " …"
}

// checkWithSpinner runs f under a one-line live spinner on stderr.
func checkWithSpinner(label string, f func() error) error {
	steps := ui.NewLive(os.Stderr, time.Now, nil)
	defer steps.Close()
	st := steps.Start(label)
	err := f()
	if err != nil {
		st.Fail(errors.New("rejected"), "")
		return err
	}
	st.Done()
	return nil
}

var errCancelled = errors.New("init cancelled; nothing was written")

func run(ctx context.Context, f *huh.Form) error {
	if err := f.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) {
			return errCancelled
		}
		return err
	}
	return nil
}

// askToken asks for a token in a hidden field until check accepts it.
func askToken(ctx context.Context, title, what string, check func(string) error) (string, error) {
	desc := ""
	for {
		var tok string
		in := huh.NewInput().Title(title).Description(desc).EchoMode(huh.EchoModePassword).Value(&tok).
			Validate(func(s string) error { return validToken(strings.TrimSpace(s)) })
		if err := run(ctx, huh.NewForm(huh.NewGroup(in))); err != nil {
			return "", err
		}
		tok = strings.TrimSpace(tok)
		err := checkWithSpinner("Check "+what, func() error { return check(tok) })
		if err == nil {
			return tok, nil
		}
		desc = fmt.Sprintf("%s rejected: %v. Paste it again.", what, err)
	}
}

// InitForm is the interactive front-end of init: a sequence of huh screens
// that ends in the same ApplyInit as the plain wizard. Nothing is written
// before the final review screen is confirmed; quitting writes nothing.
func InitForm(ctx context.Context, det Detected, lister func(token string) provider.TypeLister, finder func(token string) provider.VolumeFinder, meshCheck func(apiKey string) error, path string, opts ...InitOption) (config.Config, error) {
	o := newInitOpts(opts)
	home, _ := os.UserHomeDir()
	a := InitAnswers{}

	if _, err := os.Stat(path); err == nil {
		var ok bool
		c := huh.NewConfirm().Title(fmt.Sprintf("%s already exists. Overwrite it?", path)).Affirmative("Overwrite").Negative("Keep it").Value(&ok)
		if err := run(ctx, huh.NewForm(huh.NewGroup(c))); err != nil {
			return config.Config{}, err
		}
		if !ok {
			return config.Config{}, fmt.Errorf("%s left unchanged (delete it or answer yes to start over)", path)
		}
	}

	// user
	a.User = det.User
	if err := run(ctx, huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("User name").Description("Names every resource; lowercase letters, digits, dashes").Value(&a.User).
			Validate(func(s string) error { return validUserName(strings.TrimSpace(s)) }),
	))); err != nil {
		return config.Config{}, err
	}
	a.User = strings.TrimSpace(a.User)

	// ssh key
	const paste = "\x00paste"
	choices := keyChoices(home)
	pick := paste
	var opt []huh.Option[string]
	for _, c := range choices {
		opt = append(opt, huh.NewOption(c.label, c.key))
	}
	if len(choices) > 0 {
		pick = choices[0].key
		for _, c := range choices {
			if c.key == det.SSHPublicKey {
				pick = c.key
			}
		}
		opt = append(opt, huh.NewOption("Paste another key…", paste))
		if err := run(ctx, huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("SSH public key").Options(opt...).Value(&pick),
		))); err != nil {
			return config.Config{}, err
		}
	}
	if pick == paste {
		keyTitle := "SSH public key"
		desc := "Paste the line from a ~/.ssh/*.pub file"
		if len(choices) == 0 {
			desc = "None found; in another terminal run `ssh-keygen -t ed25519`, then paste the contents of ~/.ssh/id_ed25519.pub"
		}
		var k string
		if err := run(ctx, huh.NewForm(huh.NewGroup(
			huh.NewInput().Title(keyTitle).Description(desc).Value(&k).
				Validate(func(s string) error { return validSSHKey(strings.TrimSpace(s)) }),
		))); err != nil {
			return config.Config{}, err
		}
		pick = strings.TrimSpace(k)
	}
	a.SSHPublicKey = pick

	// timezone
	if zones := ZoneNames(); len(zones) > 0 {
		a.Timezone = det.Timezone
		if a.Timezone == "" || !inList(zones, a.Timezone) {
			if validTimezone(det.Timezone) == nil {
				zones = append([]string{det.Timezone}, zones...)
			} else {
				a.Timezone = zones[0]
			}
		}
		if err := run(ctx, huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Timezone").Description("All limit times use it. Type / to filter.").
				Options(huh.NewOptions(zones...)...).Filtering(true).Height(10).Value(&a.Timezone),
		))); err != nil {
			return config.Config{}, err
		}
	} else {
		a.Timezone = det.Timezone
		if err := run(ctx, huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Timezone").Description("IANA name such as Europe/Paris; all limit times use it").Value(&a.Timezone).
				Validate(func(s string) error { return validTimezone(strings.TrimSpace(s)) }),
		))); err != nil {
			return config.Config{}, err
		}
		a.Timezone = strings.TrimSpace(a.Timezone)
	}

	// Hetzner token, checked by listing the server types
	var types []provider.ServerType
	var err error
	if a.HetznerToken, err = askToken(ctx, "Hetzner Cloud API token", "Hetzner token", func(tok string) error {
		var e error
		types, e = lister(tok).ServerTypes(ctx)
		return e
	}); err != nil {
		return config.Config{}, err
	}

	// location
	locs := typeLocations(types)
	if len(locs) == 0 {
		return config.Config{}, errors.New("Hetzner offers no server types for this project")
	}
	var volLoc string
	var volFound bool
	if finder != nil {
		if vf := finder(a.HetznerToken); vf != nil {
			// A failed lookup only loses the default; the volume check at `up` still applies.
			_ = checkWithSpinner("Look for your home volume", func() error {
				var e error
				volLoc, volFound, e = vf.FindVolume(ctx, "jauntbox-"+a.User+"-home")
				return e
			})
		}
	}
	a.Location = defaultLocation(volFound, volLoc)
	if !inList(locs, a.Location) {
		a.Location = locs[0]
	}
	if err := run(ctx, huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Location").Options(huh.NewOptions(locs...)...).Value(&a.Location).
			DescriptionFunc(func() string { return volumeNote(volFound, volLoc, a.Location) }, &a.Location),
	))); err != nil {
		return config.Config{}, err
	}

	// server types
	if a.Profiles, err = PickProfiles(types, a.Location); err != nil {
		return config.Config{}, err
	}
	topts := typeOptionsFor(types, a.Location)
	var hopts []huh.Option[string]
	for _, t := range topts {
		hopts = append(hopts, huh.NewOption(t.Label, t.Name))
	}
	sel := map[string]*string{}
	var fields []huh.Field
	for _, size := range []string{"small", "big", "huge"} {
		v := a.Profiles[size].ServerType
		sel[size] = &v
		fields = append(fields, huh.NewSelect[string]().Title("Server type: "+size).Options(hopts...).Value(sel[size]))
	}
	if err := run(ctx, huh.NewForm(huh.NewGroup(fields...))); err != nil {
		return config.Config{}, err
	}
	for size, v := range sel {
		a.Profiles[size] = config.Profile{ServerType: *v, Arch: "amd64"}
	}

	// Tailscale key
	if a.TailscaleKey, err = askToken(ctx, "Tailscale API key", "Tailscale API key", meshCheck); err != nil {
		return config.Config{}, err
	}

	// review
	cfg, err := configFrom(a)
	if err != nil {
		return cfg, err
	}
	a.SaveEnv, a.AddSSHConfig = true, true
	review := []huh.Field{
		huh.NewNote().Title("Review").Description(fmt.Sprintf("user %s · %s · %s\nsmall=%s big=%s huge=%s\nconfig: %s",
			cfg.User, cfg.Location, cfg.Timezone, a.Profiles["small"].ServerType, a.Profiles["big"].ServerType, a.Profiles["huge"].ServerType, path)),
		huh.NewConfirm().Title(fmt.Sprintf("Save the tokens to the env file %s (mode 0600)?", EnvFilePath(path))).Value(&a.SaveEnv),
	}
	if o.sshConfig != "" {
		review = append(review, huh.NewConfirm().Title(fmt.Sprintf("Add a jauntbox-%s block to %s?", cfg.User, o.sshConfig)).
			Description("Lets `herdr --remote` skip host-key prompts; the box is authenticated by Tailscale.").Value(&a.AddSSHConfig))
	} else {
		a.AddSSHConfig = false
	}
	if err := run(ctx, huh.NewForm(huh.NewGroup(review...))); err != nil {
		return cfg, err
	}
	return ApplyInit(path, a, opts...)
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
