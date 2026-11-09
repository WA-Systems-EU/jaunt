package client

import (
	"context"
	"fmt"
	"os"
	"time"
)

var pollInterval = 10 * time.Second

var osExecutable = os.Executable

// DefaultPushWait is how long PushBinary waits for the box to join the tailnet.
const DefaultPushWait = 15 * time.Minute

const installCmd = "sudo install -m 0755 /tmp/jauntbox /usr/local/bin/jauntbox && sudo systemctl enable --now jauntbox-supervise.timer"

const supervisorCheck = "test -x /usr/local/bin/jauntbox && systemctl is-enabled --quiet jauntbox-supervise.timer"

// EnsureSupervisor makes sure an existing box runs the supervisor: if the
// binary or the enabled timer is missing (an interrupted first attach, or the
// box was created from another machine) it pushes the binary in the foreground.
// Without it the safety net would delete the box under a working human.
func EnsureSupervisor(ctx context.Context, d Deps) error {
	if _, err := d.Remote.Run(ctx, hostname(d.Cfg.User), supervisorCheck); err == nil {
		return nil
	}
	return PushBinary(ctx, d, DefaultPushWait)
}

// InstallSupervisor is the existing-box counterpart of what Up does on create:
// in the foreground (wait) it ensures the supervisor, else it spawns the
// background push.
func InstallSupervisor(ctx context.Context, d Deps, wait bool) error {
	if wait {
		return EnsureSupervisor(ctx, d)
	}
	if err := d.Spawn("internal-push"); err != nil {
		return fmt.Errorf("starting the supervisor install failed (run `jauntbox internal-push`): %w", err)
	}
	return nil
}

// PushBinary waits for the box to appear on the tailnet, copies the jauntbox
// binary to it and enables the supervisor.
func PushBinary(ctx context.Context, d Deps, wait time.Duration) error {
	if wait <= 0 {
		wait = DefaultPushWait
	}
	bin, err := BoxBinaryPath(d.Cfg)
	if err != nil {
		return err
	}
	host := hostname(d.Cfg.User)
	deadline := time.Now().Add(wait)
	var lastErr error
	for {
		online, err := d.Mesh.DeviceOnline(ctx, host)
		if err == nil && online {
			break
		}
		lastErr = err // transient API errors are retried until the deadline
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("%s did not join the tailnet within %s (check the box console; it will delete itself via the safety timer)", host, wait)
			if lastErr != nil {
				return fmt.Errorf("%s; last error: %w", msg, lastErr)
			}
			return fmt.Errorf("%s", msg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	// Right after the tailnet join the engineer user may not exist yet (dev-setup.sh
	// creates it), so ssh/scp fail until then: retry to the same deadline.
	for {
		err := d.Remote.Copy(ctx, host, bin, "/tmp/jauntbox")
		if err != nil {
			err = fmt.Errorf("copy binary: %w", err)
		} else if _, rerr := d.Remote.Run(ctx, host, installCmd); rerr != nil {
			err = fmt.Errorf("install binary: %w", rerr)
		} else {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s joined the tailnet but is not accepting ssh within %s: %w", host, wait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
