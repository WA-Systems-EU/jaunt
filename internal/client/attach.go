package client

import (
	"context"
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
// failures. It returns when the session ends for any other reason.
func Attach(ctx context.Context, d Deps, sess session.Session,
	exec func(argv []string) (int, error), lookPath func(string) (string, error)) error {
	host := hostname(d.Cfg.User)
	res, err := Up(ctx, WithoutSpawn(d), d.Cfg.DefaultProfile)
	if err != nil && !res.Created {
		return err
	}
	if err != nil {
		fmt.Fprintln(attachOut, "warning:", err)
	}
	if res.Created {
		fmt.Fprintf(attachOut, "waiting for %s to join the tailnet…\n", host)
		if err := PushBinary(ctx, d, DefaultPushWait); err != nil {
			return err
		}
	} else if err := waitOnline(ctx, d, host); err != nil {
		return err
	}

	prepareWorkspaces(ctx, d, sess, host)

	_, lerr := lookPath("herdr")
	argv := sess.AttachCommand(host, d.Cfg.User, session.Name, lerr == nil)
	var first time.Time
	delay := reconnectFirst
	for {
		code, err := exec(argv)
		if err != nil {
			return fmt.Errorf("run %s: %w", argv[0], err)
		}
		if code != exitSSHFailure {
			return nil
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

func waitOnline(ctx context.Context, d Deps, host string) error {
	deadline := nowFn().Add(onlineWait)
	announced := false
	for {
		if online, err := d.Mesh.DeviceOnline(ctx, host); err == nil && online {
			return nil
		}
		if !announced {
			fmt.Fprintf(attachOut, "waiting for %s to come online…\n", host)
			announced = true
		}
		if !nowFn().Before(deadline) {
			return fmt.Errorf("%s exists but is not on the tailnet after %s (check `jauntbox status`)", host, onlineWait)
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
	if _, err := d.Remote.Run(ctx, host, sess.StartCommand(session.Name)...); err != nil {
		warn("starting the session server", err)
		return
	}
	listing, err := d.Remote.Run(ctx, host, sess.ListCommand(session.Name)...)
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
