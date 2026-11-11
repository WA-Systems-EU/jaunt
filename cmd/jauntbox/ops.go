package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// clientOpsCmds returns the laptop-side commands that act on an existing box.
func clientOpsCmds(cfgPath *string, load func(string) (client.Deps, error)) []*cobra.Command {
	// withSteps is withDeps for commands that report through a step stream:
	// the stream is closed (a running step becomes "interrupted") before
	// cobra prints any returned error.
	withSteps := func(run func(cmd *cobra.Command, d client.Deps, steps *tracked, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cmd.SetContext(ctx)
			d, err := load(*cfgPath)
			if err != nil {
				return err
			}
			steps := track(stepsFor(cmd, plainFlag(cmd)))
			defer steps.Close()
			d.Steps = steps
			return run(cmd, d, steps, args)
		}
	}
	withDeps := func(run func(cmd *cobra.Command, d client.Deps, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cmd.SetContext(ctx)
			d, err := load(*cfgPath)
			if err != nil {
				return err
			}
			return run(cmd, d, args)
		}
	}

	var force bool
	down := &cobra.Command{
		Use:   "down",
		Short: "Delete the box (the home volume is kept)",
		Args:  cobra.NoArgs,
		RunE: withSteps(func(cmd *cobra.Command, d client.Deps, steps *tracked, _ []string) error {
			steps.Header("jauntbox down", d.Cfg.User)
			if err := client.Down(cmd.Context(), d, force); err != nil {
				return err
			}
			if !steps.ended.Load() { // Down says "no box running" itself
				steps.End(true, "Box deleted. The home volume is kept.")
			}
			return nil
		}),
	}
	down.Flags().BoolVar(&force, "force", false, "delete via the Hetzner API without asking the box to wrap up")

	var statusJSON bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the box, its tailnet presence and the supervisor's verdict",
		Args:  cobra.NoArgs,
		RunE: withDeps(func(cmd *cobra.Command, d client.Deps, _ []string) error {
			rep, err := client.Status(cmd.Context(), d)
			if err != nil {
				return err
			}
			return printStatus(cmd, d.Cfg, rep, statusJSON)
		}),
	}
	status.Flags().BoolVar(&statusJSON, "json", false, "print the box's raw status as JSON")

	var until string
	night := &cobra.Command{
		Use:   "night --until HH:MM",
		Short: "Authorise a night session until the next HH:MM (config timezone)",
		Args:  cobra.NoArgs,
		RunE: withDeps(func(cmd *cobra.Command, d client.Deps, _ []string) error {
			return client.Night(cmd.Context(), d, until)
		}),
	}
	night.Flags().StringVar(&until, "until", "", "end of the night session, HH:MM")
	_ = night.MarkFlagRequired("until")

	resize := &cobra.Command{
		Use:   "resize <profile>",
		Short: "Replace the box with another profile (the home volume persists)",
		Args:  cobra.ExactArgs(1),
		RunE: withSteps(func(cmd *cobra.Command, d client.Deps, steps *tracked, args []string) error {
			startedAt := d.Now()
			steps.Header("jauntbox", "resize "+resizeFrom(cmd.Context(), d)+" → "+args[0])
			if err := client.Resize(cmd.Context(), d, args[0]); err != nil {
				return err
			}
			finishUp(steps, d.Cfg, startedAt, d.Now())
			return nil
		}),
	}

	ssh := &cobra.Command{
		Use:                "ssh [ssh args...]",
		Short:              "Open an ssh session on the box",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag parsing is off so ssh options pass through; still honour --config.
			path := *cfgPath
			for i := 0; i < len(args); i++ {
				if args[i] == "--config" && i+1 < len(args) {
					path = args[i+1]
					args = append(args[:i:i], args[i+2:]...)
					break
				}
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			// Unlocking never blocks ssh: any problem is a warning.
			if d, err := load(path); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not unlock the box: %v\n", err)
			} else if err := client.EnsureUnlocked(cmd.Context(), d); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not unlock the box: %v\n", err)
			}
			bin, err := exec.LookPath("ssh")
			if err != nil {
				return errors.New("ssh not found in PATH; install an OpenSSH client")
			}
			argv := append([]string{"ssh"}, remote.HostKeyOpts...)
			argv = append(argv, cfg.User+"@jauntbox-"+cfg.User)
			argv = append(argv, args...)
			return syscall.Exec(bin, argv, os.Environ())
		},
	}
	return []*cobra.Command{down, status, night, resize, ssh}
}

// resizeFrom names the profile of the running box for the resize header: the
// profile whose server type it runs, else the server type itself, else "none".
func resizeFrom(ctx context.Context, d client.Deps) string {
	b, err := d.Prov.FindBox(ctx, map[string]string{"jauntbox": d.Cfg.User, "role": "box"})
	if err != nil || b == nil {
		return "(no box)"
	}
	for name, p := range d.Cfg.Profiles {
		if p.ServerType == b.ServerType {
			return name
		}
	}
	return b.ServerType
}

const (
	ansiTeal = "\x1b[38;2;79;194;185m"
	ansiRed  = "\x1b[38;2;255;106;92m"
	ansiDim  = "\x1b[38;2;111;129;150m"
	ansiOff  = "\x1b[0m"
)

// printStatus writes the status summary to stdout (or only the box's raw JSON
// with asJSON). Colour only where the live step list would have used it.
func printStatus(cmd *cobra.Command, cfg config.Config, rep client.StatusReport, asJSON bool) error {
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if rep.UnlockErr != "" {
		fmt.Fprintf(errw, "warning: could not unlock the box: %s\n", rep.UnlockErr)
	}
	if asJSON {
		if rep.Box == nil {
			fmt.Fprintln(errw, "no box running")
			return nil
		}
		if rep.Err != "" {
			fmt.Fprintln(errw, rep.Err)
		}
		if rep.BoxStatus != nil {
			var buf any
			if err := json.Unmarshal(rep.BoxStatus, &buf); err == nil {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(buf)
			}
		}
		return nil
	}
	if rep.Box == nil {
		fmt.Fprintln(out, "no box running")
		return nil
	}
	colour := ui.Live(out, plainFlag(cmd), os.Getenv, isTerminal)
	paint := func(c, s string) string {
		if !colour {
			return s
		}
		return c + s + ansiOff
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.Local
	}
	fmt.Fprintf(out, "%s  %s  %s\n", rep.Box.Name, rep.Box.Status, rep.Box.ServerType)
	fmt.Fprintln(out, paint(ansiDim, "created "+rep.Box.Created.In(loc).Format("2006-01-02 15:04")))
	if rep.Online {
		fmt.Fprintln(out, "tailnet  "+paint(ansiTeal, "✓"))
	} else {
		fmt.Fprintln(out, "tailnet  "+paint(ansiRed, "✕"))
	}
	if rep.BoxStatus != nil {
		if rep.Locked {
			fmt.Fprintln(out, "secrets  locked")
		} else {
			fmt.Fprintln(out, "secrets  unlocked")
		}
		var st struct {
			Verdict  struct{ Action, Reason string }
			Deadline struct {
				Time   time.Time
				Reason string
			}
		}
		if json.Unmarshal(rep.BoxStatus, &st) == nil && st.Verdict.Action != "" {
			line := "supervisor  " + st.Verdict.Action
			if st.Verdict.Reason != "" {
				line += " (" + st.Verdict.Reason + ")"
			}
			if !st.Deadline.Time.IsZero() {
				line += ", next deadline " + st.Deadline.Time.In(loc).Format("15:04") +
					" (" + strings.ReplaceAll(st.Deadline.Reason, "-", " ") + ")"
			}
			fmt.Fprintln(out, line)
		}
	}
	if rep.Note != "" {
		fmt.Fprintln(out, rep.Note)
	}
	if rep.Locked {
		fmt.Fprintln(out, vault.ErrLocked)
	}
	if rep.Err != "" {
		fmt.Fprintln(out, rep.Err)
	}
	return nil
}
