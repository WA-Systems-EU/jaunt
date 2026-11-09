package client

import (
	"context"
	"fmt"
)

// Resize replaces the box with one of another profile. The home volume
// persists. Down waits until the old box is gone before Up creates the new one.
func Resize(ctx context.Context, d Deps, profile string) error {
	pol, err := d.Policy.Load(ctx)
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}
	if _, ok := pol.Profiles[profile]; !ok {
		return fmt.Errorf("unknown profile %q (nothing was changed)", profile)
	}
	if err := Down(ctx, d, false); err != nil {
		return err
	}
	_, err = Up(ctx, d, profile)
	return err
}
