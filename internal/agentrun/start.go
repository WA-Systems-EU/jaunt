package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/WA-Systems-EU/jaunt/internal/agent"
	"github.com/WA-Systems-EU/jaunt/internal/agentimage"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

const (
	// DefaultMax is the run length when neither the run nor the box sets one.
	DefaultMax = 8 * time.Hour
	// DefaultReadyWait bounds how long the container may take to initialise.
	DefaultReadyWait = 10 * time.Minute

	readyFile    = "/tmp/jauntbox-ready"
	initLogTail  = 20
	agentHomeDst = "/home/agent"

	// MaxRunMax bounds a run's --max (the same bound as the config's
	// agent.max_duration).
	MaxRunMax = 24 * time.Hour

	// MaxPrompt bounds an unattended prompt, in bytes.
	MaxPrompt = 64 << 10
)

// CheckMax validates a per-run --max.
func CheckMax(d time.Duration) error {
	if d <= 0 || d > MaxRunMax {
		return fmt.Errorf("--max must be greater than 0 and at most 24h (got %s)", d)
	}
	return nil
}

// StartOpts are the per-run choices. Dir is the caller's working directory.
type StartOpts struct {
	Dir, Branch, Prompt string
	Max                 time.Duration // 0: Starter.DefaultMax, else DefaultMax
	SkipPermissions     bool
	// Progress, when set, gets one-line notes for the user (stderr), such
	// as the first image build.
	Progress io.Writer
	// HostPorts are the host TCP ports this run may reach besides the
	// box-wide ones.
	HostPorts []int
}

// Starter starts agent runs. It runs as root on behalf of the box user
// (User, UID, GID) and only acts on checkouts under Home.
type Starter struct {
	Docker     Docker
	Agent      agent.Adapter
	Mint       func(ctx context.Context, repo string) (forge.Token, error)
	Repos      func(ctx context.Context) ([]forge.Repo, error)
	Net        Runner
	Paths      Paths
	SecretsDir string
	User       string
	Home       string // default /home/<User>
	UID, GID   int
	GitConfig  func(key string) (string, error) // the box user's global git config
	Now        func() time.Time
	Loc        *time.Location // box timezone, for the default branch date
	Events     events.Sink
	ReadyWait  time.Duration
	DefaultMax time.Duration // the box's agent.max_duration; 0 means DefaultMax
	HostPorts  []int         // the box's agent.host_ports, open to every run
	ProcNet    string        // default /proc/net; tcp and tcp6 are read for listen warnings

	poll time.Duration
	// rootUID/rootGID own the shared state directories (0 in production;
	// tests use their own ids).
	rootUID, rootGID int
}

// rootDirs are the shared directories run state is created under, parents
// first: /home/.jauntbox, its runs and agents, /run/jauntbox and its runs.
func (s *Starter) rootDirs() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range []string{filepath.Dir(s.Paths.RunsVol), s.Paths.RunsVol, filepath.Dir(s.Paths.Agents), s.Paths.Agents,
		filepath.Dir(s.Paths.RunsTmp), s.Paths.RunsTmp} {
		if d = filepath.Clean(d); !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// ensureRootDirs creates each missing shared directory 0755 root:root and
// refuses one that is a symlink, not root's, or writable by group or others:
// the user could otherwise swap what root creates under it.
func (s *Starter) ensureRootDirs() error {
	for _, d := range s.rootDirs() {
		fi, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(d, 0o755); err != nil {
				return err
			}
			if err := setOwnerMode(d, s.rootUID, s.rootGID, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory (a symlink?); refusing to keep agent state there", d)
		}
		uid := int(fi.Sys().(*syscall.Stat_t).Uid)
		if uid != s.rootUID || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s must be owned by root and not writable by group or others (owner uid %d, mode %v); fix it with `sudo chown root:root %s && sudo chmod go-w %s`",
				d, uid, fi.Mode().Perm(), d, d)
		}
	}
	return nil
}

// lockCheckout takes an exclusive, non-blocking lock for one checkout. The
// lock file lives in the root-only RunsTmp; closing it releases the lock.
func (s *Starter) lockCheckout(root string) (func(), error) {
	sum := sha256.Sum256([]byte(root))
	p := filepath.Join(s.Paths.RunsTmp, "checkout-"+hex.EncodeToString(sum[:8])+".lock")
	fd, err := unix.Open(p, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("a run is being started in %s; wait for it, then reopen it with `jauntctl agent attach`", root)
		}
		return nil, &os.PathError{Op: "flock", Path: p, Err: err}
	}
	return func() { unix.Close(fd) }, nil
}

func (s *Starter) home() string {
	if s.Home != "" {
		return s.Home
	}
	return "/home/" + s.User
}

func (s *Starter) agentAdapter() agent.Adapter {
	if s.Agent != nil {
		return s.Agent
	}
	return agent.ClaudeCode{}
}

var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

func validBranch(b string) bool {
	return len(b) <= 200 && branchRE.MatchString(b) && b[0] != '-' && b[0] != '/' && b[0] != '.' &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock") &&
		!strings.Contains(b, "..") && !strings.Contains(b, "//") && !strings.Contains(b, "/.")
}

// singleLine reports whether v fits on one env-file line.
func singleLine(v string) bool { return !strings.ContainsAny(v, "\r\n\x00") }

type kv struct{ k, v string }

// credentials returns the adapter's credentials from the vault's agents.env.
func (s *Starter) credentials() ([]kv, error) {
	if vault.Locked(s.SecretsDir) {
		return nil, vault.ErrLocked
	}
	data, err := vault.Read(s.SecretsDir, secrets.AgentEnvFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []kv
	for _, key := range s.agentAdapter().Credentials() {
		v, found, _ := secrets.ExtractEnv(data, key)
		if !found || v == "" {
			return nil, fmt.Errorf("missing agent credential %s: run `jauntbox secrets set %s` then `jauntbox unlock` on your laptop", key, key)
		}
		out = append(out, kv{key, v})
	}
	return out, nil
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func (s *Starter) notInstalled(repo string) error {
	slug := "jauntbox-" + s.User
	if raw, err := vault.Read(s.SecretsDir, secrets.AppMetaFile); err == nil {
		var meta struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal(raw, &meta) == nil && slugRE.MatchString(meta.Slug) {
			slug = meta.Slug
		}
	}
	return fmt.Errorf("the App %s is not installed on %s; add it at https://github.com/apps/%s/installations/new", slug, repo, slug)
}

func (s *Starter) gitIdentity() (name, email string, err error) {
	fix := "set it on the box with `git config --global user.name \"Your Name\"` and `git config --global user.email you@example.com`"
	get := func(key string) (string, error) {
		if s.GitConfig == nil {
			return "", fmt.Errorf("git %s is not set: %s", key, fix)
		}
		v, err := s.GitConfig(key)
		v = strings.TrimSpace(v)
		if err != nil || v == "" {
			return "", fmt.Errorf("git %s is not set for %s (the agent commits as you): %s", key, s.User, fix)
		}
		if !singleLine(v) {
			return "", fmt.Errorf("git %s must be a single line", key)
		}
		return v, nil
	}
	if name, err = get("user.name"); err != nil {
		return
	}
	email, err = get("user.email")
	return
}

// hostname turns a repository name into a valid container hostname.
func hostname(name string) string {
	b := []byte(strings.ToLower(name))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	h := strings.Trim(string(b), "-")
	if len(h) > 63 {
		h = strings.Trim(h[:63], "-")
	}
	if h == "" {
		h = "agent"
	}
	return h
}

// Start validates everything it can before creating anything, then creates
// the run's directories, token, network and container and waits until the
// container's init is done. Any failure after the first resource removes the
// container, network, tmpfs run dir and env file (the volume log is kept).
func (s *Starter) Start(ctx context.Context, o StartOpts) (Run, error) {
	var run Run
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	loc := s.Loc
	if loc == nil {
		loc = time.Local
	}
	ad := s.agentAdapter()

	creds, err := s.credentials()
	if err != nil {
		return run, err
	}
	if len(o.Prompt) > MaxPrompt {
		return run, fmt.Errorf("the prompt is too long (%d bytes, at most %d)", len(o.Prompt), MaxPrompt)
	}
	if strings.ContainsRune(o.Prompt, 0) {
		return run, errors.New("the prompt must not contain NUL bytes")
	}
	if o.Branch != "" && !validBranch(o.Branch) {
		return run, fmt.Errorf("invalid branch name %q", o.Branch)
	}
	if o.Max != 0 {
		if err := CheckMax(o.Max); err != nil {
			return run, err
		}
	}
	if err := config.CheckHostPorts("--host-port", o.HostPorts); err != nil {
		return run, err
	}

	co, err := FindCheckout(o.Dir, s.home())
	if err != nil {
		return run, err
	}
	if _, err := guardFiles(s.home(), co.Root, false, s.UID, s.GID); err != nil {
		return run, err
	}

	repos, err := s.Repos(ctx)
	if err != nil {
		return run, err
	}
	var repo forge.Repo
	found := false
	for _, r := range repos {
		if strings.EqualFold(r.FullName(), co.Repo) {
			repo, found = r, true
			break
		}
	}
	if !found {
		return run, s.notInstalled(co.Repo)
	}
	if !safePart(repo.Owner) || !safePart(repo.Name) {
		return run, fmt.Errorf("unexpected repository name %q from the forge", repo.FullName())
	}

	switchTo, branch := "", co.Branch
	switch {
	case o.Branch != "" && o.Branch == repo.DefaultBranch:
		return run, fmt.Errorf("%s is the default branch of %s; agents never work on it: pick another --branch or leave it out", o.Branch, repo.FullName())
	case o.Branch != "":
		switchTo = o.Branch
	case co.Detached || co.Branch == repo.DefaultBranch:
		switchTo = "agent/" + repo.Name + "-" + now().In(loc).Format("20060102")
	}
	if switchTo != "" {
		branch = switchTo
	}

	gitName, gitEmail, err := s.gitIdentity()
	if err != nil {
		return run, err
	}

	max := o.Max
	if max <= 0 {
		max = s.DefaultMax
	}
	if max <= 0 {
		max = DefaultMax
	}

	// Everything below is created under these; they must be root's alone.
	if err := s.ensureRootDirs(); err != nil {
		return run, err
	}
	// Held until Save, so a concurrent start in this checkout cannot also
	// pass the active-run check.
	unlock, err := s.lockCheckout(co.Root)
	if err != nil {
		return run, err
	}
	defer unlock()
	if err := s.checkNoActiveRun(ctx, co.Root); err != nil {
		return run, err
	}

	if err := EnsureNetGuard(ctx, s.Net, s.HostPorts); err != nil {
		return run, fmt.Errorf("cannot block the metadata service, the tailnet and the host from agent containers, refusing to start: %w", err)
	}
	building := func() {
		if o.Progress != nil {
			fmt.Fprintln(o.Progress, "building the agent image (first start, takes a few minutes)…")
		}
	}
	if err := agentimage.EnsureNotify(ctx, s.Docker, building); err != nil {
		return run, fmt.Errorf("agent image: %w", err)
	}
	tok, err := s.Mint(ctx, repo.FullName())
	if err != nil {
		return run, err
	}
	if tok.Value == "" || !singleLine(tok.Value) {
		return run, errors.New("the minted token is malformed")
	}

	mode := ModeInteractive
	if o.Prompt != "" {
		mode = ModeUnattended
	}
	started := now()
	run = Run{ID: NewRunID(repo.Name, started), Repo: repo.FullName(), Checkout: co.Root, Branch: branch,
		DefaultBranch: repo.DefaultBranch, Mode: mode, Prompt: o.Prompt, SkipPermissions: o.SkipPermissions,
		StartedAt: started, Max: max}
	run.HostPorts, run.RunPorts = s.portSets(o.HostPorts)
	if !validID(run.ID) {
		return Run{}, fmt.Errorf("invalid run id %q", run.ID)
	}

	var cleanups []func()
	ok := false
	defer func() {
		if ok {
			return
		}
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()
	bg := context.WithoutCancel(ctx)
	name := ContainerName(run.ID)

	// volume: <RunsVol>/<id>/ (root 0755) and log/ (user 0750); kept on failure
	volDir := filepath.Join(s.Paths.RunsVol, run.ID)
	if err := os.Mkdir(volDir, 0o755); errors.Is(err, os.ErrExist) {
		return Run{}, fmt.Errorf("run %s already exists; try again in a second", run.ID)
	} else if err != nil {
		return Run{}, err
	}
	logDir := filepath.Join(volDir, "log")
	if err := mkdirOwned(logDir, 0o750, s.UID, s.GID); err != nil {
		return Run{}, err
	}

	// tmpfs: <RunsTmp>/<id>/ (root:<gid> 0750) with token (root:<gid> 0640)
	tmpDir := filepath.Join(s.Paths.RunsTmp, run.ID)
	if err := os.Mkdir(tmpDir, 0o750); err != nil {
		return Run{}, err
	}
	cleanups = append(cleanups, func() { os.RemoveAll(tmpDir) })
	if err := setOwnerMode(tmpDir, -1, s.GID, 0o750); err != nil {
		return Run{}, err
	}

	// agent home on the volume (user 0700), parents root-owned
	homeDir := filepath.Join(s.Paths.Agents, repo.Owner, repo.Name, "home")
	if err := os.MkdirAll(filepath.Dir(homeDir), 0o755); err != nil {
		return Run{}, err
	}
	if err := mkdirOwned(homeDir, 0o700, s.UID, s.GID); err != nil && !errors.Is(err, os.ErrExist) {
		return Run{}, err
	}
	if err := checkPlainDir(homeDir); err != nil {
		return Run{}, err
	}
	if err := setOwnerMode(homeDir, s.UID, s.GID, 0o700); err != nil {
		return Run{}, err
	}

	hasModules, err := guardFiles(s.home(), co.Root, true, s.UID, s.GID)
	if err != nil {
		return Run{}, err
	}

	if err := writeNew(filepath.Join(tmpDir, "token"), []byte(tok.Value), 0o640, -1, s.GID); err != nil {
		return Run{}, err
	}

	// The prompt may span lines, which an env file cannot hold: it goes in
	// a file next to the token (read-only mount at /run/jauntbox-run).
	if mode == ModeUnattended {
		if err := writeNew(filepath.Join(tmpDir, "prompt"), []byte(o.Prompt), 0o640, -1, s.GID); err != nil {
			return Run{}, err
		}
	}

	labels := map[string]string{"jauntbox.agent": "1", "jauntbox.run": run.ID, "jauntbox.repo": run.Repo}
	if err := s.Docker.NetworkCreate(ctx, name, BridgeName(run.ID), labels); err != nil {
		return Run{}, fmt.Errorf("create network %s: %w", name, err)
	}
	cleanups = append(cleanups, func() { _ = s.Docker.NetworkRemove(bg, name) })
	if err := AllowRunPorts(ctx, s.Net, run.ID, run.RunPorts); err != nil {
		return Run{}, fmt.Errorf("cannot open the host ports for this run, refusing to start: %w", err)
	}
	if len(run.RunPorts) > 0 {
		cleanups = append(cleanups, func() { RevokeRunPorts(bg, s.Net, run.ID, run.RunPorts) })
	}
	s.warnListen(o.Progress, run.HostPorts)

	env := append([]kv{}, creds...)
	env = append(env, kv{"JAUNT_REPO", run.Repo}, kv{"JAUNT_RUN", run.ID}, kv{"JAUNT_DEFAULT_BRANCH", repo.DefaultBranch},
		kv{"GIT_AUTHOR_NAME", gitName}, kv{"GIT_AUTHOR_EMAIL", gitEmail},
		kv{"GIT_COMMITTER_NAME", gitName}, kv{"GIT_COMMITTER_EMAIL", gitEmail})
	if switchTo != "" {
		env = append(env, kv{"JAUNT_SWITCH_TO", switchTo})
	}
	var envb strings.Builder
	for _, e := range env {
		envb.WriteString(e.k + "=" + e.v + "\n")
	}

	g := co.Root + "/.git"
	// .git itself is a mount point (read-write) so the agent cannot rename
	// or remove it (EBUSY) and plant a fresh .git with its own config next
	// to the moved-away read-only files.
	mounts := []Mount{
		{Src: co.Root, Dst: "/work"},
		{Src: g, Dst: "/work/.git"},
		{Src: g + "/config", Dst: "/work/.git/config", RO: true},
		{Src: g + "/hooks", Dst: "/work/.git/hooks", RO: true},
	}
	if hasModules {
		mounts = append(mounts, Mount{Src: g + "/modules", Dst: "/work/.git/modules", RO: true})
	}
	mounts = append(mounts,
		Mount{Src: g + "/commondir", Dst: "/work/.git/commondir", RO: true},
		Mount{Src: homeDir, Dst: agentHomeDst},
		Mount{Src: tmpDir, Dst: "/run/jauntbox-run", RO: true},
		Mount{Src: logDir, Dst: "/var/log/jauntbox"},
	)
	var cmd []string
	if mode == ModeUnattended {
		cmd = ad.UnattendedCmd()
	}
	envFile := filepath.Join(s.Paths.RunsTmp, run.ID+".env")
	spec := ContainerSpec{
		Name: name, Hostname: hostname(repo.Name), Network: name, UID: s.UID, GID: s.GID,
		Labels:  []string{"jauntbox.agent=1", "jauntbox.run=" + run.ID, "jauntbox.repo=" + run.Repo},
		EnvFile: envFile, Env: []string{"HOME=" + agentHomeDst},
		Mounts: mounts, Image: agentimage.Tag(), Cmd: cmd,
		ExtraHosts: []string{"host.jauntbox:host-gateway"},
	}

	if err := writeNew(envFile, []byte(envb.String()), 0o600, -1, -1); err != nil {
		return Run{}, err
	}
	cleanups = append(cleanups, func() { _ = s.Docker.Remove(bg, name) })
	runErr := s.Docker.Run(ctx, spec)
	rmErr := os.Remove(envFile)
	if runErr != nil {
		return Run{}, fmt.Errorf("start container %s: %w", name, runErr)
	}
	if rmErr != nil {
		return Run{}, fmt.Errorf("remove %s: %w", envFile, rmErr)
	}

	if err := s.waitReady(ctx, name, mode); err != nil {
		tail := s.initLogTail(filepath.Join(logDir, "init.log"))
		il := filepath.Join(logDir, "init.log")
		if strings.TrimSpace(tail) == "" {
			return Run{}, fmt.Errorf("the agent container did not start: %w\n%s was empty or not written yet", err, il)
		}
		return Run{}, fmt.Errorf("the agent container did not start: %w\nlast lines of %s:\n%s", err, il, tail)
	}

	if err := s.Paths.Save(run); err != nil {
		return Run{}, err
	}
	ok = true
	if s.Events != nil {
		_ = s.Events.Emit(events.Event{Time: now().UTC(), Kind: events.AgentStarted,
			Fields: map[string]string{"run": run.ID, "repo": run.Repo, "branch": run.Branch, "mode": run.Mode,
				"host_ports": joinPorts(run.HostPorts)}})
	}
	return run, nil
}

// checkNoActiveRun refuses a second run in a checkout whose run still has a
// container and has not finished.
func (s *Starter) checkNoActiveRun(ctx context.Context, root string) error {
	runs, err := s.Paths.All()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Checkout != root || !r.FinishedAt.IsZero() {
			continue
		}
		st, err := s.Docker.Inspect(ctx, ContainerName(r.ID))
		if err != nil {
			return err
		}
		if st.Exists {
			return fmt.Errorf("run %s is already active in %s; reopen it with `jauntctl agent attach`", r.ID, root)
		}
	}
	return nil
}

func (s *Starter) waitReady(ctx context.Context, name, mode string) error {
	wait := s.ReadyWait
	if wait <= 0 {
		wait = DefaultReadyWait
	}
	poll := s.poll
	if poll <= 0 {
		poll = time.Second
	}
	user := strconv.Itoa(s.UID) + ":" + strconv.Itoa(s.GID)
	deadline := time.Now().Add(wait)
	for {
		if _, err := s.Docker.Exec(ctx, name, user, "test", "-f", readyFile); err == nil {
			return nil
		}
		st, err := s.Docker.Inspect(ctx, name)
		if err == nil && !st.Running {
			// An unattended run may finish before we see the ready file;
			// a clean exit means init succeeded.
			if mode == ModeUnattended && st.Exists && st.ExitCode == 0 {
				return nil
			}
			return fmt.Errorf("the container stopped (exit %d) before it was ready", st.ExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the container was not ready after %s; mise installs are cached, so trying again picks up where this one stopped", wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// initLogTail returns the last lines of init.log. The log dir is the user's,
// so the file is opened without following links and must be owned by the
// user (a hard link to someone else's file is refused).
func (s *Starter) initLogTail(path string) string {
	b, uid, err := readNoFollowOwner(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	if uid != s.UID {
		return "(" + path + " is not owned by " + s.User + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > initLogTail {
		lines = lines[len(lines)-initLogTail:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// mkdirOwned creates dir (not following a final symlink) and sets owner and mode.
func mkdirOwned(dir string, mode os.FileMode, uid, gid int) error {
	if err := os.Mkdir(dir, mode); err != nil {
		return err
	}
	return setOwnerMode(dir, uid, gid, mode)
}

func checkPlainDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	return nil
}

// setOwnerMode chowns (-1 keeps) and chmods p without following a symlink.
func setOwnerMode(p string, uid, gid int, mode os.FileMode) error {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(fd)
	if uid != -1 || gid != -1 {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return &os.PathError{Op: "chown", Path: p, Err: err}
		}
	}
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return &os.PathError{Op: "chmod", Path: p, Err: err}
	}
	return nil
}

// writeNew creates p exclusively (never through a link), writes data and
// sets owner (-1 keeps) and mode.
func writeNew(p string, data []byte, mode os.FileMode, uid, gid int) error {
	fd, err := unix.Open(p, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return &os.PathError{Op: "create", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), p)
	if err := setFile(f, data, mode, uid, gid); err != nil {
		f.Close()
		os.Remove(p)
		return err
	}
	return f.Close()
}

func setFile(f *os.File, data []byte, mode os.FileMode, uid, gid int) error {
	if uid != -1 || gid != -1 {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return err
	}
	_, err := f.Write(data)
	return err
}

// portSets returns the ports a run may reach (box-wide plus its own, sorted,
// no duplicates) and the ones it owns rules for (its own that are not
// box-wide).
func (s *Starter) portSets(own []int) (all, owned []int) {
	box := map[int]bool{}
	for _, p := range s.HostPorts {
		box[p] = true
	}
	seen := map[int]bool{}
	for _, p := range append(append([]int{}, s.HostPorts...), own...) {
		if !seen[p] {
			seen[p] = true
			all = append(all, p)
			if !box[p] {
				owned = append(owned, p)
			}
		}
	}
	sort.Ints(all)
	sort.Ints(owned)
	return all, owned
}

func joinPorts(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// warnListen notes to w the ports in ports that nothing serves in a way
// agents can reach. It never fails: an unreadable /proc/net skips the check.
func (s *Starter) warnListen(w io.Writer, ports []int) {
	if w == nil || len(ports) == 0 {
		return
	}
	dir := s.ProcNet
	if dir == "" {
		dir = "/proc/net"
	}
	tcp, err := os.ReadFile(filepath.Join(dir, "tcp"))
	if err != nil {
		return
	}
	tcp6, err := os.ReadFile(filepath.Join(dir, "tcp6"))
	if err != nil {
		return
	}
	for _, m := range ListenWarnings(tcp, tcp6, ports) {
		fmt.Fprintln(w, "warning: "+m)
	}
}
