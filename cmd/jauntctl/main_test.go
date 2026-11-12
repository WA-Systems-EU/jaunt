package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/minter"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// TestLaptopCommandOnBox: jauntctl has no laptop tree, yet a laptop command
// typed on the box still points at the laptop.
func TestLaptopCommandOnBox(t *testing.T) {
	want := "\"up\" is a laptop command: run `jauntbox up` on your laptop"
	if err := runBox(t, "up"); err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	want = `unknown command "nope" for "jauntctl"`
	if err := runBox(t, "nope"); err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestConfigFlagUnknownWithoutSudo(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	if err := runBox(t, "box-status", "--config", "/tmp/x"); err == nil || err.Error() != "unknown flag: --config" {
		t.Fatalf("err = %v", err)
	}
}

// has reports whether root has a direct subcommand called name.
func has(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}

func TestBoxRootCommands(t *testing.T) {
	root := newBoxCmd()
	for _, n := range []string{"supervise", "stop", "night-set", "box-status", "agent", "mint", "repos", "git-credential", "secrets-load"} {
		if !has(root, n) {
			t.Errorf("jauntctl lacks %s", n)
		}
	}
	for _, n := range []string{"up", "secrets", "unlock"} {
		if has(root, n) {
			t.Errorf("jauntctl must not have %s", n)
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

const pwBundle = `{"files":{"agents.env":"` + "Qk9YX1NVRE9fUEFTU1dPUkQ9aHVudGVyMgpPVEhFUj0xCg==" + `"}}` // BOX_SUDO_PASSWORD=hunter2\nOTHER=1\n

func TestSecretsLoadSetsPasswordNotStored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	var got [][]byte
	err := loadSecretsWith(strings.NewReader(pwBundle), dir, func() (string, error) { return "alice", nil },
		func(in []byte) error { got = append(got, append([]byte(nil), in...)); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "alice:hunter2\n" {
		t.Fatalf("chpasswd stdin = %q", got)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "BOX_SUDO_PASSWORD") {
			t.Fatalf("password leaked into vault file %s: %q", e.Name(), b)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "agents.env")); string(b) != "OTHER=1\n" {
		t.Fatalf("agents.env = %q", b)
	}
}

func TestSecretsLoadPasswordFailureHidesValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	err := loadSecretsWith(strings.NewReader(pwBundle), dir, func() (string, error) { return "alice", nil },
		func(in []byte) error { return chpasswdFailure(1, string(in)) })
	if err == nil || !strings.Contains(err.Error(), "setting the box user's sudo password failed (chpasswd exit 1)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks password: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "agents.env")); string(b) != "OTHER=1\n" {
		t.Fatalf("other secrets not loaded: %q", b)
	}
	if _, e := os.Stat(filepath.Join(dir, ".loaded")); e != nil {
		t.Fatalf("marker missing: %v", e)
	}
}

func TestSecretsLoadOnlyPasswordLoadsEmptyVault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	b64 := "Qk9YX1NVRE9fUEFTU1dPUkQ9aHVudGVyMgo=" // BOX_SUDO_PASSWORD=hunter2\n
	var got []byte
	err := loadSecretsWith(strings.NewReader(`{"files":{"agents.env":"`+b64+`"}}`), dir, func() (string, error) { return "alice", nil },
		func(in []byte) error { got = in; return nil })
	if err != nil || string(got) != "alice:hunter2\n" {
		t.Fatalf("err=%v got=%q", err, got)
	}
	if _, e := os.Stat(filepath.Join(dir, "agents.env")); e == nil {
		t.Fatal("empty agents.env should not be written")
	}
}

// chpasswdFailure is what a failing chpasswd looks like to the caller: a real
// *exec.ExitError wrapped in text that echoes the input line.
func chpasswdFailure(code int, echoed string) error {
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	return fmt.Errorf("chpasswd: bad line %q: %w", echoed, err)
}

func runBox(t *testing.T, args ...string) error {
	t.Helper()
	root := newBoxCmd()
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root.Execute()
}

func TestBoxCommandsIgnoreUserHomeUnderSudo(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "jauntbox")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("hostile: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH")) // restored after the pre-run hook rewrites it
	t.Setenv("SUDO_USER", "x")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	t.Setenv("SSL_CERT_FILE", filepath.Join(home, "ca.pem"))
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(home, "d.sock"))
	_ = runBox(t, "box-status") // fails (not root); the pre-run hook is what matters
	if got := os.Getenv("HOME"); got != "/root" {
		t.Errorf("HOME = %q, want /root", got)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "HTTPS_PROXY", "https_proxy", "SSL_CERT_FILE", "DOCKER_HOST"} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s = %q survived under sudo", k, v)
		}
	}
}

func TestEnvKeptWhenNotUnderSudo(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("HOME", "/home/someone")
	_ = runBox(t, "box-status")
	if got := os.Getenv("HOME"); got != "/home/someone" {
		t.Errorf("HOME = %q changed without sudo", got)
	}
}

func TestConfigFlagRefusedUnderSudo(t *testing.T) {
	t.Setenv("SUDO_USER", "x")
	want := "--config is not accepted under sudo"
	for _, args := range [][]string{
		{"--config", "/tmp/x", "box-status"},
		{"box-status", "--config", "/tmp/x"},
		{"--config=/tmp/x", "repos"},
		{"mint", "--config", "/tmp/x", "a/b"},
	} {
		if err := runBox(t, args...); err == nil || err.Error() != want {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestMintRejectsBadRepoArg(t *testing.T) {
	for _, arg := range []string{"../x", "a/b/c", "", "a", "/b", "a/", "./x", "x/..", "../..", "a/b c", "a/b\n", "a:b/c"} {
		err := validateRepoArg(arg)
		if err == nil {
			t.Errorf("%q accepted", arg)
		}
	}
	for _, arg := range []string{"o/r", "WA-Systems-EU/jaunt", "a_b/c.d-e", "o/.github"} {
		if err := validateRepoArg(arg); err != nil {
			t.Errorf("%q rejected: %v", arg, err)
		}
	}
	// through the command, before any forge access (no vault, not root: the
	// refusal must come first and not depend on either)
	for _, arg := range []string{"../x", "a/b/c", ""} {
		err := runBox(t, "mint", arg)
		if err == nil || !strings.Contains(err.Error(), "owner/name") {
			t.Errorf("mint %q: err = %v", arg, err)
		}
	}
}
