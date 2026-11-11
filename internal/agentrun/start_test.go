package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/agent"
	"github.com/WA-Systems-EU/jaunt/internal/agentimage"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

type fakeDocker struct {
	calls      []string
	specs      []ContainerSpec
	envAtRun   string
	envModeRun os.FileMode
	present    map[string]bool
	stopped    bool // containers stop right after starting (init failed)
	neverReady bool
	initLog    string // written to the log mount by Run
	runErr     error
	imageOK    bool
	inExec     chan struct{} // if set, Exec signals it once, then waits on gate
	gate       chan struct{}
}

func (f *fakeDocker) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeDocker) ImageExists(_ context.Context, tag string) (bool, error) {
	f.rec("ImageExists " + tag)
	return f.imageOK, nil
}
func (f *fakeDocker) Build(_ context.Context, tag string, _ []byte) error {
	f.rec("Build " + tag)
	return nil
}
func (f *fakeDocker) NetworkCreate(_ context.Context, name string, labels map[string]string) error {
	f.rec(fmt.Sprintf("NetworkCreate %s %v", name, labels))
	return nil
}
func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	f.rec("NetworkRemove " + name)
	return nil
}
func (f *fakeDocker) Run(_ context.Context, spec ContainerSpec) error {
	f.rec("Run " + spec.Name)
	f.specs = append(f.specs, spec)
	b, err := os.ReadFile(spec.EnvFile)
	if err == nil {
		f.envAtRun = string(b)
		fi, _ := os.Stat(spec.EnvFile)
		f.envModeRun = fi.Mode().Perm()
	}
	if f.runErr != nil {
		return f.runErr
	}
	if f.present == nil {
		f.present = map[string]bool{}
	}
	f.present[spec.Name] = true
	if f.initLog != "" {
		for _, m := range spec.Mounts {
			if m.Dst == "/var/log/jauntbox" {
				os.WriteFile(filepath.Join(m.Src, "init.log"), []byte(f.initLog), 0o644)
			}
		}
	}
	return nil
}
func (f *fakeDocker) Exec(_ context.Context, container, user string, args ...string) ([]byte, error) {
	f.rec("Exec " + container + " " + user + " " + strings.Join(args, " "))
	if f.gate != nil {
		select {
		case f.inExec <- struct{}{}:
		default:
		}
		<-f.gate
	}
	if f.stopped || f.neverReady {
		return nil, errors.New("exit 1")
	}
	return nil, nil
}
func (f *fakeDocker) Inspect(_ context.Context, name string) (State, error) {
	f.rec("Inspect " + name)
	if !f.present[name] {
		return State{}, nil
	}
	if f.stopped {
		return State{Exists: true, ExitCode: 1}, nil
	}
	return State{Exists: true, Running: true, IP: "172.18.0.2"}, nil
}
func (f *fakeDocker) Logs(_ context.Context, name string, tail int) (string, error) {
	f.rec("Logs " + name)
	return "", nil
}
func (f *fakeDocker) Remove(_ context.Context, name string) error {
	f.rec("Remove " + name)
	delete(f.present, name)
	return nil
}
func (f *fakeDocker) Stop(_ context.Context, name string, _ time.Duration) error {
	f.rec("Stop " + name)
	return nil
}
func (f *fakeDocker) ListRuns(context.Context) ([]string, error) {
	f.rec("ListRuns")
	return nil, nil
}

type sinkRec struct{ ev []events.Event }

func (s *sinkRec) Emit(e events.Event) error { s.ev = append(s.ev, e); return nil }

type tenv struct {
	s       *Starter
	d       *fakeDocker
	ev      *sinkRec
	home    string
	root    string
	netCall int
	minted  []string
}

// at is 23:30 UTC on Nov 10, i.e. 00:30 on Nov 11 in the box's zone (UTC+1).
var at = time.Date(2026, 11, 10, 23, 30, 0, 0, time.UTC)

const wantID = "widget-20261110-233000"

func newTenv(t *testing.T, head string) *tenv {
	t.Helper()
	home := realTemp(t)
	root := filepath.Join(home, "src", "widget")
	mkCheckout(t, root, "git@github.com:acme/widget.git", head)
	sec := filepath.Join(realTemp(t), "secrets")
	if err := os.MkdirAll(sec, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sec, "agents.env"), "OTHER=y\nCLAUDE_CODE_OAUTH_TOKEN=sk-ant-x\n")
	writeFile(t, filepath.Join(sec, ".loaded"), "")
	base := realTemp(t)
	e := &tenv{d: &fakeDocker{}, ev: &sinkRec{}, home: home, root: root}
	e.s = &Starter{
		Docker: e.d,
		Agent:  agent.ClaudeCode{},
		Mint: func(_ context.Context, repo string) (forge.Token, error) {
			e.minted = append(e.minted, repo)
			return forge.Token{Value: "ghs_x", Repo: repo}, nil
		},
		Repos: func(context.Context) ([]forge.Repo, error) {
			return []forge.Repo{
				{Owner: "acme", Name: "gadget", DefaultBranch: "trunk"},
				{Owner: "acme", Name: "widget", DefaultBranch: "main"},
			}, nil
		},
		Net:        func(context.Context, string, ...string) error { e.netCall++; return nil },
		Paths:      Paths{RunsTmp: filepath.Join(base, "run"), RunsVol: filepath.Join(base, "vol"), Agents: filepath.Join(base, "agents")},
		SecretsDir: sec,
		User:       "alice",
		Home:       home,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
		GitConfig: func(k string) (string, error) {
			return map[string]string{"user.name": "Alice", "user.email": "alice@example.com"}[k], nil
		},
		Now:       func() time.Time { return at },
		Loc:       time.FixedZone("BOX", 3600),
		Events:    e.ev,
		ReadyWait: 200 * time.Millisecond,
		poll:      time.Millisecond,
		rootUID:   os.Getuid(),
		rootGID:   os.Getgid(),
	}
	return e
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *tenv) dockerCalls(prefix string) int {
	n := 0
	for _, c := range e.d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (e *tenv) assertNothingCreated(t *testing.T) {
	t.Helper()
	if len(e.d.calls) != 0 {
		t.Errorf("docker calls: %v", e.d.calls)
	}
	// the shared root dirs may exist (empty but for checkout locks)
	for _, p := range []string{e.s.Paths.RunsTmp, e.s.Paths.RunsVol, e.s.Paths.Agents} {
		ents, _ := os.ReadDir(p)
		for _, d := range ents {
			if !(p == e.s.Paths.RunsTmp && strings.HasSuffix(d.Name(), ".lock")) {
				t.Errorf("%s/%s was created", p, d.Name())
			}
		}
	}
	for _, p := range []string{".git/hooks", ".git/commondir"} {
		if _, err := os.Lstat(filepath.Join(e.root, p)); err == nil {
			t.Errorf("%s was created", p)
		}
	}
	if len(e.minted) != 0 {
		t.Errorf("minted %v", e.minted)
	}
	if len(e.ev.ev) != 0 {
		t.Errorf("events %v", e.ev.ev)
	}
}

func envLines(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		k, v, _ := strings.Cut(l, "=")
		m[k] = v
	}
	return m
}

func modeOf(t *testing.T, p string) (os.FileMode, int) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode(), int(fi.Sys().(*syscall.Stat_t).Gid)
}

func TestStartHappyPath(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	r, err := e.s.Start(context.Background(), StartOpts{Dir: filepath.Join(e.root), SkipPermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != wantID {
		t.Fatalf("id %q", r.ID)
	}
	p := e.s.Paths
	ug := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	name := "jauntbox-" + wantID
	envFile := filepath.Join(p.RunsTmp, wantID+".env")
	want := []string{"run", "-d", "--name", name, "--hostname", "widget", "--network", name,
		"--user", ug, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--pids-limit", "4096",
		"--label", "jauntbox.agent=1", "--label", "jauntbox.run=" + wantID, "--label", "jauntbox.repo=acme/widget",
		"--env-file", envFile, "-e", "HOME=/home/agent",
		"-v", e.root + ":/work",
		"-v", e.root + "/.git/config:/work/.git/config:ro",
		"-v", e.root + "/.git/hooks:/work/.git/hooks:ro",
		"-v", e.root + "/.git/commondir:/work/.git/commondir:ro",
		"-v", filepath.Join(p.Agents, "acme", "widget", "home") + ":/home/agent",
		"-v", filepath.Join(p.RunsTmp, wantID) + ":/run/jauntbox-run:ro",
		"-v", filepath.Join(p.RunsVol, wantID, "log") + ":/var/log/jauntbox",
		agentimage.Tag(),
	}
	if len(e.d.specs) != 1 {
		t.Fatalf("specs %v", e.d.specs)
	}
	if got := e.d.specs[0].Args(); !reflect.DeepEqual(got, want) {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
	// checkout guard files
	if b, err := os.ReadFile(filepath.Join(e.root, ".git", "commondir")); err != nil || string(b) != "." {
		t.Fatalf("commondir %q %v", b, err)
	}
	if m, _ := modeOf(t, filepath.Join(e.root, ".git", "hooks")); !m.IsDir() {
		t.Fatal("hooks not a dir")
	}
	// env file: content at run time, 0600, removed after
	wantEnv := "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-x\nJAUNT_REPO=acme/widget\nJAUNT_RUN=" + wantID +
		"\nJAUNT_DEFAULT_BRANCH=main\nGIT_AUTHOR_NAME=Alice\nGIT_AUTHOR_EMAIL=alice@example.com\nGIT_COMMITTER_NAME=Alice\nGIT_COMMITTER_EMAIL=alice@example.com\n"
	if e.d.envAtRun != wantEnv {
		t.Fatalf("env\n%s\nwant\n%s", e.d.envAtRun, wantEnv)
	}
	if e.d.envModeRun != 0o600 {
		t.Fatalf("env mode %v", e.d.envModeRun)
	}
	if _, err := os.Lstat(envFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("env file left behind: %v", err)
	}
	// token and dirs
	tok := filepath.Join(p.RunsTmp, wantID, "token")
	if b, _ := os.ReadFile(tok); string(b) != "ghs_x" {
		t.Fatalf("token %q", b)
	}
	if m, g := modeOf(t, tok); m.Perm() != 0o640 || g != os.Getgid() {
		t.Fatalf("token mode %v gid %d", m, g)
	}
	if m, g := modeOf(t, filepath.Join(p.RunsTmp, wantID)); m.Perm() != 0o750 || g != os.Getgid() {
		t.Fatalf("run dir mode %v gid %d", m, g)
	}
	if m, _ := modeOf(t, filepath.Join(p.RunsVol, wantID, "log")); m.Perm() != 0o750 {
		t.Fatalf("log mode %v", m)
	}
	if m, _ := modeOf(t, filepath.Join(p.Agents, "acme", "widget", "home")); m.Perm() != 0o700 {
		t.Fatalf("home mode %v", m)
	}
	if !reflect.DeepEqual(e.minted, []string{"acme/widget"}) {
		t.Fatalf("minted %v", e.minted)
	}
	// docker sequence
	for _, c := range []string{"ImageExists " + agentimage.Tag(), "Build " + agentimage.Tag(),
		"NetworkCreate " + name + " map[jauntbox.agent:1 jauntbox.repo:acme/widget jauntbox.run:" + wantID + "]",
		"Run " + name, "Exec " + name + " " + ug + " test -f /tmp/jauntbox-ready"} {
		if e.dockerCalls(c) != 1 {
			t.Errorf("missing call %q in %v", c, e.d.calls)
		}
	}
	if e.netCall == 0 {
		t.Error("net guard not checked")
	}
	// run record
	got, err := p.Load(wantID)
	if err != nil {
		t.Fatal(err)
	}
	wantRun := Run{ID: wantID, Repo: "acme/widget", Checkout: e.root, Branch: "feat/x", DefaultBranch: "main",
		Mode: "interactive", StartedAt: at, Max: 8 * time.Hour, SkipPermissions: true}
	if !reflect.DeepEqual(got, wantRun) || !reflect.DeepEqual(r, wantRun) {
		t.Fatalf("run\n got %+v\nwant %+v", got, wantRun)
	}
	if m, _ := modeOf(t, filepath.Join(p.RunsVol, wantID, "run.json")); m.Perm() != 0o600 {
		t.Fatalf("run.json mode %v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(p.RunsVol, wantID, "run.json")); !strings.Contains(string(b), `"skip_permissions": true`) {
		t.Fatalf("run.json %s", b)
	}
	// shared root dirs created 0755, owned by "root" (the test's own uid)
	for _, d := range []string{p.RunsVol, p.Agents, p.RunsTmp} {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 || int(fi.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
			t.Fatalf("%s: %v %v", d, fi, err)
		}
	}
	if len(e.ev.ev) != 1 || e.ev.ev[0].Kind != "agent.started" ||
		!reflect.DeepEqual(e.ev.ev[0].Fields, map[string]string{"run": wantID, "repo": "acme/widget", "branch": "feat/x", "mode": "interactive"}) {
		t.Fatalf("events %+v", e.ev.ev)
	}
}

func TestStartMaxDuration(t *testing.T) {
	for _, c := range []struct {
		opt, def, want time.Duration
	}{{0, 0, 8 * time.Hour}, {0, 3 * time.Hour, 3 * time.Hour}, {2 * time.Hour, 3 * time.Hour, 2 * time.Hour}} {
		e := newTenv(t, "ref: refs/heads/feat/x\n")
		e.s.DefaultMax = c.def
		r, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Max: c.opt})
		if err != nil || r.Max != c.want {
			t.Errorf("%v/%v: max %v err %v", c.opt, c.def, r.Max, err)
		}
	}
}

func TestStartBranchChoice(t *testing.T) {
	cases := []struct {
		name, head, flag string
		switchTo, branch string
	}{
		{"feature branch", "ref: refs/heads/feat/x\n", "", "", "feat/x"},
		{"default branch", "ref: refs/heads/main\n", "", "agent/widget-20261111", "agent/widget-20261111"},
		{"detached", "0123456789abcdef0123456789abcdef01234567\n", "", "agent/widget-20261111", "agent/widget-20261111"},
		{"flag", "ref: refs/heads/main\n", "fix/y", "fix/y", "fix/y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTenv(t, c.head)
			r, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Branch: c.flag})
			if err != nil {
				t.Fatal(err)
			}
			env := envLines(e.d.envAtRun)
			if v, ok := env["JAUNT_SWITCH_TO"]; v != c.switchTo || ok != (c.switchTo != "") {
				t.Fatalf("JAUNT_SWITCH_TO %q (%v)", v, ok)
			}
			if r.Branch != c.branch {
				t.Fatalf("branch %q", r.Branch)
			}
		})
	}
	t.Run("default branch flag refused", func(t *testing.T) {
		e := newTenv(t, "ref: refs/heads/feat/x\n")
		if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Branch: "main"}); err == nil || !strings.Contains(err.Error(), "default branch") {
			t.Fatalf("err %v", err)
		}
		e.assertNothingCreated(t)
	})
	t.Run("bad branch name refused", func(t *testing.T) {
		for _, b := range []string{"-x", "a..b", "a b", "a\nb", "x.lock", "/x", "x/"} {
			e := newTenv(t, "ref: refs/heads/feat/x\n")
			if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Branch: b}); err == nil {
				t.Fatalf("%q accepted", b)
			}
			e.assertNothingCreated(t)
		}
	})
}

func TestStartRefusesDefaultBranch(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/main\n")
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "main is the default branch") {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
}

func TestStartRefusesForeignCommondir(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	cd := filepath.Join(e.root, ".git", "commondir")
	writeFile(t, cd, "../../elsewhere/.git\n")
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), cd) || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("err %v", err)
	}
	if len(e.d.calls) != 0 {
		t.Fatalf("docker calls %v", e.d.calls)
	}
	// an existing "." is fine
	e2 := newTenv(t, "ref: refs/heads/feat/x\n")
	writeFile(t, filepath.Join(e2.root, ".git", "commondir"), ".\n")
	if _, err := e2.s.Start(context.Background(), StartOpts{Dir: e2.root}); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusesSymlinkedGuardFiles(t *testing.T) {
	for _, name := range []string{"hooks", "commondir", "config", "modules"} {
		t.Run(name, func(t *testing.T) {
			e := newTenv(t, "ref: refs/heads/feat/x\n")
			target := filepath.Join(e.home, "target-"+name) // does not exist: a followed link would create it
			p := filepath.Join(e.root, ".git", name)
			os.Remove(p)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("err %v", err)
			}
			if _, err := os.Lstat(target); err == nil {
				t.Fatal("symlink was followed")
			}
			if len(e.d.calls) != 0 {
				t.Fatalf("docker calls %v", e.d.calls)
			}
		})
	}
}

// The creation step itself never follows a link planted after validation.
func TestEnsureGuardFilesDoesNotFollowSymlinks(t *testing.T) {
	for _, name := range []string{"hooks", "commondir"} {
		t.Run(name, func(t *testing.T) {
			home := realTemp(t)
			root := filepath.Join(home, "r")
			mkCheckout(t, root, "https://github.com/o/r", "ref: refs/heads/main\n")
			target := filepath.Join(home, "target")
			if err := os.Symlink(target, filepath.Join(root, ".git", name)); err != nil {
				t.Fatal(err)
			}
			if err := ensureGuardFiles(home, root, os.Getuid(), os.Getgid()); err == nil {
				t.Fatal("symlink accepted")
			}
			if _, err := os.Lstat(target); err == nil {
				t.Fatal("symlink followed")
			}
		})
	}
	t.Run("symlinked path component", func(t *testing.T) {
		home := realTemp(t)
		real := filepath.Join(home, "real")
		mkCheckout(t, real, "https://github.com/o/r", "ref: refs/heads/main\n")
		if err := os.Symlink(real, filepath.Join(home, "r")); err != nil {
			t.Fatal(err)
		}
		if err := ensureGuardFiles(home, filepath.Join(home, "r"), os.Getuid(), os.Getgid()); err == nil {
			t.Fatal("symlinked checkout accepted")
		}
		if _, err := os.Lstat(filepath.Join(real, ".git", "commondir")); err == nil {
			t.Fatal("created through a symlink")
		}
	})
}

func TestStartModulesMountOnlyWhenPresent(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(e.d.specs[0].Args(), " "), ".git/modules") {
		t.Fatal("modules mounted without a modules dir")
	}
	e2 := newTenv(t, "ref: refs/heads/feat/x\n")
	if err := os.Mkdir(filepath.Join(e2.root, ".git", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.s.Start(context.Background(), StartOpts{Dir: e2.root}); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(e2.d.specs[0].Args(), " ")
	want := "-v " + e2.root + "/.git/hooks:/work/.git/hooks:ro -v " + e2.root + "/.git/modules:/work/.git/modules:ro -v " + e2.root + "/.git/commondir:/work/.git/commondir:ro"
	if !strings.Contains(args, want) {
		t.Fatalf("args %s", args)
	}
}

func TestStartUnattendedPassesPrompt(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	r, err := e.s.Start(context.Background(), StartOpts{Dir: e.root, Prompt: "fix the flaky test"})
	if err != nil {
		t.Fatal(err)
	}
	args := e.d.specs[0].Args()
	if args[len(args)-1] != "jaunt-unattended" || args[len(args)-2] != agentimage.Tag() {
		t.Fatalf("args %q", args)
	}
	if envLines(e.d.envAtRun)["JAUNT_PROMPT"] != "fix the flaky test" {
		t.Fatalf("env %s", e.d.envAtRun)
	}
	if r.Mode != "unattended" || r.Prompt != "fix the flaky test" {
		t.Fatalf("run %+v", r)
	}
	e2 := newTenv(t, "ref: refs/heads/feat/x\n")
	if _, err := e2.s.Start(context.Background(), StartOpts{Dir: e2.root, Prompt: "a\nJAUNT_SWITCH_TO=main"}); err == nil {
		t.Fatal("multi-line prompt accepted")
	}
	e2.assertNothingCreated(t)
}

func TestStartLocked(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	os.Remove(filepath.Join(e.s.SecretsDir, ".loaded"))
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
	if e.netCall != 0 {
		t.Fatal("net guard touched")
	}
}

func TestStartMissingClaudeToken(t *testing.T) {
	const msg = "missing agent credential CLAUDE_CODE_OAUTH_TOKEN: run `jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN` then `jauntbox unlock` on your laptop"
	for name, content := range map[string]*string{"absent key": ptr("OTHER=y\n"), "empty value": ptr("CLAUDE_CODE_OAUTH_TOKEN=\n"), "no file": nil} {
		t.Run(name, func(t *testing.T) {
			e := newTenv(t, "ref: refs/heads/feat/x\n")
			p := filepath.Join(e.s.SecretsDir, "agents.env")
			if content == nil {
				os.Remove(p)
			} else {
				writeFile(t, p, *content)
			}
			_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
			if err == nil || err.Error() != msg {
				t.Fatalf("err %v", err)
			}
			e.assertNothingCreated(t)
		})
	}
}

func ptr(s string) *string { return &s }

func TestStartNeedsGitIdentity(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.s.GitConfig = func(k string) (string, error) {
		if k == "user.email" {
			return "", errors.New("exit status 1")
		}
		return "Alice", nil
	}
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "git config --global user.email") {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
}

func TestStartNotACheckout(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	dir := filepath.Join(e.home, "notes")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Start(context.Background(), StartOpts{Dir: dir}); err == nil || !strings.Contains(err.Error(), "not inside a git checkout") {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
}

func TestStartRepoNotInstalled(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.s.Repos = func(context.Context) ([]forge.Repo, error) {
		return []forge.Repo{{Owner: "acme", Name: "gadget", DefaultBranch: "main"}}, nil
	}
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	want := "the App jauntbox-alice is not installed on acme/widget; add it at https://github.com/apps/jauntbox-alice/installations/new"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
	// the App's real slug wins when the vault knows it
	writeFile(t, filepath.Join(e.s.SecretsDir, "github-app.json"), `{"slug":"jauntbox-acme-2"}`)
	_, err = e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "https://github.com/apps/jauntbox-acme-2/installations/new") {
		t.Fatalf("err %v", err)
	}
}

func TestStartRefusesSecondRunInCheckout(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root}); err != nil {
		t.Fatal(err)
	}
	e.s.Now = func() time.Time { return at.Add(time.Minute) }
	e.d.calls = nil
	_, err := e.s.Start(context.Background(), StartOpts{Dir: filepath.Join(e.root, ".git", "..")})
	want := "run " + wantID + " is already active in " + e.root + "; reopen it with `jauntctl agent attach`"
	if err == nil || err.Error() != want {
		t.Fatalf("err %v", err)
	}
	if e.dockerCalls("Run ") != 0 || e.dockerCalls("NetworkCreate") != 0 {
		t.Fatalf("calls %v", e.d.calls)
	}
	// once its container is gone the checkout is free again
	delete(e.d.present, "jauntbox-"+wantID)
	if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root}); err != nil {
		t.Fatal(err)
	}
}

func TestStartAllowsRunsInTwoCheckouts(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	if _, err := e.s.Start(context.Background(), StartOpts{Dir: e.root}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.home, "src", "gadget")
	mkCheckout(t, other, "https://github.com/acme/gadget", "ref: refs/heads/trunk\n")
	r, err := e.s.Start(context.Background(), StartOpts{Dir: other})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "gadget-20261110-233000" || r.Branch != "agent/gadget-20261111" || r.DefaultBranch != "trunk" {
		t.Fatalf("run %+v", r)
	}
	all, _ := e.s.Paths.All()
	if len(all) != 2 {
		t.Fatalf("runs %+v", all)
	}
}

func checkCleanedUp(t *testing.T, e *tenv) {
	t.Helper()
	name := "jauntbox-" + wantID
	if e.dockerCalls("Remove "+name) != 1 || e.dockerCalls("NetworkRemove "+name) != 1 {
		t.Fatalf("calls %v", e.d.calls)
	}
	p := e.s.Paths
	if _, err := os.Lstat(filepath.Join(p.RunsTmp, wantID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tmpfs run dir left behind")
	}
	if _, err := os.Lstat(filepath.Join(p.RunsTmp, wantID+".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("env file left behind")
	}
	if _, err := os.Stat(filepath.Join(p.RunsVol, wantID, "log")); err != nil {
		t.Fatal("volume log removed")
	}
	if _, err := p.Load(wantID); err == nil {
		t.Fatal("run saved")
	}
	if len(e.ev.ev) != 0 {
		t.Fatalf("events %v", e.ev.ev)
	}
}

func TestStartInitFailureCleansUp(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.d.stopped = true
	var log strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&log, "line %02d\n", i)
	}
	e.d.initLog = log.String()
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "line 11\n") || !strings.Contains(err.Error(), "line 30") || strings.Contains(err.Error(), "line 10") {
		t.Fatalf("err %v", err)
	}
	checkCleanedUp(t, e)
}

func TestStartReadyTimeoutCleansUp(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.d.neverReady = true
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err %v", err)
	}
	checkCleanedUp(t, e)
}

func TestStartRunFailureCleansUp(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.d.runErr = errors.New("docker: boom")
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	checkCleanedUp(t, e)
}

func TestStartInitLogSymlinkNotFollowed(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.d.stopped = true
	secret := filepath.Join(e.home, "secret")
	writeFile(t, secret, "TOPSECRET\n")
	// the container replaces init.log with a link to a file it cannot read
	e.s.Docker = &linkingDocker{fakeDocker: e.d, target: secret}
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err %v", err)
	}
}

type linkingDocker struct {
	*fakeDocker
	target string
}

func (l *linkingDocker) Run(ctx context.Context, spec ContainerSpec) error {
	err := l.fakeDocker.Run(ctx, spec)
	for _, m := range spec.Mounts {
		if m.Dst == "/var/log/jauntbox" {
			os.Symlink(l.target, filepath.Join(m.Src, "init.log"))
		}
	}
	return err
}

func TestStartNetGuardFailureRefuses(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.s.Net = func(context.Context, string, ...string) error { return errors.New("iptables: no chain") }
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "iptables: no chain") {
		t.Fatalf("err %v", err)
	}
	e.assertNothingCreated(t)
}

func TestStartRootDirs(t *testing.T) {
	t.Run("group-writable refused", func(t *testing.T) {
		e := newTenv(t, "ref: refs/heads/feat/x\n")
		if err := os.Mkdir(e.s.Paths.Agents, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(e.s.Paths.Agents, 0o775); err != nil {
			t.Fatal(err)
		}
		_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
		if err == nil || !strings.Contains(err.Error(), e.s.Paths.Agents+" must be owned by root") {
			t.Fatalf("err %v", err)
		}
		e.assertNothingCreated(t)
		if e.netCall != 0 {
			t.Fatal("went on after the refusal")
		}
	})
	t.Run("not root-owned refused", func(t *testing.T) {
		e := newTenv(t, "ref: refs/heads/feat/x\n")
		e.s.rootUID = os.Getuid() + 1
		_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
		parent := filepath.Dir(e.s.Paths.RunsVol)
		if err == nil || !strings.Contains(err.Error(), parent+" must be owned by root") {
			t.Fatalf("err %v", err)
		}
		e.assertNothingCreated(t)
	})
	t.Run("symlink refused", func(t *testing.T) {
		e := newTenv(t, "ref: refs/heads/feat/x\n")
		elsewhere := realTemp(t)
		if err := os.Symlink(elsewhere, e.s.Paths.RunsVol); err != nil {
			t.Fatal(err)
		}
		_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
		if err == nil || !strings.Contains(err.Error(), e.s.Paths.RunsVol+" is not a directory") {
			t.Fatalf("err %v", err)
		}
		if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
			t.Fatal("wrote through the link")
		}
	})
}

func TestStartConcurrentStartInCheckoutRefused(t *testing.T) {
	e := newTenv(t, "ref: refs/heads/feat/x\n")
	e.d.inExec, e.d.gate = make(chan struct{}, 1), make(chan struct{})
	type res struct {
		r   Run
		err error
	}
	first := make(chan res, 1)
	go func() {
		r, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
		first <- res{r, err}
	}()
	<-e.d.inExec // the first start holds the lock, waiting for ready
	_, err := e.s.Start(context.Background(), StartOpts{Dir: e.root})
	want := "a run is being started in " + e.root + "; wait for it, then reopen it with `jauntctl agent attach`"
	if err == nil || err.Error() != want {
		t.Fatalf("second start: %v", err)
	}
	close(e.d.gate)
	if r := <-first; r.err != nil || r.r.ID != wantID {
		t.Fatalf("first start: %+v", r)
	}
	if n := e.dockerCalls("Run "); n != 1 {
		t.Fatalf("%d containers started", n)
	}
	// after the first start finished, the lock is free and the active-run check answers
	_, err = e.s.Start(context.Background(), StartOpts{Dir: e.root})
	if err == nil || !strings.Contains(err.Error(), "is already active") {
		t.Fatalf("third start: %v", err)
	}
}
