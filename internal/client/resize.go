package client

import (
	"context"
	"fmt"
)

// resizePushWait bounds the foreground push to the new box (var for tests).
var resizePushWait = DefaultPushWait

// Resize replaces the box with one of another profile. The home volume
// persists. Down waits until the old box is gone before Up creates the new one.
// The UpResult reports the new box (zero while Down or Up have not got that
// far), so a caller whose context was cancelled can tell whether it exists.
func Resize(ctx context.Context, d Deps, profile string) (UpResult, error) {
	pol, err := d.Policy.Load(ctx)
	if err != nil {
		return UpResult{}, fmt.Errorf("load policy: %w", err)
	}
	if _, ok := pol.Profiles[profile]; !ok {
		return UpResult{}, fmt.Errorf("unknown profile %q (nothing was changed)", profile)
	}
	if found, err := down(ctx, d, false); err != nil {
		return UpResult{}, err
	} else if !found {
		d.steps().Note("no box running; creating one")
	}
	// The new box is pushed in the foreground so the stream covers it all.
	res, upErr := Up(ctx, WithoutSpawn(d), profile)
	if upErr != nil && !res.Created {
		return res, upErr
	}
	if err := PushBinary(ctx, d, resizePushWait); err != nil {
		return res, err
	}
	if err := WaitDevTools(ctx, d); err != nil {
		return res, err
	}
	return res, upErr
}
