// Command jauntbox brings up and tears down a remote Hetzner workspace.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider/hetzner"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
	"github.com/WA-Systems-EU/jaunt/internal/session"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
)

func stateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "jauntbox")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "jauntbox")
}

// requireEnv returns the variable from the environment, else from the env file
// written by `jauntbox init` (envFile may be "").
func requireEnv(name, what, envFile string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	if envFile != "" {
		if vals, err := client.ReadEnvFile(envFile); err == nil && vals[name] != "" {
			return vals[name], nil
		}
	}
	return "", fmt.Errorf("%s is not set — export your %s (see jauntbox init)", name, what)
}

// spawner re-executes this binary detached (new session) with output sent to the push log.
func spawner(cfgPath string) func(args ...string) error {
	return func(args ...string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		dir := stateDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		logf, err := os.OpenFile(filepath.Join(dir, "push.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer logf.Close()
		argv := append([]string{"--config", cfgPath}, args...)
		c := exec.Command(exe, argv...)
		c.Stdin, c.Stdout, c.Stderr = nil, logf, logf
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			return err
		}
		return c.Process.Release()
	}
}

func loadDeps(cfgPath string) (client.Deps, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return client.Deps{}, err
	}
	hz, err := requireEnv(cfg.HetznerTokenEnv, "Hetzner API token", client.EnvFilePath(cfgPath))
	if err != nil {
		return client.Deps{}, err
	}
	ts, err := requireEnv(cfg.TailscaleAPIKeyEnv, "Tailscale API key", client.EnvFilePath(cfgPath))
	if err != nil {
		return client.Deps{}, err
	}
	return client.Deps{
		Cfg:          cfg,
		Policy:       policy.Local{Cfg: cfg},
		Prov:         hetzner.New(hz),
		Mesh:         mesh.NewTailscale(ts, "", nil),
		Remote:       remote.NewSSH(cfg.User),
		Events:       events.JSONLFile(filepath.Join(stateDir(), "events.jsonl")),
		Now:          time.Now,
		Spawn:        spawner(cfgPath),
		HetznerToken: hz,
	}, nil
}

// runInherited runs argv with this process's stdio and returns its exit code.
func runInherited(argv []string) (int, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

func newRootCmd() *cobra.Command {
	var cfgPath string
	boxSup := func() (*supervisor.Supervisor, error) {
		if os.Geteuid() != 0 {
			return nil, errors.New("run with sudo")
		}
		info, err := boxinfo.Read(boxinfo.Path)
		if err != nil {
			return nil, err
		}
		return supervisor.New(info), nil
	}
	supervise := &cobra.Command{
		Use:   "supervise",
		Short: "Run one supervisor pass (box side, invoked by the systemd timer)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := boxSup()
			if err != nil {
				return err
			}
			_, err = s.RunOnce(cmd.Context())
			return err
		},
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Delete this box now (box side)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := boxSup()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(s.Paths.StopRequest), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(s.Paths.StopRequest, nil, 0o644); err != nil {
				return err
			}
			v, err := s.RunOnce(cmd.Context())
			if errors.Is(err, supervisor.ErrBusy) {
				fmt.Fprintln(cmd.OutOrStdout(), "stop recorded; the running supervisor pass will act on it")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stop requested: %s\n", v.Reason)
			return nil
		},
	}
	var until string
	nightSet := &cobra.Command{
		Use:   "night-set --until <RFC3339>",
		Short: "Authorise a night session that keeps the box while agents run (box side)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := boxSup()
			if err != nil {
				return err
			}
			t, err := time.Parse(time.RFC3339, until)
			if err != nil {
				return fmt.Errorf("--until must be an RFC3339 time such as 2026-06-02T07:00:00+02:00: %w", err)
			}
			if err := supervisor.ValidateNightUntil(s.Now(), t); err != nil {
				return err
			}
			if err := supervisor.SaveNight(s.Paths.Night, t); err != nil {
				return err
			}
			_ = s.Events.Emit(events.Event{Time: s.Now().UTC(), Kind: events.NightAuthorised, Fields: map[string]string{"until": t.Format(time.RFC3339)}})
			fmt.Fprintf(cmd.OutOrStdout(), "night session authorised until %s\n", t.Format(time.RFC3339))
			return nil
		},
	}
	nightSet.Flags().StringVar(&until, "until", "", "end of the night session (RFC3339)")
	_ = nightSet.MarkFlagRequired("until")
	var asJSON bool
	boxStatus := &cobra.Command{
		Use:   "box-status",
		Short: "Show box facts, supervisor state and the current verdict (box side, read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := boxSup()
			if err != nil {
				return err
			}
			st, v, err := s.Preview(cmd.Context())
			if err != nil {
				return err
			}
			loc, err := time.LoadLocation(s.Info.Timezone)
			if err != nil {
				return err
			}
			dl, dlReason := lifecycle.Deadline(s.Info.UpAt, s.Info.Limits, loc)
			night, err := supervisor.LoadNight(s.Paths.Night)
			if err != nil {
				return err
			}
			actions := map[lifecycle.Action]string{lifecycle.Keep: "keep", lifecycle.StartWrapUp: "start-wrapup", lifecycle.Delete: "delete"}
			out := map[string]any{
				"box":      s.Info,
				"state":    st,
				"verdict":  map[string]string{"action": actions[v.Action], "reason": v.Reason},
				"deadline": map[string]any{"time": dl, "reason": dlReason},
			}
			if !night.IsZero() {
				out["night_until"] = night
			}
			if !asJSON {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s); deadline %s (%s)\n", s.Info.Hostname, actions[v.Action], v.Reason, dl.In(loc).Format(time.RFC3339), dlReason)
				return nil
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	boxStatus.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	root := &cobra.Command{
		Use:           "jauntbox",
		Short:         "On-demand remote workspace on Hetzner",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// Bare `jauntbox`: bring the box up, wait for it and attach to the persistent session.
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d, err := loadDeps(cfgPath)
			if err != nil {
				return err
			}
			return client.Attach(ctx, d, session.Herdr{}, runInherited, exec.LookPath)
		},
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultPath(), "path to the jauntbox config file")

	var wait bool
	up := &cobra.Command{
		Use:   "up [profile]",
		Short: "Create the box (or report the existing one) and install the supervisor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d, err := loadDeps(cfgPath)
			if err != nil {
				return err
			}
			profile := d.Cfg.DefaultProfile
			if len(args) == 1 {
				profile = args[0]
			}
			if wait {
				d = client.WithoutSpawn(d)
			}
			res, err := client.Up(ctx, d, profile)
			if err != nil && !res.Created {
				return err
			}
			if !res.Created {
				fmt.Fprintf(cmd.OutOrStdout(), "box %s already exists (%s)\n", res.Box.Name, res.Box.Status)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s (%s)\n", res.Box.Name, profile)
			if err != nil { // the box exists; report the follow-up problem as a warning
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
				if !wait {
					return nil
				}
			}
			if wait {
				fmt.Fprintln(cmd.OutOrStdout(), "waiting for the box to join the tailnet...")
				return client.PushBinary(ctx, d, client.DefaultPushWait)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installing the supervisor in the background; log: %s\n", filepath.Join(stateDir(), "push.log"))
			return nil
		},
	}
	up.Flags().BoolVar(&wait, "wait", false, "install the supervisor in the foreground and wait for it")

	push := &cobra.Command{
		Use:    "internal-push",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := loadDeps(cfgPath)
			if err != nil {
				return err
			}
			return client.PushBinary(cmd.Context(), d, client.DefaultPushWait)
		},
	}

	acl := &cobra.Command{
		Use:   "acl-snippet",
		Short: "Print the Tailscale ACL snippet to paste into your policy file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), mesh.ACLSnippet(cfg.User))
			return nil
		},
	}

	root.AddCommand(initCmd(&cfgPath), up, push, acl, supervise, stop, nightSet, boxStatus)
	root.AddCommand(clientOpsCmds(&cfgPath, loadDeps)...)
	return root
}

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "jauntbox:", err)
		os.Exit(1)
	}
}
