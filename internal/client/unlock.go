package client

import (
	"context"
	"encoding/json"
	"fmt"
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
	payload, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	if _, err := d.Remote.RunInput(ctx, hostname(d.Cfg.User), payload, "sudo", "jauntctl", "secrets-load"); err != nil {
		return fmt.Errorf("load secrets on the box: %w", err)
	}
	return nil
}

// EnsureUnlocked pushes the secrets only when the box does not hold them
// (it was just created or rebooted).
func EnsureUnlocked(ctx context.Context, d Deps) error {
	if _, err := d.Remote.Run(ctx, hostname(d.Cfg.User), "sudo", "-n", "test", "-e", secretsMarker); err == nil {
		return nil
	}
	return PushSecrets(ctx, d)
}

// tryUnlock is EnsureUnlocked for commands whose main job must not fail
// because of it: a failure is a warning.
func tryUnlock(ctx context.Context, d Deps) {
	if err := EnsureUnlocked(ctx, d); err != nil {
		fmt.Fprintf(attachOut, "warning: could not unlock the box: %v\n", err)
	}
}
