// Package vault is the box-side secret store: a root-only directory on tmpfs
// that unlock fills from the laptop. Nothing here ever touches the persistent
// disk, and a reboot empties it (the box is then "locked").
package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

// Dir is where the box keeps secrets (tmpfs under /run).
const Dir = "/run/jauntbox/secrets"

const marker = ".loaded"

// ErrLocked is returned when the secrets have not been (re)loaded since boot.
var ErrLocked = errors.New("the box is locked: its forge and agent credentials are not in memory (it was rebooted or never unlocked); run `jauntbox unlock` on your laptop")

// Load replaces the contents of dir with the bundle: directory 0700, files
// 0600, files not in the bundle removed, marker written last. The marker is
// removed first, so a crash part-way leaves the box locked, not half-loaded.
func Load(dir string, b secrets.Bundle) error {
	for name := range b.Files {
		if !secrets.NameRE.MatchString(name) {
			return fmt.Errorf("invalid secret name %q", name)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, marker)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if _, keep := b.Files[e.Name()]; !keep {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	for name, data := range b.Files {
		if err := writeFile(dir, name, data); err != nil {
			return err
		}
	}
	return writeFile(dir, marker, nil)
}

// writeFile writes <name>.tmp (0600) then renames it over name.
func writeFile(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, name+".tmp")
	_ = os.RemoveAll(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// Locked reports whether the secrets are absent (no marker).
func Locked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, marker))
	return err != nil
}

// Read returns the named secret, or ErrLocked while the box is locked.
func Read(dir, name string) ([]byte, error) {
	if !secrets.NameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid secret name %q", name)
	}
	if Locked(dir) {
		return nil, ErrLocked
	}
	return os.ReadFile(filepath.Join(dir, name))
}
