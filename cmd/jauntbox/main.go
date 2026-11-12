// Command jauntbox is the laptop client: it brings up, attaches to and tears
// down a remote Hetzner workspace. The box side is cmd/jauntctl.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/cli"
	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider/hetzner"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/session"
	"github.com/WA-Systems-EU/jaunt/internal/version"
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
		Root:         remote.NewSSH("root"),
		Events:       events.JSONLFile(filepath.Join(stateDir(), "events.jsonl")),
		Secrets:      secrets.Store{Dir: secrets.DirFor(cfgPath)},
		Now:          time.Now,
		Spawn:        spawner(cfgPath),
		HetznerToken: hz,
	}, nil
}

// runInherited runs argv with this process's stdio and returns its exit code.
func runInherited(argv []string) (int, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = session.WithoutHerdrEnv(os.Environ()) // the box's session is not a nested local one
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// newClientCmd is the jauntbox (laptop) root.
func newClientCmd() *cobra.Command { return newClientCmdWith(loadDeps) }

// newClientCmdWith is newClientCmd with the dependency loader injected.
func newClientCmdWith(load func(string) (client.Deps, error)) *cobra.Command {
	var cfgPath string
	var plain bool
	var nested bool
	defaultCfg, defaultCfgErr := config.DefaultPath()
	root := &cobra.Command{
		Use:           "jauntbox",
		Short:         "On-demand remote workspace on Hetzner",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Version:       version.Current().Version,
		// Without a home directory there is no default config path: demand --config.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if defaultCfgErr != nil && !cmd.Flags().Changed("config") {
				return defaultCfgErr
			}
			return nil
		},
		// Bare `jauntbox`: bring the box up, wait for it and attach to the persistent session.
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			context.AfterFunc(ctx, stop) // a second Ctrl-C kills the process
			d, err := load(cfgPath)
			if err != nil {
				return err
			}
			steps := track(stepsFor(cmd, plain))
			defer steps.Close()
			d.Steps = steps
			startedAt := d.Now()
			steps.Header("jauntbox", upContext(d.Cfg, d.Cfg.DefaultProfile))
			steps.Plan("Policy and limits", "SSH key, firewall, volume")
			var upRes client.UpResult
			attached := false // BeforeAttach ran: the box is ready, nothing to hand off
			spawn := d.Spawn  // Attach hides it from Up; Ctrl-C still needs it
			d.AfterUp = func(res client.UpResult) {
				upRes = res
				if res.Created {
					steps.Plan("Joined the tailnet", "Box reachable", "Install supervisor", "Unlock secrets", "Dev tools installed")
				} else {
					steps.Plan("Box online", "Dev tools installed")
				}
			}
			// Nothing may print under a live block: finish the stream before
			// Attach writes its own warnings and reconnect notes.
			d.BeforeAttach = func(created bool) {
				attached = true
				if created {
					finishUp(ctx, steps, d, startedAt, startedAt, true)
				}
				steps.Close()
			}
			err = client.Attach(ctx, d, session.Herdr{}, nested, runInherited, exec.LookPath)
			if err != nil && !attached {
				return handOff(ctx, cmd, spawn, steps, upRes, err)
			}
			return err
		},
	}
	root.Flags().BoolVar(&nested, "nested", false, "attach even from inside a local herdr pane (herdr inside herdr)")
	root.PersistentFlags().StringVar(&cfgPath, "config", defaultCfg, "path to the jauntbox config file")
	root.PersistentFlags().BoolVar(&plain, "plain", false, "no colour or animation: print one line per step")

	up := upCmd(&cfgPath, load)

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
	root.SetVersionTemplate(version.Current().Line("jauntbox") + "\n")

	root.AddCommand(initCmd(&cfgPath), up, push, acl, secretsCmd(&cfgPath), setupCmd(&cfgPath), unlockCmd(&cfgPath))
	root.AddCommand(clientOpsCmds(&cfgPath, load)...)
	// Agents start on the box: point a laptop `jauntbox agent` there.
	root.AddCommand(&cobra.Command{
		Use:                "agent",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("\"agent\" is a box command: run `jauntctl agent` on the box, in your repository's folder")
		},
	})
	return root
}

func main() {
	if err := newClientCmd().ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, cli.ErrReported) {
			os.Exit(1) // the error is already on stderr
		}
		fmt.Fprintf(os.Stderr, "jauntbox: %v\n", err)
		os.Exit(1)
	}
}
