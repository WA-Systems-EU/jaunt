package minter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/forge/github"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

type fakeForge struct {
	mu             sync.Mutex
	mints, revokes int
	failRevoke     map[string]bool
	notInstalled   bool
	revoked        []string
}

func (f *fakeForge) Repos(context.Context) ([]forge.Repo, error) { return nil, nil }
func (f *fakeForge) Mint(_ context.Context, repo string) (forge.Token, error) {
	if f.notInstalled {
		return forge.Token{}, forge.ErrNotInstalled
	}
	f.mu.Lock()
	f.mints++
	n := f.mints
	f.mu.Unlock()
	return forge.Token{Value: fmt.Sprintf("SECRET-TOKEN-%d", n), Repo: repo, ExpiresAt: clock.Add(time.Hour)}, nil
}
func (f *fakeForge) Revoke(_ context.Context, tok string) error {
	f.revokes++
	if f.failRevoke[tok] {
		return errors.New("boom")
	}
	f.revoked = append(f.revoked, tok)
	return nil
}
func (f *fakeForge) Guarantees() forge.Guarantees { return forge.Guarantees{} }
func (f *fakeForge) InstallURL() string           { return "https://example/install" }

type sink struct {
	mu sync.Mutex
	ev []events.Event
}

func (s *sink) Emit(e events.Event) error {
	s.mu.Lock()
	s.ev = append(s.ev, e)
	s.mu.Unlock()
	return nil
}

var clock = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, load bool, files ...string) (*Minter, *fakeForge, *sink) {
	t.Helper()
	clock = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sec := filepath.Join(t.TempDir(), "secrets")
	if load {
		b := secrets.Bundle{Files: map[string][]byte{}}
		for _, n := range files {
			b.Files[n] = []byte(`{"app_id":1,"slug":"x","owner":"o"}`)
		}
		if err := vault.Load(sec, b); err != nil {
			t.Fatal(err)
		}
	}
	ff := &fakeForge{failRevoke: map[string]bool{}}
	s := &sink{}
	return &Minter{
		SecretsDir: sec, CacheDir: filepath.Join(t.TempDir(), "tokens"),
		Now:      func() time.Time { return clock },
		NewForge: func(github.AppMeta, []byte) (forge.Forge, error) { return ff, nil },
		Events:   s,
	}, ff, s
}

func TestMintCachesUntilMargin(t *testing.T) {
	m, ff, _ := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	ctx := context.Background()
	if _, err := m.Mint(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Minute)
	if _, err := m.Mint(ctx, "o/r"); err != nil || ff.mints != 1 {
		t.Fatalf("mints=%d err=%v", ff.mints, err)
	}
	clock = clock.Add(21 * time.Minute) // +51 min: 9 min left < margin
	if _, err := m.Mint(ctx, "o/r"); err != nil || ff.mints != 2 {
		t.Fatalf("mints=%d err=%v", ff.mints, err)
	}
	fi, err := os.Stat(filepath.Join(m.CacheDir, "o__r.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v %v", fi, err)
	}
}

func TestMintLocked(t *testing.T) {
	m, ff, _ := setup(t, false)
	_, err := m.Mint(context.Background(), "o/r")
	if !errors.Is(err, vault.ErrLocked) || ff.mints != 0 {
		t.Fatalf("err=%v mints=%d", err, ff.mints)
	}
}

func TestMintNotSetUp(t *testing.T) {
	m, _, _ := setup(t, true, secrets.AppMetaFile)
	if _, err := m.Mint(context.Background(), "o/r"); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("err=%v", err)
	}
}

func TestMintNotInstalledNamesInstallURL(t *testing.T) {
	m, ff, _ := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	ff.notInstalled = true
	_, err := m.Mint(context.Background(), "o/r")
	if !errors.Is(err, forge.ErrNotInstalled) || !strings.Contains(err.Error(), "o/r is not installed on the jauntbox App: add it at https://example/install") {
		t.Fatalf("err=%v", err)
	}
}

func TestMintRejectsTraversal(t *testing.T) {
	m, ff, _ := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	for _, r := range []string{"../x/y", "o/..", "o/r/s", "o", "o/r;rm"} {
		if _, err := m.Mint(context.Background(), r); err == nil {
			t.Errorf("%q accepted", r)
		}
	}
	if ff.mints != 0 {
		t.Fatal("minted")
	}
}

func TestRevokeAllContinuesPastFailure(t *testing.T) {
	m, ff, s := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	ctx := context.Background()
	for _, r := range []string{"o/a", "o/b", "o/c"} {
		if _, err := m.Mint(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ff.failRevoke["SECRET-TOKEN-2"] = true
	n, err := m.RevokeAll(ctx)
	if err == nil || n != 2 || ff.revokes != 3 {
		t.Fatalf("n=%d err=%v revokes=%d", n, err, ff.revokes)
	}
	var left []os.DirEntry
	all, _ := os.ReadDir(m.CacheDir)
	for _, e := range all {
		if strings.HasSuffix(e.Name(), ".json") {
			left = append(left, e)
		}
	}
	if len(left) != 1 || left[0].Name() != "o__b.json" {
		t.Fatalf("left=%v", left)
	}
	counts := map[string]int{}
	for _, e := range s.ev {
		counts[e.Kind]++
	}
	if counts["token.revoked"] != 2 || counts["token.revoke_failed"] != 1 || counts["token.minted"] != 3 {
		t.Fatalf("events=%v", counts)
	}
}

func TestEventsNeverContainToken(t *testing.T) {
	m, _, s := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	if _, err := m.Mint(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RevokeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.ev) != 2 {
		t.Fatalf("events=%v", s.ev)
	}
	for _, e := range s.ev {
		if strings.Contains(fmt.Sprint(e), "SECRET-TOKEN") {
			t.Fatalf("token in event %v", e)
		}
	}
}

func TestConcurrentMintsSameRepo(t *testing.T) {
	m, ff, _ := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Mint(context.Background(), "o/r")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ff.mints != 1 {
		t.Fatalf("mints=%d, want 1 (callers share one token)", ff.mints)
	}
	ents, _ := os.ReadDir(m.CacheDir)
	var jsons int
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray temp %s", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".json") {
			jsons++
		}
	}
	if jsons != 1 {
		t.Fatalf("entries=%v", ents)
	}
}

func TestRevokeAllIgnoresStrayTempFiles(t *testing.T) {
	m, ff, _ := setup(t, true, secrets.AppKeyFile, secrets.AppMetaFile)
	if _, err := m.Mint(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(m.CacheDir, "o__r.json.123.tmp")
	if err := os.WriteFile(stray, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := m.RevokeAll(context.Background()); err != nil || n != 1 || ff.revokes != 1 {
		t.Fatalf("n=%d err=%v revokes=%d", n, err, ff.revokes)
	}
}
