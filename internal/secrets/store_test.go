package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "secrets")}
}

func TestDirFor(t *testing.T) {
	if got := DirFor("/a/b/config.yaml"); got != "/a/b/secrets" {
		t.Fatalf("got %q", got)
	}
}

func TestPutCreatesPrivateFiles(t *testing.T) {
	s := newStore(t)
	if err := s.Put(AppKeyFile, []byte("pem")); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(s.Dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", di, err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, AppKeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	got, err := s.Get(AppKeyFile)
	if err != nil || string(got) != "pem" {
		t.Fatalf("get: %q %v", got, err)
	}
	for _, bad := range []string{"../x", "a/b", "", "UPPER"} {
		if err := s.Put(bad, []byte("x")); err == nil {
			t.Errorf("Put(%q) should fail", bad)
		}
	}
	if _, err := s.Get("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get missing = %v", err)
	}
}

func TestSetEnvUpserts(t *testing.T) {
	s := newStore(t)
	if err := s.SetEnv("OTHER_KEY", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnv("CLAUDE_CODE_OAUTH_TOKEN", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnv("CLAUDE_CODE_OAUTH_TOKEN", "b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get(AgentEnvFile)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0] != "OTHER_KEY=keep" || lines[1] != "CLAUDE_CODE_OAUTH_TOKEN=b" {
		t.Fatalf("agents.env = %q", b)
	}
	keys, err := s.EnvKeys()
	if err != nil || len(keys) != 2 || keys[0] != "CLAUDE_CODE_OAUTH_TOKEN" || keys[1] != "OTHER_KEY" {
		t.Fatalf("keys = %v %v", keys, err)
	}
	if err := s.SetEnv("bad key", "x"); err == nil {
		t.Error("bad key accepted")
	}
	if err := s.SetEnv("OK_KEY", "a\nb"); err == nil {
		t.Error("newline value accepted")
	}
}

func TestEnvKeysMissingFile(t *testing.T) {
	keys, err := newStore(t).EnvKeys()
	if err != nil || len(keys) != 0 {
		t.Fatalf("%v %v", keys, err)
	}
}

func TestBundleRefusesOpenFile(t *testing.T) {
	s := newStore(t)
	if err := s.Put(AppKeyFile, []byte("pem")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.Dir, AppKeyFile)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Bundle()
	want := fmt.Sprintf("secret file %s is readable by others (mode 644); run chmod 600 %s", p, p)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestBundleContents(t *testing.T) {
	s := newStore(t)
	_ = s.Put(AppKeyFile, []byte("pem"))
	_ = s.SetEnv("K", "v")
	b, err := s.Bundle()
	if err != nil || string(b.Files[AppKeyFile]) != "pem" || string(b.Files[AgentEnvFile]) != "K=v\n" || len(b.Files) != 2 {
		t.Fatalf("%v %v", b, err)
	}
}

func TestBundleEmptyDir(t *testing.T) {
	b, err := newStore(t).Bundle()
	if err != nil || len(b.Files) != 0 {
		t.Fatalf("%v %v", b, err)
	}
}
