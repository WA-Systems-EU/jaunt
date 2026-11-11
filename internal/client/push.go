package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/ui"
)

var pollInterval = 10 * time.Second

var osExecutable = os.Executable

// rootSSHWait bounds how long PushBinary keeps retrying a refused root ssh
// login. Unlike a box that is still provisioning, a tailnet policy that
// denies root will not fix itself, so fail soon and name the fix.
var rootSSHWait = 60 * time.Second

// DefaultPushWait is how long PushBinary waits for the box to join the tailnet.
const DefaultPushWait = 15 * time.Minute

// installCmd runs as root on the box. The git settings belong to the box user,
// so they run as that user.
func installCmd(user string) string {
	// cd / first: git exits 128 when the cwd (/root over ssh) is unreadable for the user.
	git := "runuser -u " + user + " -- git config --global "
	return "cd / && install -m 0755 /tmp/jauntbox /usr/local/bin/jauntctl && ln -sf jauntctl /usr/local/bin/jauntbox && systemctl enable --now jauntbox-supervise.timer && " +
		git + "--replace-all credential.https://github.com.helper '' && " +
		git + "--add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential' && " +
		git + "credential.https://github.com.useHttpPath true && " +
		// Never let a hostile repo's submodules or fsmonitor run code in the host user's git.
		git + "diff.ignoreSubmodules all && " +
		git + "status.submoduleSummary false && " +
		git + "submodule.recurse false && " +
		git + "core.fsmonitor false && " +
		// Best-effort and last: Docker may not be installed yet and older boxes lack the unit.
		// Enabling still links it for the next boot; every agent start re-applies the rules.
		"{ systemctl enable --now jauntbox-agent-net.service || true; }"
}

const supervisorCheck = "test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer"

// EnsureSupervisor makes sure an existing box runs the supervisor: if the
// binary or the enabled timer is missing (an interrupted first attach, or the
// box was created from another machine) it pushes the binary in the foreground.
// Without it the safety net would delete the box under a working human.
func EnsureSupervisor(ctx context.Context, d Deps) error {
	if supervisorInstalled(ctx, d) {
		tryUnlock(ctx, d)
		return nil
	}
	return PushBinary(ctx, d, DefaultPushWait) // unlocks after installing
}

// supervisorInstalled reports whether the box already runs the supervisor,
// recording the "Install supervisor" step as skipped when it does.
func supervisorInstalled(ctx context.Context, d Deps) bool {
	if _, err := d.Remote.Run(ctx, hostname(d.Cfg.User), supervisorCheck); err != nil {
		return false
	}
	d.steps().Start("Install supervisor").Skip("already running")
	return true
}

// InstallSupervisor is the existing-box counterpart of what Up does on create:
// in the foreground (wait) it ensures the supervisor, else it spawns the
// background push.
func InstallSupervisor(ctx context.Context, d Deps, wait bool) error {
	if wait {
		return EnsureSupervisor(ctx, d)
	}
	// Already installed: do not re-push over a running supervisor, but still unlock.
	if supervisorInstalled(ctx, d) {
		tryUnlock(ctx, d)
		return nil
	}
	if err := d.Spawn("internal-push"); err != nil {
		return fmt.Errorf("starting the supervisor install failed (run `jauntbox internal-push`): %w", err)
	}
	return nil
}

// neverJoined deletes a box that provably never appeared on the tailnet: it is
// unreachable, so nothing can use it, and leaving it would only bill.
func neverJoined(ctx context.Context, d Deps, host string, wait time.Duration) error {
	hint := "check cloud-init in the Hetzner console (/var/log/cloud-init-output.log) and the Tailscale ACL tag"
	box, err := d.Prov.FindBox(ctx, labels(d.Cfg.User, "box"))
	if err != nil {
		return fmt.Errorf("%s never joined the tailnet within %s and looking it up to delete it failed: %w (delete it by hand; %s)", host, wait, err, hint)
	}
	if box == nil {
		return fmt.Errorf("%s never joined the tailnet within %s (and no such server exists any more; %s)", host, wait, hint)
	}
	if err := d.Prov.DeleteBox(ctx, box.ID); err != nil {
		return fmt.Errorf("%s never joined the tailnet within %s and deleting it failed: %w (delete it by hand; %s)", host, wait, err, hint)
	}
	return fmt.Errorf("%s never joined the tailnet within %s; deleted it (%s)", host, wait, hint)
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
	steps := d.steps()
	host := hostname(d.Cfg.User)
	joined := steps.Start("Joined the tailnet")
	joined.Detail(host)
	deadline := time.Now().Add(wait)
	var lastErr error
	for {
		online, err := d.Mesh.DeviceOnline(ctx, host)
		if err == nil && online {
			break
		}
		lastErr = err // transient API errors are retried until the deadline
		if time.Now().After(deadline) {
			if lastErr != nil {
				// The mesh API itself is failing, so we cannot tell whether the box joined.
				err := fmt.Errorf("could not confirm that %s joined the tailnet within %s (Tailscale API error: %w); the box was NOT deleted: check `jauntbox status` and the Tailscale admin console, then re-run `jauntbox internal-push`", host, wait, lastErr)
				joined.Fail(err, "")
				return err
			}
			err := neverJoined(ctx, d, host, wait)
			joined.Fail(err, "")
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err() // left open: the caller closes the steps as interrupted
		case <-time.After(pollInterval):
		}
	}
	joined.Done()

	// Right after the tailnet join dev-setup.sh may not have created the box user
	// yet, so the runuser steps fail until then: retry those to the same deadline.
	// A refused root ssh login is different (stale tailnet policy): it gets its
	// own short budget and the first error is a warning while it retries.
	prov := steps.Start("Provisioning")
	var install ui.Step // started once the binary copy has worked
	fail := func(err error) error {
		if install != nil {
			install.Fail(err, "")
		} else {
			prov.Fail(err, "")
		}
		return err
	}
	var rootSince time.Time
	for {
		err := rootCopy(ctx, d, bin, "/tmp/jauntbox")
		if err != nil {
			err = fmt.Errorf("copy binary: %w", err)
		} else {
			if install == nil {
				prov.Done()
				install = steps.Start("Install supervisor")
			}
			if _, rerr := rootRun(ctx, d, installCmd(d.Cfg.User)); rerr != nil {
				err = fmt.Errorf("install binary: %w", rerr)
			} else {
				install.Done()
				tryUnlock(ctx, d)
				return nil
			}
		}
		if errors.Is(err, ErrRootSSH) {
			if rootSince.IsZero() {
				rootSince = time.Now()
				if install != nil {
					install.Warn(fmt.Sprintf("%v; retrying for up to %s", err, rootSSHWait))
				} else {
					prov.Warn(fmt.Sprintf("%v; retrying for up to %s", err, rootSSHWait))
				}
			}
			if time.Since(rootSince) > rootSSHWait {
				return fail(err)
			}
		} else {
			rootSince = time.Time{}
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("%s joined the tailnet but is not accepting ssh within %s: %w", host, wait, err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err() // left open: the caller closes the steps as interrupted
		case <-time.After(pollInterval):
		}
	}
}
