package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = "user: thomas\nssh_public_key: ssh-ed25519 AAAA\ntimezone: Europe/Paris\n"

func TestLoadOverlaysDefaults(t *testing.T) {
	c, err := Load(writeCfg(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.MaxUptime != Duration(10*time.Hour) {
		t.Errorf("MaxUptime = %v", time.Duration(c.Limits.MaxUptime))
	}
	if c.Limits.EveningCutoff != "19:00" {
		t.Errorf("EveningCutoff = %q", c.Limits.EveningCutoff)
	}
	if c.Profiles["small"].ServerType != "cpx42" {
		t.Errorf("small = %+v", c.Profiles["small"])
	}
	if c.Profiles["small"].Arch != "amd64" {
		t.Errorf("small arch = %q", c.Profiles["small"].Arch)
	}
}

func TestLoadProfileWithoutArchDefaultsAmd64(t *testing.T) {
	c, err := Load(writeCfg(t, minimal+"profiles:\n  tiny:\n    server_type: cpx11\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiles["tiny"].Arch != "amd64" {
		t.Errorf("tiny arch = %q", c.Profiles["tiny"].Arch)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no user", "ssh_public_key: k\ntimezone: Europe/Paris\n", "user is required"},
		{"no timezone", "user: thomas\nssh_public_key: k\n", "timezone is required"},
		{"bad timezone", "user: thomas\nssh_public_key: k\ntimezone: Mars/Base\n", "unknown timezone"},
		{"bad default profile", minimal + "default_profile: xl\n", `default_profile "xl" not in profiles`},
		{"arm64", minimal + "profiles:\n  small:\n    server_type: cax31\n    arch: arm64\n", `arch "arm64" not supported in v1 (amd64 only)`},
		{"bad cutoff", minimal + "limits:\n  evening_cutoff: \"25:00\"\n", "invalid evening_cutoff"},
		{"bad night", minimal + "limits:\n  night_hours: \"22:00\"\n", "invalid night_hours"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeCfg(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestUserIsDNSLabel(t *testing.T) {
	_, err := Load(writeCfg(t, "user: \"Thomas!\"\nssh_public_key: k\ntimezone: Europe/Paris\n"))
	if err == nil || !strings.Contains(err.Error(), "user must be lowercase letters, digits and dashes") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoc(t *testing.T) {
	c, err := Load(writeCfg(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	loc, err := c.Loc()
	if err != nil || loc.String() != "Europe/Paris" {
		t.Fatalf("loc = %v, %v", loc, err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got := DefaultPath(); got != "/x/cfg/jauntbox/config.yaml" {
		t.Errorf("got %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if got := DefaultPath(); got != "/h/.config/jauntbox/config.yaml" {
		t.Errorf("got %q", got)
	}
}

func TestLimitsJSONRoundTrip(t *testing.T) {
	in := Default().Limits
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{`"max_uptime":"10h0m0s"`, `"evening_cutoff":"19:00"`, `"idle_day":"2h0m0s"`, `"idle_night":"1h0m0s"`, `"night_hours":"22:00-07:00"`, `"human_active_window":"30m0s"`, `"wrapup_grace":"5m0s"`} {
		if !strings.Contains(s, k) {
			t.Errorf("json %s missing %s", s, k)
		}
	}
	var out Limits
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip: %+v != %+v", out, in)
	}
	var hand Limits
	if err := json.Unmarshal([]byte(`{"max_uptime":"15m"}`), &hand); err != nil || hand.MaxUptime != Duration(15*time.Minute) {
		t.Errorf("hand-edited: %+v, %v", hand, err)
	}
	if err := json.Unmarshal([]byte(`{"max_uptime":"bogus"}`), &hand); err == nil {
		t.Error("expected error for bogus duration")
	}
	if err := json.Unmarshal([]byte(`{"max_uptime":900}`), &hand); err == nil {
		t.Error("expected error for non-string duration")
	}
}

func TestDurationYAML(t *testing.T) {
	var l Limits
	if err := yaml.Unmarshal([]byte("idle_day: 2h\nwrapup_grace: 30m\n"), &l); err != nil {
		t.Fatal(err)
	}
	if l.IdleDay != Duration(2*time.Hour) || l.WrapUpGrace != Duration(30*time.Minute) {
		t.Errorf("%+v", l)
	}
	if err := yaml.Unmarshal([]byte("idle_day: nope\n"), &l); err == nil {
		t.Error("expected error")
	}
}
