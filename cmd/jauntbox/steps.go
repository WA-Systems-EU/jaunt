package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
)

// isTerminal is a seam so tests can pretend stderr is a terminal.
var isTerminal = term.IsTerminal

// stepsFor builds the step stream of a command; tests replace it.
var stepsFor = newSteps

// newSteps returns the step stream for cmd: the live renderer on stderr when
// that is a terminal (and neither plain nor NO_COLOR/TERM=dumb say otherwise),
// else plain lines on stderr.
func newSteps(cmd *cobra.Command, plain bool) ui.Steps {
	w := cmd.ErrOrStderr()
	if ui.Live(w, plain, os.Getenv, isTerminal) {
		return ui.NewLive(w, time.Now, nil)
	}
	return ui.NewPlain(w, time.Now)
}

// plainFlag reads the root's persistent --plain flag.
func plainFlag(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("plain")
	return v
}

// tracked wraps a Steps to remember whether any step warned and whether the
// command already ended the stream itself.
type tracked struct {
	ui.Steps
	warned atomic.Bool
	ended  atomic.Bool
}

func track(s ui.Steps) *tracked { return &tracked{Steps: s} }

func (t *tracked) Start(label string) ui.Step {
	return &trackedStep{Step: t.Steps.Start(label), t: t}
}

func (t *tracked) End(ok bool, summary string, lines ...string) {
	t.ended.Store(true)
	t.Steps.End(ok, summary, lines...)
}

type trackedStep struct {
	ui.Step
	t *tracked
}

func (s *trackedStep) Warn(msg string) {
	s.t.warned.Store(true)
	s.Step.Warn(msg)
}

// readyLines is the closing text of up/resize/attach: the summary and the
// lines under it, with the time measured from upAt to now.
func readyLines(cfg config.Config, upAt time.Time, user string) (string, []string) {
	return readyLinesAt(cfg, cfg.Limits, upAt, time.Now(), upAt, user)
}

// readyLinesAt measures "Ready in" from elapsedFrom (the command start) and
// the deletion deadline from deadlineFrom (when the box itself came up), under
// the limits the supervisor enforces.
func readyLinesAt(cfg config.Config, lim config.Limits, elapsedFrom, now, deadlineFrom time.Time, user string) (string, []string) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.Local
	}
	dl, reason := lifecycle.Deadline(deadlineFrom, lim, loc)
	reason = strings.ReplaceAll(reason, "-", " ")
	dl, nowL := dl.In(loc), now.In(loc)
	day := func(t time.Time) string { // "" today, else yesterday/tomorrow/Mon 3 Jun
		y, m, d := t.Date()
		ny, nm, nd := nowL.Date()
		diff := int(time.Date(y, m, d, 12, 0, 0, 0, loc).Sub(time.Date(ny, nm, nd, 12, 0, 0, 0, loc)).Hours() / 24)
		switch {
		case diff == 0:
			return ""
		case diff == -1:
			return "yesterday"
		case diff == 1:
			return "tomorrow"
		}
		return t.Format("Mon 2 Jan")
	}
	var first string
	if dl.After(nowL) {
		at := dl.Format("15:04")
		if dd := day(dl); dd != "" {
			at = dd + " " + at
		}
		first = fmt.Sprintf("Deletes itself at %s (%s) or after %s idle.", at, reason, shortDuration(time.Duration(lim.IdleDay)))
	} else {
		at := dl.Format("15:04")
		if dd := day(dl); dd != "" {
			at += " " + dd
		}
		first = fmt.Sprintf("Past its deadline (%s, %s); deletes itself once you're idle for %s.", at, reason, shortDuration(time.Duration(lim.HumanActiveWindow)))
	}
	lines := []string{first, "ssh " + hostnameFor(user)}
	return "Ready in " + ui.FormatDuration(now.Sub(elapsedFrom)) + ".", lines
}

// finishUp ends the stream with the Ready line; warned turns the final
// full stop into ", with warnings."
func finishUp(ctx context.Context, t *tracked, d client.Deps, startedAt, boxUpAt time.Time) {
	lim := d.Cfg.Limits
	if pol, err := d.Policy.Load(ctx); err == nil { // what the supervisor enforces
		lim = pol.Limits
	}
	cfg := d.Cfg
	summary, lines := readyLinesAt(cfg, lim, startedAt, d.Now(), boxUpAt, cfg.User)
	if t.warned.Load() {
		summary = strings.TrimSuffix(summary, ".") + ", with warnings."
	}
	t.End(true, summary, lines...)
}

func hostnameFor(user string) string { return "jauntbox-" + user }

// upContext is the header context line of up: user, profile and where.
func upContext(cfg config.Config, profile string) string {
	p := profile
	if pr, ok := cfg.Profiles[profile]; ok && pr.ServerType != "" {
		p += " (" + pr.ServerType + ")"
	}
	return cfg.User + " · " + p + " · " + cfg.Location
}

// upCmd is `jauntbox up`: the box comes up in the foreground with a live step
// list; --detach hands the install to a background process instead.
func upCmd(cfgPath *string, load func(string) (client.Deps, error)) *cobra.Command {
	var wait, detach bool
	up := &cobra.Command{
		Use:   "up [profile]",
		Short: "Create the box (or find the existing one), install the supervisor and wait until it is ready",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			context.AfterFunc(ctx, stop) // a second Ctrl-C kills the process
			d, err := load(*cfgPath)
			if err != nil {
				return err
			}
			profile := d.Cfg.DefaultProfile
			if len(args) == 1 {
				profile = args[0]
			}
			steps := track(stepsFor(cmd, plainFlag(cmd)))
			defer steps.Close()
			d.Steps = steps
			startedAt := d.Now()
			steps.Header("jauntbox", upContext(d.Cfg, profile))
			steps.Plan("Policy and limits", "SSH key, firewall, volume")
			if detach {
				return upDetached(ctx, cmd, d, steps, profile)
			}
			spawn := d.Spawn // WithoutSpawn hides it from Up; Ctrl-C still needs it
			d = client.WithoutSpawn(d)
			res, err := client.Up(ctx, d, profile)
			if err != nil && !res.Created {
				return handOff(ctx, cmd, spawn, steps, res, err)
			}
			if err != nil { // the box exists; its step already carries the warning
				steps.warned.Store(true)
			}
			if res.Created {
				steps.Plan("Joined the tailnet", "Provisioning", "Install supervisor", "Unlock secrets")
				err = client.PushBinary(ctx, d, client.DefaultPushWait)
			} else {
				err = client.InstallSupervisor(ctx, d, true)
			}
			if err != nil {
				return handOff(ctx, cmd, spawn, steps, res, err)
			}
			boxUpAt := startedAt
			if !res.Created && !res.Box.Created.IsZero() {
				boxUpAt = res.Box.Created // a running box dies by its own clock
			}
			finishUp(ctx, steps, d, startedAt, boxUpAt)
			return nil
		},
	}
	up.Flags().BoolVar(&detach, "detach", false, "return once the box exists; install the supervisor in the background")
	up.Flags().BoolVar(&wait, "wait", false, "default; kept for scripts")
	_ = up.Flags().MarkHidden("wait")
	return up
}

// handOff is Ctrl-C after the server exists: the foreground push has returned
// (it was cancelled), so start exactly one background push to finish the job.
// The steps are closed first (the interrupted step line), then one note line is
// printed and the exit is reported without another message. Before the server
// exists nothing is spawned and err is returned as it is.
func handOff(ctx context.Context, cmd *cobra.Command, spawn func(...string) error, steps ui.Steps, res client.UpResult, err error) error {
	if ctx.Err() == nil || (!res.Created && res.Box.Name == "") {
		return err
	}
	steps.Close()
	if serr := spawn("internal-push"); serr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "could not start the background install: %v (run `jauntbox internal-push`)\n", serr)
		return errReported
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "the box keeps getting ready in the background; log: "+filepath.Join(stateDir(), "push.log"))
	return errReported
}

func upDetached(ctx context.Context, cmd *cobra.Command, d client.Deps, steps *tracked, profile string) error {
	res, err := client.Up(ctx, d, profile)
	if err != nil && !res.Created {
		return err
	}
	if !res.Created {
		if err := client.InstallSupervisor(ctx, d, false); err != nil {
			return err
		}
	}
	steps.Close()
	if err != nil { // the box exists; report the follow-up problem as a warning
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
		return nil
	}
	if !res.Created {
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installing the supervisor in the background; log: %s\n", filepath.Join(stateDir(), "push.log"))
	return nil
}
