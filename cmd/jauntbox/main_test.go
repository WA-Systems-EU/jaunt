package main

import (
	"bytes"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRootForBoxName(t *testing.T) {
	v, h := names(rootFor("/usr/local/bin/jauntctl"))
	for _, n := range []string{"supervise", "stop", "night-set", "box-status"} {
		if !v[n] {
			t.Errorf("jauntctl lacks %s", n)
		}
	}
	if v["up"] || h["up"] {
		t.Error("jauntctl must not have up")
	}
	v, h = names(rootFor("jauntbox"))
	if !v["up"] || !v["status"] {
		t.Error("jauntbox lacks up/status")
	}
	for _, n := range []string{"supervise", "stop", "night-set", "box-status"} {
		if v[n] || !h[n] {
			t.Errorf("%s should be hidden-only on the client (visible=%v hidden=%v)", n, v[n], h[n])
		}
	}
}

func TestJauntctlRejectsLaptopCommand(t *testing.T) {
	root := rootFor("jauntctl")
	root.SetArgs([]string{"up"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	want := "\"up\" is a laptop command: run `jauntbox up` on your laptop"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestClientHelpHidesBoxCommands(t *testing.T) {
	root := rootFor("jauntbox")
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
}

func TestSecretsAreClientOnly(t *testing.T) {
	for _, c := range newBoxCmd().Commands() {
		if c.Name() == "secrets" {
			t.Fatal("secrets registered on the box root")
		}
	}
}
