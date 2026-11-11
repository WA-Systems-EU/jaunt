package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// clientOpsCmds returns the laptop-side commands that act on an existing box.
func clientOpsCmds(cfgPath *string, load func(string) (client.Deps, error)) []*cobra.Command {
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
		RunE: withDeps(func(cmd *cobra.Command, d client.Deps, _ []string) error {
			return client.Down(cmd.Context(), d, force)
		}),
	}
	down.Flags().BoolVar(&force, "force", false, "delete via the Hetzner API without asking the box to wrap up")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the box, its tailnet presence and the supervisor's verdict",
		Args:  cobra.NoArgs,
		RunE: withDeps(func(cmd *cobra.Command, d client.Deps, _ []string) error {
			rep, err := client.Status(cmd.Context(), d)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if rep.Box == nil {
				fmt.Fprintln(out, "no box running")
				return nil
			}
			fmt.Fprintf(out, "%s: %s, server type %s, created %s, on tailnet: %v\n",
				rep.Box.Name, rep.Box.Status, rep.Box.ServerType, rep.Box.Created.Format(time.RFC3339), rep.Online)
			if rep.Note != "" {
				fmt.Fprintln(out, rep.Note)
			}
			if rep.Locked {
				fmt.Fprintln(out, vault.ErrLocked)
			}
			if rep.Err != "" {
				fmt.Fprintln(out, rep.Err)
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
		}),
	}

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
		RunE: withDeps(func(cmd *cobra.Command, d client.Deps, args []string) error {
			return client.Resize(cmd.Context(), d, args[0])
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
