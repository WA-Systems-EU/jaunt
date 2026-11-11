package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/agent"
	"github.com/WA-Systems-EU/jaunt/internal/agentrun"
	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// jauntctlPath is the only jauntctl the sudoers line lets the box user run
// without a password; every root re-exec goes through exactly this path.
const jauntctlPath = "/usr/local/bin/jauntctl"

// errReported makes jauntctl exit 1 without printing: the root part already
// wrote its error to the terminal.
var errReported = errors.New("already reported")

// sudoUser is the box user a root agent command acts for.
type sudoUser struct {
	Name     string
	Home     string // /home/<Name>
	UID, GID int
}

// agentDeps are the seams of the agent commands.
type agentDeps struct {
	euid     func() int
	getwd    func() (string, error)
	userHome func() (string, error)
	lookPath func(string) (string, error)
	// exec replaces the process (syscall.Exec), so a TTY passes through.
	exec func(path string, argv, env []string) error
	// sudo runs `sudo -n jauntctl args...` with the terminal's stdin and
	// stderr, stdout to the writer; a non-zero exit is errReported.
	sudo func(args []string, stdout io.Writer) error

	// root side
	docker     agentrun.Docker
	paths      agentrun.Paths
	lookupUser func() (sudoUser, error)
	// startRun is the root part of `jauntctl agent` (Starter.Start).
	startRun func(ctx context.Context, u sudoUser, o agentrun.StartOpts) (agentrun.Run, error)
	events   events.Sink
	adapter  agent.Adapter
	now      func() time.Time
	poll     time.Duration // logs --follow
}

func defaultAgentDeps() *agentDeps {
	d := &agentDeps{
		euid:       os.Geteuid,
		getwd:      os.Getwd,
		userHome:   os.UserHomeDir,
		lookPath:   exec.LookPath,
		exec:       syscall.Exec,
		sudo:       runSudo,
		docker:     agentrun.NewCLI(),
		paths:      agentrun.DefaultPaths(),
		lookupUser: lookupSudoUser,
		events:     events.JSONLFile(supervisor.DefaultPaths().Events),
		adapter:    agent.ClaudeCode{},
		now:        time.Now,
		poll:       500 * time.Millisecond,
	}
	d.startRun = func(ctx context.Context, u sudoUser, o agentrun.StartOpts) (agentrun.Run, error) {
		s, err := d.starter(u)
		if err != nil {
			return agentrun.Run{}, err
		}
		return s.Start(ctx, o)
	}
	return d
}

// runSudo runs the root part with the user's terminal on stdin and stderr.
func runSudo(args []string, stdout io.Writer) error {
	c := exec.Command("sudo", append([]string{"-n", jauntctlPath}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return errReported
	}
	return err
}

var userNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// lookupSudoUser is the box user behind sudo, from the passwd entry of
// SUDO_USER (never SUDO_UID). Root agent commands refuse to run without one,
// and never act for root.
func lookupSudoUser() (sudoUser, error) {
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return sudoUser{}, errors.New("run this as the box user (it uses sudo itself), not as root")
	}
	if !userNameRE.MatchString(name) {
		return sudoUser{}, fmt.Errorf("unexpected user name %q", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return sudoUser{}, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return sudoUser{}, fmt.Errorf("unexpected ids for %s", name)
	}
	if uid == 0 || gid == 0 {
		return sudoUser{}, errors.New("agents never run as root; run this as the box user")
	}
	return sudoUser{Name: name, Home: "/home/" + name, UID: uid, GID: gid}, nil
}

// starter is the production Starter for u.
func (d *agentDeps) starter(u sudoUser) (*agentrun.Starter, error) {
	info, err := boxinfo.Read(boxinfo.Path)
	if err != nil {
		return nil, err
	}
	loc := time.Local
	if info.Timezone != "" {
		if loc, err = time.LoadLocation(info.Timezone); err != nil {
			return nil, err
		}
	}
	m := newMinter()
	return &agentrun.Starter{
		Docker: d.docker, Agent: d.adapter, Mint: m.Mint, Repos: m.Repos, Net: agentrun.HostRunner,
		Paths: d.paths, SecretsDir: vault.Dir, User: u.Name, Home: u.Home, UID: u.UID, GID: u.GID,
		GitConfig: userGitConfig(u), Loc: loc, Events: d.events,
		DefaultMax: time.Duration(info.Agent.MaxDuration), HostPorts: info.Agent.HostPorts,
	}, nil
}

// userGitConfig reads the box user's global git config as that user.
func userGitConfig(u sudoUser) func(key string) (string, error) {
	return func(key string) (string, error) {
		c := exec.Command("runuser", "-u", u.Name, "--", "git", "config", "--global", "--get", key)
		c.Env = []string{"HOME=" + u.Home, "PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C"}
		c.Dir = "/"
		out, err := c.Output()
		return strings.TrimSpace(string(out)), err
	}
}

// reexec replaces this process with `sudo -n /usr/local/bin/jauntctl args...`.
func (d *agentDeps) reexec(args ...string) error {
	p, err := d.lookPath("sudo")
	if err != nil {
		return errors.New("sudo not found")
	}
	return d.exec(p, append([]string{"sudo", "-n", jauntctlPath}, args...), os.Environ())
}

// owns reports whether run r belongs to u: its checkout lies under u's home.
func (u sudoUser) owns(r agentrun.Run) bool {
	home := filepath.Clean(u.Home)
	c := filepath.Clean(r.Checkout)
	return filepath.IsAbs(c) && c == r.Checkout && strings.HasPrefix(c, home+"/")
}

func noSuchRun(id string) error { return fmt.Errorf("no such run %s", id) }

// resolveRun finds the run a root command acts on: id, or without one the
// newest unfinished run of the checkout dir is in. It accepts only u's runs
// whose container carries the jauntbox.agent=1 label; anything else reads
// as "no such run", so other users' runs stay invisible.
func (d *agentDeps) resolveRun(ctx context.Context, u sudoUser, id, dir string) (agentrun.Run, error) {
	var r agentrun.Run
	if id != "" {
		if !agentrun.ValidRunID(id) {
			return r, fmt.Errorf("invalid run id %q", id)
		}
		var err error
		if r, err = d.paths.Load(id); err != nil {
			return agentrun.Run{}, noSuchRun(id)
		}
	} else {
		co, err := agentrun.FindCheckout(dir, u.Home)
		if err != nil {
			return r, err
		}
		runs, err := d.paths.All()
		if err != nil {
			return r, err
		}
		for _, x := range runs {
			if x.Checkout == co.Root && x.FinishedAt.IsZero() {
				r = x // newest wins: runs are oldest first
			}
		}
		if r.ID == "" {
			return r, fmt.Errorf("no active run in %s; start one there with `jauntctl agent`", co.Root)
		}
		id = r.ID
	}
	if r.ID != id || !u.owns(r) {
		return agentrun.Run{}, noSuchRun(id)
	}
	labelled, err := d.docker.ListRuns(ctx)
	if err != nil {
		return agentrun.Run{}, err
	}
	if !slices.Contains(labelled, id) {
		return agentrun.Run{}, noSuchRun(id)
	}
	return r, nil
}

// readRun finds the run a read-only root command (logs) shows: id, or
// without one the newest run of the checkout dir is in, finished or not. It
// needs only a valid id and a root-written record whose checkout is under
// u's home; the container may be gone. Anything else is "no such run".
func (d *agentDeps) readRun(u sudoUser, id, dir string) (agentrun.Run, error) {
	var r agentrun.Run
	if id != "" {
		if !agentrun.ValidRunID(id) {
			return r, fmt.Errorf("invalid run id %q", id)
		}
		r, err := d.paths.Load(id)
		if err != nil || r.ID != id || !u.owns(r) {
			return agentrun.Run{}, noSuchRun(id)
		}
		return r, nil
	}
	co, err := agentrun.FindCheckout(dir, u.Home)
	if err != nil {
		return r, err
	}
	runs, err := d.paths.All()
	if err != nil {
		return r, err
	}
	for _, x := range runs {
		if x.Checkout == co.Root && u.owns(x) {
			r = x // newest wins: runs are oldest first
		}
	}
	if r.ID == "" {
		return r, fmt.Errorf("no run in %s; start one there with `jauntctl agent`", co.Root)
	}
	return r, nil
}

// idOrDir is the tail of a user-side re-exec's root command line: the run
// id if given (checked first), else --dir <cwd>.
func (d *agentDeps) idOrDir(args []string) ([]string, error) {
	if len(args) == 1 {
		if !agentrun.ValidRunID(args[0]) {
			return nil, fmt.Errorf("invalid run id %q", args[0])
		}
		return []string{args[0]}, nil
	}
	wd, err := d.getwd()
	if err != nil {
		return nil, err
	}
	return []string{"--dir", wd}, nil
}

// rootDir is --dir, or the working directory.
func (d *agentDeps) rootDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return d.getwd()
}

func signalCtx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
}

// agentCmd is `jauntctl agent` and its subcommands. Each command run by the
// box user re-execs itself through `sudo -n /usr/local/bin/jauntctl agent …`;
// as root it does the work for SUDO_USER.
func agentCmd(d *agentDeps) *cobra.Command {
	var branch, prompt string
	var max time.Duration
	var skip bool
	var hostPorts []int
	c := &cobra.Command{
		Use:   "agent",
		Short: "Start a coding agent in a container on the checkout you are in (box side)",
		Long: "Start a coding agent in a container, working in the checkout you are in.\n" +
			"Interactive runs open Claude in this terminal; --unattended runs work on their own.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() == 0 {
				return errors.New("run `jauntctl agent` as yourself, not with sudo; it asks sudo for the root part itself")
			}
			if cmd.Flags().Changed("max") {
				if err := agentrun.CheckMax(max); err != nil {
					return err
				}
			}
			if err := config.CheckHostPorts("--host-port", hostPorts); err != nil {
				return err
			}
			wd, err := d.getwd()
			if err != nil {
				return err
			}
			rootArgs := []string{"agent", "start-run", "--dir", wd}
			if branch != "" {
				rootArgs = append(rootArgs, "--branch", branch)
			}
			if prompt != "" {
				rootArgs = append(rootArgs, "--unattended", prompt)
			}
			if cmd.Flags().Changed("max") {
				rootArgs = append(rootArgs, "--max", max.String())
			}
			if skip {
				rootArgs = append(rootArgs, "--skip-permissions")
			}
			for _, p := range hostPorts {
				rootArgs = append(rootArgs, "--host-port", strconv.Itoa(p))
			}
			rootArgs = append(rootArgs, "--json")

			// The branch before the start, read as the user, to tell whether
			// the run switched it.
			before, haveBefore := "", false
			if home, err := d.userHome(); err == nil {
				if co, err := agentrun.FindCheckout(wd, home); err == nil {
					before, haveBefore = co.Branch, true
				}
			}

			var out bytes.Buffer
			if err := d.sudo(rootArgs, &out); err != nil {
				return err
			}
			var run agentrun.Run
			if err := json.Unmarshal(out.Bytes(), &run); err != nil || !agentrun.ValidRunID(run.ID) {
				return errors.New("the root part returned no run")
			}
			w := cmd.OutOrStdout()
			if haveBefore && run.Branch != before {
				fmt.Fprintf(w, "switched %s to %s\n", run.Checkout, run.Branch)
			}
			if run.Mode == agentrun.ModeUnattended {
				fmt.Fprintf(w, "run %s started; watch it with: jauntctl agent logs --follow\n", run.ID)
				return nil
			}
			return d.reexec("agent", "attach", run.ID)
		},
	}
	c.Flags().StringVar(&branch, "branch", "", "work on this branch (created from your current commit if missing)")
	c.Flags().StringVar(&prompt, "unattended", "", "run unattended with this prompt")
	c.Flags().DurationVar(&max, "max", 0, "maximum run length (default: the box's agent.max_duration, 8h)")
	c.Flags().BoolVar(&skip, "skip-permissions", false, "interactive: let Claude skip permission prompts (the container is the sandbox)")
	c.Flags().IntSliceVar(&hostPorts, "host-port", nil, "let this run reach this TCP port on the box (repeatable; on top of agent.host_ports)")
	c.AddCommand(startRunCmd(d), attachCmd(d), listCmd(d), stopAgentCmd(d), logsCmd(d))
	return c
}

func startRunCmd(d *agentDeps) *cobra.Command {
	var o agentrun.StartOpts
	var asJSON bool
	c := &cobra.Command{
		Use:    "start-run --dir <checkout>",
		Short:  "Root part of `jauntctl agent`: start the run's container",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() != 0 {
				return errors.New("start-run is the root part of `jauntctl agent`; run that instead")
			}
			u, err := d.lookupUser()
			if err != nil {
				return err
			}
			if o.Dir == "" {
				return errors.New("--dir is required")
			}
			if cmd.Flags().Changed("max") {
				if err := agentrun.CheckMax(o.Max); err != nil {
					return err
				}
			}
			if err := config.CheckHostPorts("--host-port", o.HostPorts); err != nil {
				return err
			}
			// notes such as the first image build go to stderr: with
			// --json, stdout carries the run only
			o.Progress = cmd.ErrOrStderr()
			ctx, stop := signalCtx(cmd)
			defer stop()
			run, err := d.startRun(ctx, u, o)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(run)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %s started in %s on %s\n", run.ID, run.Checkout, run.Branch)
			return nil
		},
	}
	c.Flags().StringVar(&o.Dir, "dir", "", "the caller's working directory")
	c.Flags().StringVar(&o.Branch, "branch", "", "")
	c.Flags().StringVar(&o.Prompt, "unattended", "", "")
	c.Flags().DurationVar(&o.Max, "max", 0, "")
	c.Flags().BoolVar(&o.SkipPermissions, "skip-permissions", false, "")
	c.Flags().IntSliceVar(&o.HostPorts, "host-port", nil, "")
	c.Flags().BoolVar(&asJSON, "json", false, "print the run as JSON")
	return c
}

var termRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)

func attachCmd(d *agentDeps) *cobra.Command {
	var shell bool
	var dir string
	c := &cobra.Command{
		Use:   "attach [<run>]",
		Short: "Reopen a run's Claude session (or a shell with --shell) in this terminal",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() != 0 {
				rest, err := d.idOrDir(args)
				if err != nil {
					return err
				}
				a := []string{"agent", "attach"}
				if shell {
					a = append(a, "--shell")
				}
				return d.reexec(append(a, rest...)...)
			}
			u, err := d.lookupUser()
			if err != nil {
				return err
			}
			wd, err := d.rootDir(dir)
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			ctx := cmd.Context()
			r, err := d.resolveRun(ctx, u, id, wd)
			if err != nil {
				return err
			}
			if !r.FinishedAt.IsZero() {
				return fmt.Errorf("run %s has finished", r.ID)
			}
			name := agentrun.ContainerName(r.ID)
			st, err := d.docker.Inspect(ctx, name)
			if err != nil {
				return err
			}
			if !st.Running {
				return fmt.Errorf("run %s is not running (its container has stopped); see `jauntctl agent list`", r.ID)
			}
			userSpec := strconv.Itoa(u.UID) + ":" + strconv.Itoa(u.GID)
			var argv []string
			if shell {
				argv = []string{"bash"}
			} else {
				if r.Mode == agentrun.ModeUnattended {
					return fmt.Errorf("run %s is unattended; watch it with `jauntctl agent logs --follow %s`, or open a shell with --shell", r.ID, r.ID)
				}
				_, err := d.docker.Exec(ctx, name, userSpec, "test", "-f", "/work/CONTINUATION.md")
				resume := err == nil
				marker := filepath.Join(d.paths.RunsVol, r.ID, "first-attach-done")
				_, err = os.Lstat(marker)
				later := err == nil
				if !later {
					f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
					if err != nil {
						return err
					}
					f.Close()
				}
				argv = d.adapter.InteractiveCmd(resume, later, r.SkipPermissions)
			}
			docker, err := d.lookPath("docker")
			if err != nil {
				return errors.New("docker not found")
			}
			full := []string{"docker", "exec", "-it", "-w", "/work", "--user", userSpec}
			if t := os.Getenv("TERM"); termRE.MatchString(t) {
				full = append(full, "-e", "TERM="+t)
			}
			full = append(append(full, name), argv...)
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "DOCKER_") {
					env = append(env, kv)
				}
			}
			return d.exec(docker, full, env)
		},
	}
	c.Flags().BoolVar(&shell, "shell", false, "open bash in the container instead of Claude")
	c.Flags().StringVar(&dir, "dir", "", "the checkout to use when no run is given (default: the working directory)")
	_ = c.Flags().MarkHidden("dir")
	return c
}

func listCmd(d *agentDeps) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List agent runs with their state, limit and container IP",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() != 0 {
				a := []string{"agent", "list"}
				if asJSON {
					a = append(a, "--json")
				}
				return d.reexec(a...)
			}
			sts, err := agentrun.List(cmd.Context(), d.docker, d.paths)
			if err != nil {
				return err
			}
			// Under sudo only the caller's runs; plain root sees all.
			if u, err := d.lookupUser(); err == nil {
				sts = slices.DeleteFunc(sts, func(s agentrun.Status) bool { return !u.owns(s.Run) })
			} else if underSudo() {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				if sts == nil {
					sts = []agentrun.Status{}
				}
				return json.NewEncoder(w).Encode(sts)
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "RUN\tREPO\tBRANCH\tMODE\tSTATE\tSTARTED\tMAX\tIP\tPORTS")
			for _, s := range sts {
				ip := s.IP
				if ip == "" {
					ip = "-"
				}
				ports := "-"
				if len(s.HostPorts) > 0 {
					p := make([]string, len(s.HostPorts))
					for i, n := range s.HostPorts {
						p[i] = strconv.Itoa(n)
					}
					ports = strings.Join(p, ",")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Repo, s.Branch, s.Mode, s.State,
					s.StartedAt.Local().Format("2006-01-02 15:04"), shortDuration(s.Max), ip, ports)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	return c
}

// shortDuration prints 8h0m0s as 8h and 1h30m0s as 1h30m.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func stopAgentCmd(d *agentDeps) *cobra.Command {
	var now bool
	var dir string
	c := &cobra.Command{
		Use:   "stop [<run>]",
		Short: "Ask a run to wrap up (commit and push CONTINUATION.md); --now also stops it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() != 0 {
				rest, err := d.idOrDir(args)
				if err != nil {
					return err
				}
				a := []string{"agent", "stop"}
				if now {
					a = append(a, "--now")
				}
				return d.reexec(append(a, rest...)...)
			}
			u, err := d.lookupUser()
			if err != nil {
				return err
			}
			wd, err := d.rootDir(dir)
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			ctx, stop := signalCtx(cmd)
			defer stop()
			r, err := d.resolveRun(ctx, u, id, wd)
			if err != nil {
				return err
			}
			// --now finishes the run even when its tmpfs dir (and so the
			// flag) is gone.
			if err := agentrun.RequestWrapUp(d.paths, r.ID, "stop-requested", d.now()); err != nil &&
				!(now && errors.Is(err, os.ErrNotExist)) {
				return err
			}
			w := cmd.OutOrStdout()
			if !now {
				fmt.Fprintf(w, "asked run %s to wrap up; the supervisor stops it after the grace period (or run `jauntctl agent stop --now %s`)\n", r.ID, r.ID)
				return nil
			}
			if err := agentrun.Finish(ctx, d.docker, d.paths, agentrun.HostRunner, r.ID, d.events, d.now()); err != nil {
				return err
			}
			fmt.Fprintf(w, "run %s stopped\n", r.ID)
			return nil
		},
	}
	c.Flags().BoolVar(&now, "now", false, "run the mechanical wrap-up and stop the container now")
	c.Flags().StringVar(&dir, "dir", "", "the checkout to use when no run is given (default: the working directory)")
	_ = c.Flags().MarkHidden("dir")
	return c
}

func logsCmd(d *agentDeps) *cobra.Command {
	var follow bool
	var dir string
	c := &cobra.Command{
		Use:   "logs [<run>]",
		Short: "Show an unattended run's Claude log (--follow to watch it)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if d.euid() != 0 {
				rest, err := d.idOrDir(args)
				if err != nil {
					return err
				}
				a := []string{"agent", "logs"}
				if follow {
					a = append(a, "--follow")
				}
				return d.reexec(append(a, rest...)...)
			}
			u, err := d.lookupUser()
			if err != nil {
				return err
			}
			wd, err := d.rootDir(dir)
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			ctx, stop := signalCtx(cmd)
			defer stop()
			r, err := d.readRun(u, id, wd)
			if err != nil {
				return err
			}
			return d.showLog(ctx, cmd.OutOrStdout(), u, r, follow)
		},
	}
	c.Flags().BoolVar(&follow, "follow", false, "keep printing new lines until the run's container stops")
	c.Flags().StringVar(&dir, "dir", "", "the checkout to use when no run is given (default: the working directory)")
	_ = c.Flags().MarkHidden("dir")
	return c
}

// openUserLog opens a file in the user-owned log dir without following a
// symlink, and only if it is a regular file owned by the user (a hard link
// to another user's file is refused).
func openUserLog(path string, uid int) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symlink; refusing to follow it", path)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() || int(fi.Sys().(*syscall.Stat_t).Uid) != uid {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file owned by you", path)
	}
	return f, nil
}

// showLog copies the run's claude.jsonl to w; with follow it keeps copying
// new data until the container stops (or ctx ends).
func (d *agentDeps) showLog(ctx context.Context, w io.Writer, u sudoUser, r agentrun.Run, follow bool) error {
	path := filepath.Join(d.paths.RunsVol, r.ID, "log", "claude.jsonl")
	name := agentrun.ContainerName(r.ID)
	running := func() bool {
		st, err := d.docker.Inspect(ctx, name)
		return err == nil && st.Running
	}
	var f *os.File
	for {
		var err error
		f, err = openUserLog(path, u.UID)
		if err == nil {
			break
		}
		if !follow || !errors.Is(err, os.ErrNotExist) {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("run %s has no Claude log yet (%s); only unattended runs write one", r.ID, path)
			}
			return err
		}
		if !running() {
			return fmt.Errorf("run %s stopped without writing a Claude log (%s)", r.ID, path)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d.poll):
		}
	}
	defer f.Close()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		if !running() {
			_, err := io.Copy(w, f)
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d.poll):
		}
	}
}
