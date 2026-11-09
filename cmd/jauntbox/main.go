// Command jauntbox brings up and tears down a remote Hetzner workspace.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider/hetzner"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
)

func stateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "jauntbox")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "jauntbox")
}

func requireEnv(name, what string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
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
	hz, err := requireEnv(cfg.HetznerTokenEnv, "Hetzner API token")
	if err != nil {
		return client.Deps{}, err
	}
	ts, err := requireEnv(cfg.TailscaleAPIKeyEnv, "Tailscale API key")
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

func newRootCmd() *cobra.Command {
	var cfgPath string
	root := &cobra.Command{
		Use:           "jauntbox",
		Short:         "On-demand remote workspace on Hetzner",
		SilenceUsage:  true,
		SilenceErrors: true,
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
			if err != nil {
				return err
			}
			if !res.Created {
				fmt.Fprintf(cmd.OutOrStdout(), "box %s already exists (%s)\n", res.Box.Name, res.Box.Status)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s (%s)\n", res.Box.Name, profile)
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

	root.AddCommand(up, push, acl)
	return root
}

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "jauntbox:", err)
		os.Exit(1)
	}
}
