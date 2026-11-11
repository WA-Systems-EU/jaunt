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
	return readyLinesAt(cfg, upAt, time.Now(), user)
}

func readyLinesAt(cfg config.Config, upAt, now time.Time, user string) (string, []string) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.Local
	}
	dl, reason := lifecycle.Deadline(upAt, cfg.Limits, loc)
	reason = strings.ReplaceAll(reason, "-", " ")
	lines := []string{
		fmt.Sprintf("Deletes itself at %s (%s) or after %s idle.", dl.In(loc).Format("15:04"), reason, shortDuration(time.Duration(cfg.Limits.IdleDay))),
		"ssh " + hostnameFor(user),
	}
	return "Ready in " + ui.FormatDuration(now.Sub(upAt)) + ".", lines
}

// finishUp ends the stream with the Ready line; warned turns the final
// full stop into ", with warnings."
func finishUp(t *tracked, cfg config.Config, upAt, now time.Time) {
	summary, lines := readyLinesAt(cfg, upAt, now, cfg.User)
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
				handOff(ctx, spawn, steps, res)
				return err
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
				handOff(ctx, spawn, steps, res)
				return err
			}
			finishUp(steps, d.Cfg, startedAt, d.Now())
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
// Before the server exists nothing is spawned.
func handOff(ctx context.Context, spawn func(...string) error, steps ui.Steps, res client.UpResult) {
	if ctx.Err() == nil || (!res.Created && res.Box.Name == "") {
		return
	}
	if err := spawn("internal-push"); err != nil {
		steps.Note("could not start the background install: " + err.Error() + " (run `jauntbox internal-push`)")
		return
	}
	steps.Note("the box keeps getting ready in the background; log: " + filepath.Join(stateDir(), "push.log"))
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
