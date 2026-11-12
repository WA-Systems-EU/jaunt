// Command jauntctl is the box side of jauntbox: the supervisor, agent runs,
// token minting and the secrets vault. It is installed on the box as
// /usr/local/bin/jauntctl, with a jauntbox symlink to it; it answers the same
// whatever name it is run as.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/cli"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
	"github.com/WA-Systems-EU/jaunt/internal/version"
)

// newBoxCmd is the jauntctl root.
func newBoxCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "jauntctl",
		Short:         "Box-side commands for a jauntbox workspace",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Accept anything so that a laptop command gets a pointed message
		// rather than cobra's generic "unknown command".
		Args:    cobra.ArbitraryArgs,
		Version: version.Current().Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return boxPreRun(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if slices.Contains(cli.LaptopCommands, args[0]) {
				return fmt.Errorf("%q is a laptop command: run `jauntbox %s` on your laptop", args[0], args[0])
			}
			return fmt.Errorf("unknown command %q for \"jauntctl\"", args[0])
		},
	}
	// jauntctl has no config file. The flag exists only so that passing it under
	// sudo gets a pointed refusal instead of being silently honoured.
	root.PersistentFlags().String("config", "", "")
	_ = root.PersistentFlags().MarkHidden("config")
	root.SetVersionTemplate(version.Current().Line("jauntctl") + "\n")
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

// boxPreRun runs before every jauntctl command. Under sudo it refuses --config and drops the caller's
// HOME, XDG_* and network/docker steering from the environment.
func boxPreRun(cmd *cobra.Command) error {
	if !underSudo() {
		if f := cmd.Flags().Lookup("config"); f != nil && f.Changed {
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

// boxCmds builds the supervisor command set.
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
				"version":  version.Current().Version,
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

func main() {
	if err := newBoxCmd().ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, errCredential) || errors.Is(err, cli.ErrReported) {
			os.Exit(1) // the error is already on stderr (git-credential's line, or the root part's)
		}
		fmt.Fprintf(os.Stderr, "jauntctl: %v\n", err)
		os.Exit(1)
	}
}
