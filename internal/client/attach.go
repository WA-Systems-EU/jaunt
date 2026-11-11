package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/session"
)

// Reconnect and wait tuning; variables so tests run instantly.
var (
	attachOut       io.Writer = os.Stderr
	reconnectFirst            = 2 * time.Second
	reconnectCap              = 30 * time.Second
	reconnectGiveUp           = 10 * time.Minute
	onlineWait                = 2 * time.Minute
	provisionPoll             = 10 * time.Second
	provisionWait             = 20 * time.Minute
	healthySession            = 30 * time.Second
	nowFn                     = time.Now
	sleepFn                   = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
)

// exitSSHFailure is ssh's exit status for a transport failure. herdr documents
// no distinct "connection lost" status for --remote, so only this one retries.
const exitSSHFailure = 255

var projectName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Attach brings the box up (idempotently), prepares one workspace per project
// in ~/src and attaches the persistent session, reattaching after transport
// failures. It returns when the session ends for any other reason. Inside a
// local herdr pane it refuses, before doing anything, unless nested is set.
func Attach(ctx context.Context, d Deps, sess session.Session, nested bool,
	exec func(argv []string) (int, error), lookPath func(string) (string, error)) error {
	if session.InsideHerdr() && !nested {
		return errors.New("you are inside a local herdr pane; attaching to the box's herdr session from here would nest herdr inside herdr.\n" +
			"Run it from a plain terminal, or use `jauntbox --nested` to attach anyway")
	}
	host := hostname(d.Cfg.User)
	res, err := Up(ctx, WithoutSpawn(d), d.Cfg.DefaultProfile)
	if err != nil && !res.Created {
		return err
	}
	// A non-nil err with Created set is a warning Up already put on its step.
	if res.Created {
		if err := PushBinary(ctx, d, DefaultPushWait); err != nil {
			return err
		}
	} else if err := waitOnline(ctx, d, host); err != nil {
		return err
	}

	if err := waitProvisioned(ctx, d, host); err != nil {
		return err
	}
	if !res.Created {
		if err := EnsureSupervisor(ctx, d); err != nil {
			return err
		}
	}
	prepareWorkspaces(ctx, d, sess, host)

	if session.InsideHerdr() {
		fmt.Fprintf(attachOut, "inside a local herdr pane: attaching to the box's %q session (local herdr pane variables are not passed on)\n", session.Name)
	}
	_, lerr := lookPath("herdr")
	argv := sess.AttachCommand(host, d.Cfg.User, session.Name, lerr == nil)
	var first time.Time
	delay := reconnectFirst
	for {
		started := nowFn()
		code, err := exec(argv)
		if err != nil {
			return fmt.Errorf("run %s: %w", argv[0], err)
		}
		if code != exitSSHFailure {
			return nil
		}
		if nowFn().Sub(started) > healthySession {
			// The session ran fine for a while: this is a fresh drop, not a continuing outage.
			first, delay = time.Time{}, reconnectFirst
		}
		if first.IsZero() {
			first = nowFn()
		}
		if nowFn().Sub(first) >= reconnectGiveUp {
			return fmt.Errorf("connection to %s lost for %s; giving up (run `jauntbox` to reattach, or `jauntbox status`)", host, reconnectGiveUp)
		}
		fmt.Fprintf(attachOut, "connection lost; reconnecting in %s…\n", delay)
		if err := sleepFn(ctx, delay); err != nil {
			return err
		}
		if delay *= 2; delay > reconnectCap {
			delay = reconnectCap
		}
	}
}

// waitProvisioned polls until dev-setup has installed herdr (it runs last),
// which also means the engineer user exists and ssh works.
func waitProvisioned(ctx context.Context, d Deps, host string) error {
	herdr := "/home/" + d.Cfg.User + "/.local/bin/herdr"
	deadline := nowFn().Add(provisionWait)
	st := d.steps().Start("Box provisioned")
	for {
		if _, err := d.Remote.Run(ctx, host, "test", "-x", herdr); err == nil {
			st.Done()
			return nil
		}
		if !nowFn().Before(deadline) {
			err := fmt.Errorf("%s is not provisioned after %s (herdr missing); look with `jauntbox ssh` and `cloud-init status --long`", host, provisionWait)
			st.Fail(err, "")
			return err
		}
		if err := sleepFn(ctx, provisionPoll); err != nil {
			return err
		}
	}
}

func waitOnline(ctx context.Context, d Deps, host string) error {
	deadline := nowFn().Add(onlineWait)
	st := d.steps().Start("Box online")
	for {
		if online, err := d.Mesh.DeviceOnline(ctx, host); err == nil && online {
			st.Done()
			return nil
		}
		if !nowFn().Before(deadline) {
			err := fmt.Errorf("%s exists but is not on the tailnet after %s (check `jauntbox status`)", host, onlineWait)
			st.Fail(err, "")
			return err
		}
		if err := sleepFn(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// prepareWorkspaces is best effort: failures warn and the attach goes on.
func prepareWorkspaces(ctx context.Context, d Deps, sess session.Session, host string) {
	user := d.Cfg.User
	out, err := d.Remote.Run(ctx, host, "ls", "-1d", "/home/"+user+"/src/*/")
	if err != nil {
		return // no ~/src or no projects
	}
	var projects []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := path.Base(strings.TrimRight(line, "/"))
		if !projectName.MatchString(p) {
			fmt.Fprintf(attachOut, "warning: skipping project %q (name has characters outside A-Za-z0-9._-)\n", p)
			continue
		}
		projects = append(projects, p)
	}
	if len(projects) == 0 {
		return
	}
	warn := func(what string, err error) { fmt.Fprintf(attachOut, "warning: %s: %v\n", what, err) }
	if _, err := d.Remote.Run(ctx, host, sess.StartCommand(user, session.Name)...); err != nil {
		warn("starting the session server", err)
		return
	}
	listing, err := d.Remote.Run(ctx, host, sess.ListCommand(user, session.Name)...)
	if err != nil {
		warn("listing workspaces", err)
		return
	}
	cmds, err := sess.EnsureWorkspaces(session.Name, user, projects, listing)
	if err != nil {
		warn("workspaces", err)
		return
	}
	for _, c := range cmds {
		if _, err := d.Remote.Run(ctx, host, c...); err != nil {
			warn("creating a workspace", err)
		}
	}
}
