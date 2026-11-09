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
	for {
		online, err := d.Mesh.DeviceOnline(ctx, host)
		if err != nil {
			return fmt.Errorf("check tailnet for %s: %w", host, err)
		}
		if online {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not join the tailnet within %s (check the box console; it will delete itself via the safety timer)", host, wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	if err := d.Remote.Copy(ctx, host, bin, "/tmp/jauntbox"); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	if _, err := d.Remote.Run(ctx, host, installCmd); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}
