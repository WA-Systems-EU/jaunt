package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/release"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
	"github.com/WA-Systems-EU/jaunt/internal/version"
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
	return "cd / && install -m 0755 /tmp/jauntctl /usr/local/bin/jauntctl && ln -sf jauntctl /usr/local/bin/jauntbox && systemctl enable --now jauntbox-supervise.timer && " +
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

// supervisorCheck exits 0 when the supervisor is installed and enabled; its
// stdout is the installed jauntctl's --version line (empty when unreadable).
const supervisorCheck = "test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer && { /usr/local/bin/jauntctl --version 2>/dev/null || true; }"

// versionPlan compares this laptop's build with the version the box reports
// ("" = unknown). A release laptop reinstalls an unknown or different box
// version; a development build on either side is only warned about.
func versionPlan(laptop version.Build, box string) (reinstall bool, detail string, warn string) {
	switch {
	case laptop.Release():
		switch {
		case box == "":
			return true, "unknown → " + laptop.Version, ""
		case box == "dev":
			return false, "", "the box runs a development build; this laptop is " + laptop.Version
		case box != laptop.Version:
			return true, box + " → " + laptop.Version, ""
		}
	case box == "":
		return false, "", "the box's version is unknown; this laptop is a development build"
	case box != "dev":
		return false, "", "the box runs " + box + "; this laptop is a development build"
	}
	return false, "", ""
}

// Supervisor check retries: a box that has only just come online may refuse
// the first ssh, which must not be mistaken for a missing supervisor. Vars so
// tests run instantly.
var (
	checkAttempts = 3
	checkGap      = 2 * time.Second
)

// EnsureSupervisor makes sure an existing box runs the supervisor: if the
// binary or the enabled timer is missing (an interrupted first attach, or the
// box was created from another machine) it pushes the binary in the foreground.
// Without it the safety net would delete the box under a working human. The
// box has already been seen online and provisioned, so the push is one step.
func EnsureSupervisor(ctx context.Context, d Deps) error {
	ok, detail, err := supervisorInstalled(ctx, d)
	if err != nil {
		return err
	}
	if ok {
		tryUnlock(ctx, d)
		return nil
	}
	src, err := boxBinarySource(d)
	if err != nil {
		return err
	}
	install := d.steps().Start("Install supervisor")
	if detail != "" {
		install.Detail(detail)
	}
	return copyAndInstall(ctx, d, src, time.Now().Add(DefaultPushWait), DefaultPushWait, nil, install)
}

// notInstalled reports whether err is the remote check command itself failing
// (it ran and exited non-zero). ssh exits 255 for transport failures; a
// negative code means ssh was killed by a signal; anything that is not an exit
// status (a start failure) says nothing about the box. All of those are
// retried.
func notInstalled(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() >= 0 && ee.ExitCode() != exitSSHFailure
}

// supervisorInstalled reports whether the box already runs the supervisor at
// the laptop's version, recording the "Install supervisor" step as skipped
// when it does (with a warning when the versions cannot be reconciled). When
// the box needs a reinstall for its version it returns false and the detail to
// show on the step. A failing ssh is retried a few times; a clean "not
// installed" answer decides at once.
func supervisorInstalled(ctx context.Context, d Deps) (bool, string, error) {
	for attempt := 1; ; attempt++ {
		out, err := d.Remote.Run(ctx, hostname(d.Cfg.User), supervisorCheck)
		if err == nil {
			reinstall, detail, warn := versionPlan(d.build(), version.ParseLine(string(out)))
			if reinstall {
				return false, detail, nil
			}
			step := d.steps().Start("Install supervisor")
			if warn != "" {
				step.Warn(warn)
			}
			step.Skip("already running")
			return true, "", nil
		}
		if ctx.Err() != nil {
			return false, "", ctx.Err() // Ctrl-C killed ssh: not an answer about the box
		}
		if notInstalled(err) || attempt >= checkAttempts {
			return false, "", nil
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-time.After(checkGap):
		}
	}
}

// InstallSupervisor is the existing-box counterpart of what Up does on create:
// in the foreground (wait) it ensures the supervisor, else it spawns the
// background push.
func InstallSupervisor(ctx context.Context, d Deps, wait bool) error {
	if wait {
		return EnsureSupervisor(ctx, d)
	}
	// Already installed at this version: do not re-push, but still unlock.
	ok, _, err := supervisorInstalled(ctx, d)
	if err != nil {
		return err
	}
	if ok {
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

// PushBinary waits for the box to appear on the tailnet, copies the jauntctl
// binary to it and enables the supervisor.
func PushBinary(ctx context.Context, d Deps, wait time.Duration) error {
	if wait <= 0 {
		wait = DefaultPushWait
	}
	src, err := boxBinarySource(d)
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

	return copyAndInstall(ctx, d, src, deadline, wait, steps.Start("Box reachable"), nil)
}

// copyAndInstall copies the binary to the box and installs the supervisor,
// retrying until deadline. Right after the tailnet join dev-setup.sh may not
// have created the box user yet, so the runuser steps fail until then. A
// refused root ssh login is different (stale tailnet policy): it gets its own
// short budget and the first error is a warning while it retries. A box that
// does not answer ssh at all yet (ErrSSHUnreachable, common right after a
// resize while the new server boots) is just retried until deadline.
//
// reach is the "Box reachable" step, open until the copy works (nil when the
// box is already known to be reachable); install is the "Install supervisor"
// step, passed open for a re-push or started after the copy otherwise.
func copyAndInstall(ctx context.Context, d Deps, src boxSource, deadline time.Time, wait time.Duration, reach, install ui.Step) error {
	host := hostname(d.Cfg.User)
	fail := func(err error) error {
		if install != nil {
			install.Fail(err, "")
		} else {
			reach.Fail(err, "")
		}
		return err
	}
	var rootSince time.Time
	for {
		err := stageBinary(ctx, d, src)
		if err != nil {
			var fe *fatalStageError
			if errors.As(err, &fe) {
				return fail(err)
			}
			err = fmt.Errorf("copy binary: %w", err)
		} else {
			if install == nil {
				reach.Done()
				install = d.steps().Start("Install supervisor")
			}
			if _, rerr := rootRun(ctx, d, installCmd(d.Cfg.User)); rerr != nil {
				err = fmt.Errorf("install binary: %w", rerr)
			} else {
				install.Done()
				tryUnlock(ctx, d)
				return nil
			}
		}
		if errors.Is(err, ErrRootSSH) && !errors.Is(err, ErrSSHUnreachable) {
			if rootSince.IsZero() {
				rootSince = time.Now()
				msg := fmt.Sprintf("%v; retrying for up to %s", err, rootSSHWait)
				if install != nil {
					install.Warn(msg)
				} else {
					reach.Warn(msg)
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

// fatalStageError is a staging failure that retrying cannot fix: a checksum
// mismatch, an unsupported architecture, a release that does not exist.
type fatalStageError struct{ err error }

func (e *fatalStageError) Error() string { return e.err.Error() }
func (e *fatalStageError) Unwrap() error { return e.err }

// boxExit returns the exit status of a remote command that ran and failed.
func boxExit(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) && !errors.Is(err, ErrRootSSH) {
		return ee.ExitCode(), true
	}
	return 0, false
}

// stageBinary leaves the box's jauntctl at /tmp/jauntctl: a local file is
// copied; a release is downloaded by the box itself, and when the box cannot
// reach the download the laptop fetches it and copies it over. A checksum
// mismatch or an unsupported architecture is fatal, never retried.
func stageBinary(ctx context.Context, d Deps, src boxSource) error {
	if src.Version == "" {
		return rootCopy(ctx, d, src.Local, "/tmp/jauntctl")
	}
	base := release.BaseURL(d.releaseRoot(), src.Version)
	script, err := release.BoxFetchScript(base, src.Version)
	if err != nil {
		return &fatalStageError{err}
	}
	_, err = rootRun(ctx, d, "sh", "-c", script)
	if err == nil {
		return nil
	}
	code, ok := boxExit(err)
	if !ok {
		return err
	}
	switch code {
	case release.ExitChecksum:
		return &fatalStageError{fmt.Errorf("the box downloaded jauntctl %s but its checksum does not match (%s): %w", src.Version, base, release.ErrChecksum)}
	case release.ExitArch:
		return &fatalStageError{fmt.Errorf("the box has an unsupported architecture for jauntctl %s: %w", src.Version, err)}
	case release.ExitDownload:
		return fetchOnLaptop(ctx, d, src.Version, base, err)
	}
	return fmt.Errorf("fetching jauntctl %s on the box: %w", src.Version, err)
}

// fetchOnLaptop downloads the release on this machine and copies it to the box.
func fetchOnLaptop(ctx context.Context, d Deps, version, base string, boxErr error) error {
	out, err := rootRun(ctx, d, "uname", "-m")
	if err != nil {
		return err
	}
	arch, err := release.ArchFromUname(string(out))
	if err != nil {
		return &fatalStageError{err}
	}
	dir, err := os.MkdirTemp("", "jauntctl-release")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "jauntctl")
	hc := &http.Client{Timeout: 5 * time.Minute}
	if err := release.FetchBox(ctx, hc, base, version, arch, dest); err != nil {
		if errors.Is(err, release.ErrChecksum) {
			return &fatalStageError{err}
		}
		return &fatalStageError{fmt.Errorf("jauntctl %s could not be downloaded, neither by the box (%v) nor by this machine; is %s a published release? %w", version, boxErr, base, err)}
	}
	return rootCopy(ctx, d, dest, "/tmp/jauntctl")
}
