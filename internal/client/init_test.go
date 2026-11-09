package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

func fixtureTypes() []provider.ServerType {
	loc := []string{"fsn1", "nbg1"}
	pr := func(p float64) map[string]float64 { return map[string]float64{"fsn1": p, "nbg1": p} }
	return []provider.ServerType{
		{Name: "cx22", Cores: 2, MemoryGB: 4, Arch: "amd64", Locations: loc, PriceMonthly: pr(4)},
		{Name: "ccx23", Cores: 4, MemoryGB: 16, Arch: "amd64", Locations: loc, PriceMonthly: pr(40)}, // dedicated, pricier
		{Name: "cpx42", Cores: 8, MemoryGB: 16, Arch: "amd64", Locations: loc, PriceMonthly: pr(25)},
		{Name: "ccx33", Cores: 8, MemoryGB: 32, Arch: "amd64", Locations: loc, PriceMonthly: pr(60)},
		{Name: "ccx43", Cores: 16, MemoryGB: 64, Arch: "amd64", Locations: loc, PriceMonthly: pr(120)},
		{Name: "nopr64", Cores: 16, MemoryGB: 64, Arch: "amd64", Locations: loc}, // no price: skipped
		{Name: "old32", Cores: 4, MemoryGB: 32, Arch: "amd64", Locations: loc, Deprecated: true, PriceMonthly: pr(10)},
		{Name: "cax41", Cores: 16, MemoryGB: 32, Arch: "arm64", Locations: loc, PriceMonthly: pr(5)},
	}
}

func TestPickProfiles(t *testing.T) {
	got, err := PickProfiles(fixtureTypes(), "fsn1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"small": "cpx42", "big": "ccx33", "huge": "ccx43"}
	for k, v := range want {
		if got[k].ServerType != v || got[k].Arch != "amd64" {
			t.Errorf("%s = %+v, want %s amd64", k, got[k], v)
		}
	}
	_, err = PickProfiles(fixtureTypes()[:4], "fsn1")
	if err == nil || err.Error() != "no amd64 server type with >= 64 GB in fsn1" {
		t.Fatalf("err = %v", err)
	}
	if _, err = PickProfiles(fixtureTypes(), "hel1"); err == nil {
		t.Fatal("expected error for a location with no types")
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	must(t, os.WriteFile(filepath.Join(home, ".ssh", "id_rsa.pub"), []byte("ssh-rsa RSA\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 AAAA me@x\n"), 0o644))
	env := func(k string) string {
		if k == "USER" {
			return "Thomas.S"
		}
		return ""
	}
	d := Detect(home, env, func() string { return "Europe/Paris" })
	if d.User != "thomas-s" || d.SSHPublicKey != "ssh-ed25519 AAAA me@x" || d.Timezone != "Europe/Paris" {
		t.Fatalf("got %+v", d)
	}
	if u := cleanUser("--Éric  Le_Roi--"); u != "ric-le-roi" {
		t.Errorf("cleanUser = %q", u)
	}
	if u := cleanUser(strings.Repeat("a", 60)); len(u) != 40 {
		t.Errorf("len = %d", len(u))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type scripted struct {
	secrets map[string][]string // substring of question -> queued answers
	asks    map[string][]string // substring of question -> queued answers (else default)
	asked   []string
	confirm map[string]bool // substring -> answer; absent = default
}

func (s *scripted) Ask(q, def string) (string, error) {
	s.asked = append(s.asked, q)
	for k, v := range s.asks {
		if strings.Contains(q, k) && len(v) > 0 {
			s.asks[k] = v[1:]
			return v[0], nil
		}
	}
	return def, nil
}
func (s *scripted) Secret(q string) (string, error) {
	for k, v := range s.secrets {
		if strings.Contains(q, k) && len(v) > 0 {
			s.secrets[k] = v[1:]
			return v[0], nil
		}
	}
	return "", errors.New("no scripted secret for " + q)
}
func (s *scripted) Confirm(q string, def bool) (bool, error) {
	for k, v := range s.confirm {
		if strings.Contains(q, k) {
			return v, nil
		}
	}
	return def, nil
}

type fakeLister struct {
	err   error
	types []provider.ServerType
	token *string
}

func (f fakeLister) ServerTypes(context.Context) ([]provider.ServerType, error) {
	return f.types, f.err
}

func okLister(token string) provider.TypeLister {
	return fakeLister{types: fixtureTypes()}
}

var testDet = Detected{User: "thomas", SSHPublicKey: "ssh-ed25519 AAAA me@x", Timezone: "Europe/Paris"}

func TestInitWritesConfigWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jb", "config.yaml")
	p := &scripted{secrets: map[string][]string{"Hetzner": {"hz-SECRET-1"}, "Tailscale": {"ts-SECRET-2"}}}
	var out bytes.Buffer
	cfg, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil }, path, WithOutput(&out))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.User != "thomas" || loaded.Profiles["small"].ServerType != "cpx42" || cfg.Timezone != "Europe/Paris" {
		t.Fatalf("loaded %+v", loaded)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "SECRET") {
		t.Error("config contains a token")
	}
	// default answers write the env file 0600
	envPath := filepath.Join(filepath.Dir(path), "env")
	eb, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eb), "export HCLOUD_TOKEN='hz-SECRET-1'") || !strings.Contains(string(eb), "export TAILSCALE_API_KEY='ts-SECRET-2'") {
		t.Errorf("env file: %s", eb)
	}
	if est, _ := os.Stat(envPath); est.Mode().Perm() != 0o600 {
		t.Errorf("env mode %v", est.Mode().Perm())
	}
	vals, err := ReadEnvFile(envPath)
	if err != nil || vals["HCLOUD_TOKEN"] != "hz-SECRET-1" || vals["TAILSCALE_API_KEY"] != "ts-SECRET-2" {
		t.Errorf("ReadEnvFile = %v, %v", vals, err)
	}
	for _, want := range []string{"source " + envPath, "Access controls", "tag:jauntbox-thomas", "jauntbox up small", "jauntbox ssh"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInitPrintsExportsWhenEnvFileDeclined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := &scripted{secrets: map[string][]string{"Hetzner": {"hz-SECRET-1"}, "Tailscale": {"ts-SECRET-2"}}, confirm: map[string]bool{"env file": false}}
	var out bytes.Buffer
	if _, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil }, path, WithOutput(&out)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "env")); err == nil {
		t.Error("env file written despite decline")
	}
	if !strings.Contains(out.String(), "export HCLOUD_TOKEN=") || !strings.Contains(out.String(), "export TAILSCALE_API_KEY=") {
		t.Errorf("exports not printed:\n%s", out.String())
	}
}

func TestInitRejectsBadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := &scripted{secrets: map[string][]string{"Hetzner": {"bad1", "bad2"}, "Tailscale": {"x"}}}
	bad := func(string) provider.TypeLister { return fakeLister{err: errors.New("unauthorized")} }
	_, err := Init(context.Background(), p, testDet, bad, func(string) error { return nil }, path, WithOutput(&bytes.Buffer{}))
	if err == nil || err.Error() != "Hetzner token rejected: unauthorized" {
		t.Fatalf("err = %v", err)
	}
	if len(p.secrets["Hetzner"]) != 0 {
		t.Errorf("expected exactly two attempts, left %v", p.secrets["Hetzner"])
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("config written despite failure")
	}
}

func TestInitRejectsBadTailscaleKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := &scripted{secrets: map[string][]string{"Hetzner": {"h"}, "Tailscale": {"a", "b"}}}
	_, err := Init(context.Background(), p, testDet, okLister, func(string) error { return errors.New("403") }, path, WithOutput(&bytes.Buffer{}))
	if err == nil || err.Error() != "Tailscale API key rejected: 403" {
		t.Fatalf("err = %v", err)
	}
}

func TestInitExistingConfigNeedsConfirm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	must(t, os.WriteFile(path, []byte("user: keep\n"), 0o600))
	p := &scripted{} // default for overwrite is no
	_, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil }, path, WithOutput(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "left unchanged") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "user: keep\n" {
		t.Error("existing config modified")
	}
}

func TestInitReasksInvalidAnswersAndHintsKeygen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	det := Detected{User: "thomas", Timezone: ""}
	p := &scripted{
		secrets: map[string][]string{"Hetzner": {"h"}, "Tailscale": {"t"}},
		asks: map[string][]string{
			"SSH public key": {"", "not a key", "ssh-ed25519 AAAA me@x"},
			"Timezone":       {"Mars/Base", "Europe/Paris"},
			"User name":      {"Bad User", "thomas"},
		},
	}
	var out bytes.Buffer
	cfg, err := Init(context.Background(), p, det, okLister, func(string) error { return nil }, path, WithOutput(&out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timezone != "Europe/Paris" || cfg.User != "thomas" || cfg.SSHPublicKey != "ssh-ed25519 AAAA me@x" {
		t.Fatalf("cfg %+v", cfg)
	}
	hinted := false
	for _, q := range p.asked {
		if strings.Contains(q, "ssh-keygen -t ed25519") {
			hinted = true
		}
	}
	if !hinted {
		t.Errorf("no keygen hint in prompts: %v", p.asked)
	}
}

func TestInitRejectsTokenWithUnsafeChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	p := &scripted{secrets: map[string][]string{"Hetzner": {"a'b", "x\ny"}}}
	_, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil }, path, WithOutput(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "Hetzner token rejected") {
		t.Fatalf("err = %v", err)
	}
}
