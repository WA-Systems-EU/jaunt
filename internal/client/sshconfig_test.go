package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSSHConfigCreatesFileAndDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")
	added, warn, err := EnsureSSHConfig(path, "alice")
	if err != nil || !added || warn != "" {
		t.Fatalf("added=%v err=%v", added, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != SSHConfigBlock("alice") {
		t.Errorf("content %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
}

func TestEnsureSSHConfigAppendsWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	orig := "Host other\n    User x"
	os.WriteFile(path, []byte(orig), 0o600)
	added, warn, err := EnsureSSHConfig(path, "alice")
	if err != nil || !added || warn != "" {
		t.Fatalf("added=%v err=%v", added, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), orig+"\n") || !strings.Contains(string(b), "Host jauntbox-alice\n") {
		t.Errorf("content %q", b)
	}
}

func TestEnsureSSHConfigSkipsExistingHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	orig := "Host jauntbox-alice\n    User alice\n    StrictHostKeyChecking no\n    UserKnownHostsFile /dev/null\n"
	os.WriteFile(path, []byte(orig), 0o600)
	added, warn, err := EnsureSSHConfig(path, "alice")
	if err != nil || added || warn != "" {
		t.Fatalf("added=%v err=%v", added, err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Errorf("rewritten: %q", b)
	}
}

func TestInitOffersSSHConfig(t *testing.T) {
	sec := map[string][]string{"Hetzner": {"h"}, "Tailscale": {"t"}}
	// accepted by default
	sshPath := filepath.Join(t.TempDir(), "ssh", "config")
	p := &scripted{secrets: sec}
	var out bytes.Buffer
	if _, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil },
		filepath.Join(t.TempDir(), "c.yaml"), WithOutput(&out), WithSSHConfig(sshPath)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(sshPath); !strings.Contains(string(b), "Host jauntbox-thomas") {
		t.Errorf("ssh config not appended: %q", b)
	}
	// declined: nothing written, block printed
	sshPath = filepath.Join(t.TempDir(), "ssh", "config")
	p = &scripted{secrets: map[string][]string{"Hetzner": {"h"}, "Tailscale": {"t"}}, confirm: map[string]bool{"jauntbox-thomas block": false}}
	out.Reset()
	if _, err := Init(context.Background(), p, testDet, okLister, func(string) error { return nil },
		filepath.Join(t.TempDir(), "c.yaml"), WithOutput(&out), WithSSHConfig(sshPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sshPath); err == nil {
		t.Error("ssh config written despite decline")
	}
	if !strings.Contains(out.String(), "Host jauntbox-thomas") {
		t.Errorf("block not printed:\n%s", out.String())
	}
}

func TestEnsureSSHConfigWarnsOnBareHostEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	orig := "Host jauntbox-alice\n    User alice\n"
	os.WriteFile(path, []byte(orig), 0o600)
	added, warn, err := EnsureSSHConfig(path, "alice")
	if err != nil || added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	if !strings.Contains(warn, "StrictHostKeyChecking") || !strings.Contains(warn, "UserKnownHostsFile") {
		t.Errorf("warning = %q", warn)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Errorf("rewritten: %q", b)
	}
}
