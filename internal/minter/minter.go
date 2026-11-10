// Package minter is the box-side token issuer: it mints single-repository
// tokens from the GitHub App key held in the vault and caches them in RAM
// (root-only tmpfs) until shortly before they expire.
package minter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/forge/github"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// ReuseMargin is how long a cached token must still be valid to be reused.
const ReuseMargin = 10 * time.Minute

// ErrNotSetUp means the box is unlocked but holds no GitHub App.
var ErrNotSetUp = errors.New("GitHub is not set up: run `jauntbox setup github` on your laptop, then `jauntbox unlock`")

// NotInstalledError reports a repository the App is not installed on.
type NotInstalledError struct {
	Repo       string
	InstallURL string
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("%s is not installed on the jauntbox App: add it at %s, then re-run `jauntbox setup github` on your laptop", e.Repo, e.InstallURL)
}

// Is makes errors.Is(err, forge.ErrNotInstalled) hold.
func (e *NotInstalledError) Is(target error) bool { return target == forge.ErrNotInstalled }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Minter mints and caches repository tokens.
type Minter struct {
	SecretsDir string
	CacheDir   string
	Now        func() time.Time
	NewForge   func(meta github.AppMeta, key []byte) (forge.Forge, error)
	Events     events.Sink
}

// New returns a Minter with the production paths.
func New(ev events.Sink) *Minter {
	return &Minter{
		SecretsDir: vault.Dir,
		CacheDir:   "/run/jauntbox/tokens",
		Now:        time.Now,
		NewForge: func(meta github.AppMeta, key []byte) (forge.Forge, error) {
			return github.NewApp(meta, key, "")
		},
		Events: ev,
	}
}

type cached struct {
	Token     string    `json:"token"`
	Repo      string    `json:"repo"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (m *Minter) emit(kind, repo string, exp time.Time) {
	if m.Events == nil {
		return
	}
	f := map[string]string{"repo": repo}
	if !exp.IsZero() {
		f["expires_at"] = exp.UTC().Format(time.RFC3339)
	}
	_ = m.Events.Emit(events.Event{Time: m.Now().UTC(), Kind: kind, Fields: f})
}

// forge builds the forge from the vault: ErrLocked, ErrNotSetUp or a forge.
func (m *Minter) forge() (forge.Forge, error) {
	key, err := vault.Read(m.SecretsDir, secrets.AppKeyFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotSetUp
	}
	if err != nil {
		return nil, err
	}
	raw, err := vault.Read(m.SecretsDir, secrets.AppMetaFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotSetUp
	}
	if err != nil {
		return nil, err
	}
	var meta github.AppMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, errors.New("github-app.json is not valid; re-run `jauntbox setup github` on your laptop")
	}
	return m.NewForge(meta, key)
}

func (m *Minter) cachePath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !nameRE.MatchString(owner) || !nameRE.MatchString(name) || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", fmt.Errorf("invalid repository %q (want owner/name)", repo)
	}
	return filepath.Join(m.CacheDir, owner+"__"+name+".json"), nil
}

// Mint returns a token for repo (owner/name), reusing a cached one that is
// valid for more than ReuseMargin.
func (m *Minter) Mint(ctx context.Context, repo string) (forge.Token, error) {
	path, err := m.cachePath(repo)
	if err != nil {
		return forge.Token{}, err
	}
	f, err := m.forge()
	if err != nil {
		return forge.Token{}, err
	}
	if err := os.MkdirAll(m.CacheDir, 0o700); err != nil {
		return forge.Token{}, err
	}
	// Serialise callers for this repo so they share one token.
	unlock, err := lockFile(strings.TrimSuffix(path, ".json") + ".lock")
	if err != nil {
		return forge.Token{}, err
	}
	defer unlock()
	if b, err := os.ReadFile(path); err == nil {
		var c cached
		if json.Unmarshal(b, &c) == nil && c.Token != "" && c.ExpiresAt.Sub(m.Now()) > ReuseMargin {
			return forge.Token{Value: c.Token, Repo: c.Repo, ExpiresAt: c.ExpiresAt}, nil
		}
	}
	tok, err := f.Mint(ctx, repo)
	if err != nil {
		if errors.Is(err, forge.ErrNotInstalled) {
			ne := &NotInstalledError{Repo: repo}
			if u, ok := f.(interface{ InstallURL() string }); ok {
				ne.InstallURL = u.InstallURL()
			}
			return forge.Token{}, ne
		}
		return forge.Token{}, err
	}
	b, err := json.Marshal(cached{Token: tok.Value, Repo: tok.Repo, ExpiresAt: tok.ExpiresAt})
	if err != nil {
		return forge.Token{}, err
	}
	if err := writePrivate(path, b); err != nil {
		return forge.Token{}, err
	}
	m.emit("token.minted", repo, tok.ExpiresAt)
	return tok, nil
}

// lockFile takes an exclusive flock on path (created 0600) until unlock.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// writePrivate writes b to path atomically via a unique 0600 temp file.
func writePrivate(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Repos lists the repositories the App is installed on.
func (m *Minter) Repos(ctx context.Context) ([]forge.Repo, error) {
	f, err := m.forge()
	if err != nil {
		return nil, err
	}
	return f.Repos(ctx)
}

// RevokeAll revokes every cached token and deletes its file. It continues past
// failures and returns the first error. It returns the number revoked.
func (m *Minter) RevokeAll(ctx context.Context) (int, error) {
	ents, err := os.ReadDir(m.CacheDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var first error
	note := func(e error) {
		if first == nil {
			first = e
		}
	}
	var f forge.Forge
	var ferr error
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(m.CacheDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			note(err)
			continue
		}
		var c cached
		if err := json.Unmarshal(b, &c); err != nil {
			note(fmt.Errorf("%s: unreadable token cache entry", e.Name()))
			continue
		}
		if f == nil && ferr == nil {
			f, ferr = m.forge()
		}
		if ferr != nil {
			m.emit("token.revoke_failed", c.Repo, c.ExpiresAt)
			note(ferr)
			continue
		}
		if err := f.Revoke(ctx, c.Token); err != nil {
			m.emit("token.revoke_failed", c.Repo, c.ExpiresAt)
			note(fmt.Errorf("revoke token for %s: %w", c.Repo, err))
			continue
		}
		if err := os.Remove(path); err != nil {
			note(err)
		}
		_ = os.Remove(strings.TrimSuffix(path, ".json") + ".lock")
		m.emit("token.revoked", c.Repo, c.ExpiresAt)
		n++
	}
	return n, first
}
