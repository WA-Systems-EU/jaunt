package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

const secretsMarker = "/run/jauntbox/secrets/.loaded"

// PushSecrets sends every laptop-side secret to the box's RAM (replacing what
// is there). The JSON travels on stdin only and is never logged or included
// in an error.
func PushSecrets(ctx context.Context, d Deps) error {
	b, err := d.Secrets.Bundle()
	if err != nil {
		return err
	}
	if len(b.Files) == 0 {
		return noSecretsError(d.Secrets.Dir)
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	if _, err := rootRunInput(ctx, d, payload, "jauntctl", "secrets-load"); err != nil {
		return fmt.Errorf("load secrets on the box: %w", err)
	}
	return nil
}

// EnsureUnlocked pushes the secrets only when the box does not hold them
// (it was just created or rebooted).
func EnsureUnlocked(ctx context.Context, d Deps) error {
	_, err := rootRun(ctx, d, "test", "-e", secretsMarker)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRootSSH) {
		return err // only a remote exit status means "locked"
	}
	return PushSecrets(ctx, d)
}

// noSecretsError explains why nothing is pushed: replacing the box's secrets
// with an empty bundle would wipe a working box.
func noSecretsError(dir string) error {
	return fmt.Errorf("no secrets stored on this laptop (%s): copy them from your other client or run `jauntbox setup github`", dir)
}

// tryUnlock is EnsureUnlocked for commands whose main job must not fail
// because of it: a failure is a warning.
func tryUnlock(ctx context.Context, d Deps) {
	if err := EnsureUnlocked(ctx, d); err != nil {
		fmt.Fprintf(attachOut, "warning: could not unlock the box: %v\n", err)
	}
}

// UnlockNote is the hint shown by unlock and status while the laptop holds no
// BOX_SUDO_PASSWORD (empty once one is set).
func UnlockNote(d Deps) string {
	keys, err := d.Secrets.EnvKeys()
	if err == nil {
		for _, k := range keys {
			if k == secrets.BoxSudoPassword {
				return ""
			}
		}
	}
	return "note: sudo on the box has no password yet; set one with `jauntbox secrets set BOX_SUDO_PASSWORD` and run `jauntbox unlock`"
}
