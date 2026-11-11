package client

import (
	"context"
	"fmt"
)

// resizePushWait bounds the foreground push to the new box (var for tests).
var resizePushWait = DefaultPushWait

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
	if found, err := down(ctx, d, false); err != nil {
		return err
	} else if !found {
		d.steps().Note("no box running; creating one")
	}
	// The new box is pushed in the foreground so the stream covers it all.
	res, upErr := Up(ctx, WithoutSpawn(d), profile)
	if upErr != nil && !res.Created {
		return upErr
	}
	if err := PushBinary(ctx, d, resizePushWait); err != nil {
		return err
	}
	return upErr
}
