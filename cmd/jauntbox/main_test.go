package main

import (
	"bytes"
	"errors"
	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/minter"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
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

func TestSecretsLoadRejectsOversizedInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	big := `{"files":{"a":"` + strings.Repeat("A", maxSecretsInput) + `"}}` // base64-ish filler
	err := loadSecretsFrom(strings.NewReader(big), dir)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("dir created for rejected input")
	}
}

func TestSecretsLoadLoadsBundleAndNeverEchoesInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := loadSecretsFrom(strings.NewReader(`{"files":{"agents.env":"U0VDUkVUVkFMVUU="}}`), dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "agents.env")); string(b) != "SECRETVALUE" {
		t.Fatalf("content = %q", b)
	}
	err := loadSecretsFrom(strings.NewReader(`{"files":{"agents.env":"U0VDUkVUVkFMVUU="} garbage`), dir)
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretsLoadIsBoxOnly(t *testing.T) {
	has := func(root *cobra.Command, name string) bool {
		for _, c := range root.Commands() {
			if c.Name() == name {
				return true
			}
		}
		return false
	}
	if !has(newBoxCmd(), "secrets-load") || has(newClientCmd(), "secrets-load") {
		t.Fatal("secrets-load must be on the jauntctl root only")
	}
	if !has(newClientCmd(), "unlock") || has(newBoxCmd(), "unlock") {
		t.Fatal("unlock must be on the jauntbox root only")
	}
}

func credRun(t *testing.T, in string, mint func(string) (forge.Token, error)) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := gitCredential("get", strings.NewReader(in), &out, &errb, mint)
	return out.String(), errb.String(), err
}

func TestGitCredentialGet(t *testing.T) {
	var got string
	out, _, err := credRun(t, "protocol=https\nhost=github.com\npath=o/r.git\n\n", func(r string) (forge.Token, error) {
		got = r
		return forge.Token{Value: "tok"}, nil
	})
	if err != nil || got != "o/r" || out != "username=x-access-token\npassword=tok\n" {
		t.Fatalf("repo=%q out=%q err=%v", got, out, err)
	}
}

func TestGitCredentialIgnoresOtherHosts(t *testing.T) {
	called := false
	mint := func(string) (forge.Token, error) { called = true; return forge.Token{}, nil }
	for _, in := range []string{"host=gitlab.com\npath=o/r\n", "host=github.com\n"} {
		out, e, err := credRun(t, in, mint)
		if err != nil || out != "" || e != "" || called {
			t.Fatalf("in=%q out=%q err=%v called=%v", in, out, err, called)
		}
	}
}

func TestGitCredentialNotInstalled(t *testing.T) {
	ne := &minter.NotInstalledError{Repo: "o/r", InstallURL: "https://github.com/apps/x/installations/new"}
	out, e, err := credRun(t, "host=github.com\npath=o/r\n", func(string) (forge.Token, error) { return forge.Token{}, ne })
	if !errors.Is(err, errCredential) || out != "quit=1\n" || strings.Count(e, "\n") != 1 || !strings.Contains(e, "https://github.com/apps/x/installations/new") {
		t.Fatalf("out=%q err=%q %v", out, e, err)
	}
}

func TestGitCredentialLocked(t *testing.T) {
	out, e, err := credRun(t, "host=github.com\npath=o/r\n", func(string) (forge.Token, error) { return forge.Token{}, vault.ErrLocked })
	if !errors.Is(err, errCredential) || out != "quit=1\n" || e != vault.ErrLocked.Error()+"\n" {
		t.Fatalf("err=%q %v", e, err)
	}
}

// TestRealGitStopsOnHelperFailure runs real git against a helper that behaves
// like a failing git-credential: git must stop with the helper's message and
// never fall through to a username prompt.
func TestRealGitStopsOnHelperFailure(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	script := "#!/bin/sh\ncat >/dev/null\necho 'the box is locked: test' >&2\nprintf 'quit=1\\n'\nexit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, []byte("[credential]\n\thelper = "+helper+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(gitBin, "credential", "fill")
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	c.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=o/r\n\n")
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err == nil {
		t.Fatalf("git succeeded: %q", out.String())
	}
	if !strings.Contains(errb.String(), "the box is locked: test") || strings.Contains(errb.String(), "could not read Username") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestGitCredentialStoreEraseSilent(t *testing.T) {
	for _, op := range []string{"store", "erase"} {
		var out, errb bytes.Buffer
		if err := gitCredential(op, strings.NewReader("host=github.com\npath=o/r\npassword=x\n"), &out, &errb, nil); err != nil || out.Len()+errb.Len() != 0 {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

func TestReposJSONShape(t *testing.T) {
	var b bytes.Buffer
	rs := []forge.Repo{{Owner: "o", Name: "r", DefaultBranch: "main", CloneURL: "https://github.com/o/r.git"}}
	if err := printRepos(&b, rs, true); err != nil {
		t.Fatal(err)
	}
	if b.String() != `[{"owner":"o","name":"r","default_branch":"main","clone_url":"https://github.com/o/r.git"}]`+"\n" {
		t.Fatalf("json = %s", b.String())
	}
	b.Reset()
	_ = printRepos(&b, rs, false)
	if b.String() != "o/r  (default branch main)\n" {
		t.Fatalf("human = %q", b.String())
	}
}

func TestMintAndReposAreBoxOnly(t *testing.T) {
	for _, n := range []string{"mint", "repos", "git-credential"} {
		for _, c := range newClientCmd().Commands() {
			if c.Name() == n {
				t.Errorf("%s on laptop root", n)
			}
		}
	}
}

func TestBoxAliasesIgnoreMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, name := range []string{"supervise", "box-status", "stop"} {
		root := newClientCmd()
		root.SetArgs([]string{name})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err != nil && strings.Contains(err.Error(), "default config path") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a client command still demands --config
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
