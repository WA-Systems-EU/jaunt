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
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/session"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
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

// BoxName is the name the binary answers to on the box (installed as
// /usr/local/bin/jauntctl); under any other name it is the laptop client.
const BoxName = "jauntctl"

// rootFor picks the command tree for the name this binary was invoked as.
func rootFor(argv0 string) *cobra.Command {
	if filepath.Base(argv0) == BoxName {
		return newBoxCmd()
	}
	return newClientCmd()
}

// newBoxCmd is the jauntctl root. Later tasks register further box commands
// on it by adding them to the AddCommand call below.
func newBoxCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           BoxName,
		Short:         "Box-side commands for a jauntbox workspace",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Accept anything so that a laptop command gets a pointed message
		// rather than cobra's generic "unknown command".
		Args: cobra.ArbitraryArgs,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return boxPreRun(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			for _, c := range newClientCmd().Commands() {
				if c.Name() == args[0] {
					return fmt.Errorf("%q is a laptop command: run `jauntbox %s` on your laptop", args[0], args[0])
				}
			}
			return fmt.Errorf("unknown command %q for %q", args[0], BoxName)
		},
	}
	// jauntctl has no config file. The flag exists only so that passing it under
	// sudo gets a pointed refusal instead of being silently honoured.
	root.PersistentFlags().String("config", "", "")
	_ = root.PersistentFlags().MarkHidden("config")
	root.AddCommand(boxCmds()...)
	root.AddCommand(boxOnlyCmds()...)
	root.AddCommand(agentCmd(defaultAgentDeps()))
	return root
}

// underSudo reports whether we were started by sudo: the caller is the
// (untrusted) box user and everything it controls is hostile input.
func underSudo() bool { return os.Getenv("SUDO_USER") != "" }

// sudoScrubbedEnv are variables a sudo caller could set to steer root's HTTP
// (proxy, CA bundle), docker or path lookups. Box commands read only
// /etc/jauntbox, /run/jauntbox and /var/lib/jauntbox.
var sudoScrubbedEnv = []string{
	"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy",
	"NO_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
}

// boxPreRun runs before every jauntctl command (and before the box aliases on
// the client tree). Under sudo it refuses --config and drops the caller's
// HOME, XDG_* and network/docker steering from the environment.
func boxPreRun(cmd *cobra.Command) error {
	if !underSudo() {
		if f := cmd.Flags().Lookup("config"); f != nil && f.Changed && cmd.Root().Name() == BoxName {
			return errors.New("unknown flag: --config")
		}
		return nil
	}
	if f := cmd.Flags().Lookup("config"); f != nil && f.Changed {
		return errors.New("--config is not accepted under sudo")
	}
	for _, k := range sudoScrubbedEnv {
		os.Unsetenv(k)
	}
	os.Setenv("HOME", "/root")
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	return nil
}

// boxCmds builds the box command set; fresh instances on every call so the
// client can also register them as hidden transition aliases.
func boxCmds() []*cobra.Command {
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
				"locked":   vault.Locked(vault.Dir),
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
	return []*cobra.Command{supervise, stop, nightSet, boxStatus}
}

// newClientCmd is the jauntbox (laptop) root.
func newClientCmd() *cobra.Command {
	var cfgPath string
	var nested bool
	defaultCfg, defaultCfgErr := config.DefaultPath()
	root := &cobra.Command{
		Use:           "jauntbox",
		Short:         "On-demand remote workspace on Hetzner",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// Without a home directory there is no default config path: demand --config.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Box commands (the hidden aliases old units still run) never read the
			// config, and a system service has no $HOME.
			if cmd.Annotations["box"] != "" {
				return boxPreRun(cmd)
			}
			if defaultCfgErr != nil && !cmd.Flags().Changed("config") {
				return defaultCfgErr
			}
			return nil
		},
		// Bare `jauntbox`: bring the box up, wait for it and attach to the persistent session.
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d, err := loadDeps(cfgPath)
			if err != nil {
				return err
			}
			return client.Attach(ctx, d, session.Herdr{}, nested, runInherited, exec.LookPath)
		},
	}
	root.Flags().BoolVar(&nested, "nested", false, "attach even from inside a local herdr pane (herdr inside herdr)")
	root.PersistentFlags().StringVar(&cfgPath, "config", defaultCfg, "path to the jauntbox config file")

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
				if wait {
					fmt.Fprintln(cmd.OutOrStdout(), "making sure the supervisor is installed...")
				}
				return client.InstallSupervisor(ctx, d, wait)
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

	root.AddCommand(initCmd(&cfgPath), up, push, acl, secretsCmd(&cfgPath), setupCmd(&cfgPath), unlockCmd(&cfgPath))
	// Transition aliases: boxes created before jauntctl run `jauntbox supervise`
	// from their unit. Remove in plan 03.
	for _, c := range boxCmds() {
		c.Hidden = true
		c.Annotations = map[string]string{"box": "1"}
		root.AddCommand(c)
	}
	root.AddCommand(clientOpsCmds(&cfgPath, loadDeps)...)
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
	if err := rootFor(os.Args[0]).ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, errCredential) || errors.Is(err, errReported) {
			os.Exit(1) // the error is already on stderr (git-credential's line, or the root part's)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}
