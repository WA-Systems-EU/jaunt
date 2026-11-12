package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/cli"
	"github.com/WA-Systems-EU/jaunt/internal/client"
)

func TestRequireEnvFallsBackToEnvFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(f, []byte("export JB_TEST_TOKEN='from-file'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JB_TEST_TOKEN", "")
	if v, err := requireEnv("JB_TEST_TOKEN", "x", f); err != nil || v != "from-file" {
		t.Fatalf("got %q, %v", v, err)
	}
	t.Setenv("JB_TEST_TOKEN", "from-env")
	if v, _ := requireEnv("JB_TEST_TOKEN", "x", f); v != "from-env" {
		t.Fatalf("env should win, got %q", v)
	}
	t.Setenv("JB_TEST_TOKEN", "")
	if _, err := requireEnv("JB_TEST_TOKEN", "x", filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunInheritedDropsHerdrEnv(t *testing.T) {
	t.Setenv("HERDR_SESSION", "local")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("JB_KEEP", "yes")
	t.Setenv("HERDR_CONFIG_PATH", "/my/config.toml")
	out := filepath.Join(t.TempDir(), "env.txt")
	code, err := runInherited([]string{"sh", "-c", "env > " + out})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if strings.Contains(s, "HERDR_SESSION") || strings.Contains(s, "HERDR_ENV") {
		t.Errorf("HERDR_* leaked to child:\n%s", s)
	}
	if !strings.Contains(s, "HERDR_CONFIG_PATH=/my/config.toml") {
		t.Errorf("user herdr settings dropped:\n%s", s)
	}
	if !strings.Contains(s, "JB_KEEP=yes") {
		t.Errorf("other vars dropped:\n%s", s)
	}
}

func TestRootHasNestedFlag(t *testing.T) {
	f := newClientCmd().Flags().Lookup("nested")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--nested flag missing or defaults on: %v", f)
	}
}

func names(c *cobra.Command) (visible, hidden map[string]bool) {
	visible, hidden = map[string]bool{}, map[string]bool{}
	for _, sub := range c.Commands() {
		if sub.Hidden {
			hidden[sub.Name()] = true
		} else {
			visible[sub.Name()] = true
		}
	}
	return
}

func TestClientHelpHidesBoxCommands(t *testing.T) {
	root := newClientCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"supervise", "box-status", "night-set"} {
		if strings.Contains(out.String(), n) {
			t.Errorf("client help mentions %s:\n%s", n, out.String())
		}
	}
}

func TestSecretsListNeverPrintsValues(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	run := func(in string, args ...string) (string, error) {
		root := newClientCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(in))
		root.SetArgs(append([]string{"--config", cfg}, args...))
		err := root.Execute()
		return out.String(), err
	}
	if _, err := run("sekrit-value-1\n", "secrets", "set", "CLAUDE_CODE_OAUTH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("sekrit-value-2\n", "secrets", "set", "OTHER_KEY"); err != nil {
		t.Fatal(err)
	}
	out, err := run("", "secrets", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sekrit") {
		t.Fatalf("list leaked a value:\n%s", out)
	}
	if !strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN") || !strings.Contains(out, "OTHER_KEY") {
		t.Fatalf("list missing keys:\n%s", out)
	}
	if out, err := run("x\n", "secrets", "set", "bad key"); err == nil || strings.Contains(out, "x\n") {
		t.Fatalf("bad key accepted: %v", err)
	}
	if _, err := run("\n", "secrets", "set", "EMPTY"); err == nil {
		t.Fatal("empty value accepted")
	}
	if _, err := run("\n", "secrets", "set", "BOX_SUDO_PASSWORD"); err == nil || !strings.Contains(err.Error(), "BOX_SUDO_PASSWORD") {
		t.Fatalf("empty box password: %v", err)
	}
}

func TestClientWithoutHomeDemandsConfig(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	root := newClientCmd()
	root.SetArgs([]string{"acl-snippet"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--config") {
		t.Errorf("acl-snippet err = %v", err)
	}
}

func TestPickOwner(t *testing.T) {
	choices := []client.OwnerChoice{{Login: "alice"}, {Login: "WA-Systems-EU", Org: true}}
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"\n", 1, false},
		{"", 1, false},
		{"1\n", 0, false},
		{"9\nx\n2\n", 1, false},
		{"x", 0, true},
	}
	for _, c := range cases {
		var out bytes.Buffer
		got, err := pickOwner(strings.NewReader(c.in), &out, choices, 1)
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("input %q: got %d, %v", c.in, got, err)
		}
		if !strings.Contains(out.String(), " * 2) WA-Systems-EU (organization)") || !strings.Contains(out.String(), "1) alice (personal account)") {
			t.Errorf("prompt:\n%s", out.String())
		}
	}
}

// TestLaptopCommandsMatchClient keeps cli.LaptopCommands (what jauntctl names
// as laptop commands) equal to the client root's visible commands.
func TestLaptopCommandsMatchClient(t *testing.T) {
	visible, _ := names(newClientCmd())
	listed := map[string]bool{}
	for _, n := range cli.LaptopCommands {
		listed[n] = true
		if !visible[n] {
			t.Errorf("cli.LaptopCommands has %q, which is not a visible jauntbox command", n)
		}
	}
	for n := range visible {
		if !listed[n] {
			t.Errorf("visible jauntbox command %q is missing from cli.LaptopCommands", n)
		}
	}
}

func TestNoBoxAliases(t *testing.T) {
	visible, hidden := names(newClientCmd())
	for _, n := range []string{"supervise", "mint", "box-status", "agent-net", "stop", "night-set", "repos", "git-credential", "secrets-load"} {
		if visible[n] || hidden[n] {
			t.Errorf("jauntbox still has the box command %q", n)
		}
	}
}

func TestLaptopAgentIsWrongSide(t *testing.T) {
	root := newClientCmd()
	root.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "c.yaml"), "agent", "--unattended", "x"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	want := "\"agent\" is a box command: run `jauntctl agent` on the box, in your repository's folder"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
