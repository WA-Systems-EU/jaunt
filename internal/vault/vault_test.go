package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

func bundle(kv ...string) secrets.Bundle {
	b := secrets.Bundle{Files: map[string][]byte{}}
	for i := 0; i+1 < len(kv); i += 2 {
		b.Files[kv[i]] = []byte(kv[i+1])
	}
	return b
}

func TestLoadWritesPrivateFilesAndMarkerLast(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := Load(dir, bundle("agents.env", "A=1\n", "github-app.pem", "KEY")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", fi, err)
	}
	for name, want := range map[string]string{"agents.env": "A=1\n", "github-app.pem": "KEY"} {
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, fi, err)
		}
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Fatalf("%s content = %q", name, b)
		}
	}
	mi, err := os.Stat(filepath.Join(dir, ".loaded"))
	if err != nil || mi.Mode().Perm() != 0o600 {
		t.Fatalf("marker: %v %v", mi, err)
	}
	for _, name := range []string{"agents.env", "github-app.pem"} {
		fi, _ := os.Stat(filepath.Join(dir, name))
		if fi.ModTime().After(mi.ModTime()) {
			t.Errorf("%s written after the marker", name)
		}
	}
	if Locked(dir) {
		t.Fatal("locked after Load")
	}
}

func TestLoadFixesExistingDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir, bundle("a", "x")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestLoadRejectsBadNames(t *testing.T) {
	for _, n := range []string{"../x", ".loaded", "A", "a/b", ""} {
		dir := filepath.Join(t.TempDir(), "s")
		if err := Load(dir, bundle(n, "x")); err == nil {
			t.Errorf("name %q accepted", n)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("name %q: dir created despite rejection", n)
		}
	}
}

func TestLoadRemovesStaleFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := Load(dir, bundle("old", "1", "keep", "2")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir, bundle("keep", "3")); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if len(got) != 2 || got[0] != ".loaded" || got[1] != "keep" {
		t.Fatalf("entries = %v", got)
	}
	if b, _ := Read(dir, "keep"); string(b) != "3" {
		t.Fatalf("keep = %q", b)
	}
}

func TestLoadClearsMarkerFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := Load(dir, bundle("a", "1")); err != nil {
		t.Fatal(err)
	}
	// A bad bundle is rejected before anything changes...
	if err := Load(dir, bundle("A", "1")); err == nil {
		t.Fatal("bad bundle accepted")
	}
	if Locked(dir) {
		t.Fatal("rejected bundle must not lock the box")
	}
	// ...but a failure while writing leaves the box locked.
	// A non-empty directory in the way makes the rename of "b" fail.
	if err := os.MkdirAll(filepath.Join(dir, "b", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir, bundle("b", "2")); err == nil {
		t.Fatal("expected write failure")
	}
	if !Locked(dir) {
		t.Fatal("half-loaded box must be locked")
	}
}

func TestLockedWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	if !Locked(dir) {
		t.Fatal("empty dir should be locked")
	}
	if !Locked(filepath.Join(dir, "missing")) {
		t.Fatal("missing dir should be locked")
	}
}

func TestReadWhenLocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, "a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v", err)
	}
	if err := Load(dir, bundle("a", "x")); err != nil {
		t.Fatal(err)
	}
	if b, err := Read(dir, "a"); err != nil || string(b) != "x" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := Read(dir, "../a"); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestLockedMessage(t *testing.T) {
	want := "the box is locked: its forge and agent credentials are not in memory (it was rebooted or never unlocked); run `jauntbox unlock` on your laptop"
	if ErrLocked.Error() != want {
		t.Fatalf("message = %q", ErrLocked)
	}
}
