package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/agent"
	"github.com/WA-Systems-EU/jaunt/internal/agentrun"
	"github.com/WA-Systems-EU/jaunt/internal/events"
)

// agentFakeDocker is a minimal agentrun.Docker for the command tests.
type agentFakeDocker struct {
	calls   []string
	running map[string]bool // container name -> running
	labeled []string        // run ids ListRuns reports
	hasCont bool            // `test -f /work/CONTINUATION.md` succeeds
}

func (f *agentFakeDocker) rec(s string) { f.calls = append(f.calls, s) }
func (f *agentFakeDocker) ImageExists(context.Context, string) (bool, error) {
	return true, nil
}
func (f *agentFakeDocker) Build(context.Context, string, []byte) error { return nil }
func (f *agentFakeDocker) NetworkCreate(context.Context, string, string, map[string]string) error {
	return nil
}
func (f *agentFakeDocker) NetworkRemove(_ context.Context, n string) error {
	f.rec("NetworkRemove " + n)
	return nil
}
func (f *agentFakeDocker) Run(context.Context, agentrun.ContainerSpec) error { return nil }
func (f *agentFakeDocker) Exec(_ context.Context, c, user string, args ...string) ([]byte, error) {
	f.rec("Exec " + c + " " + user + " " + strings.Join(args, " "))
	if len(args) > 0 && args[0] == "test" && !f.hasCont {
		return nil, errors.New("exit 1")
	}
	return nil, nil
}
func (f *agentFakeDocker) Inspect(_ context.Context, n string) (agentrun.State, error) {
	r, ok := f.running[n]
	if !ok {
		return agentrun.State{}, nil
	}
	return agentrun.State{Exists: true, Running: r, IP: "172.18.0.2"}, nil
}
func (f *agentFakeDocker) Logs(context.Context, string, int) (string, error) { return "", nil }
func (f *agentFakeDocker) Remove(_ context.Context, n string) error {
	f.rec("Remove " + n)
	return nil
}
func (f *agentFakeDocker) Stop(_ context.Context, n string, _ time.Duration) error {
	f.rec("Stop " + n)
	return nil
}
func (f *agentFakeDocker) ListRuns(context.Context) ([]string, error) { return f.labeled, nil }

type execCall struct {
	path string
	argv []string
}

type agentEnv struct {
	d      *agentDeps
	docker *agentFakeDocker
	home   string
	root   string // the checkout
	execs  []execCall
	sudos  [][]string
	out    *bytes.Buffer
	errOut *bytes.Buffer
	reply  agentrun.Run // what the fake `sudo … start-run` prints
}

func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// newAgentEnv has a checkout <home>/src/widget on branch head, a box user
// alice with that home, and fakes for exec, sudo and Docker. euid picks the
// side: 0 is the root part, anything else the user part.
func newAgentEnv(t *testing.T, euid int, head string) *agentEnv {
	t.Helper()
	home := realTempDir(t)
	root := filepath.Join(home, "src", "widget")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = git@github.com:acme/widget.git\n"
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	base := realTempDir(t)
	e := &agentEnv{docker: &agentFakeDocker{running: map[string]bool{}}, home: home, root: root, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	e.d = &agentDeps{
		euid:     func() int { return euid },
		getwd:    func() (string, error) { return root, nil },
		userHome: func() (string, error) { return home, nil },
		lookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		exec: func(path string, argv, _ []string) error {
			e.execs = append(e.execs, execCall{path, argv})
			return nil
		},
		sudo: func(args []string, stdout io.Writer) error {
			e.sudos = append(e.sudos, args)
			return json.NewEncoder(stdout).Encode(e.reply)
		},
		docker:     e.docker,
		paths:      agentrun.Paths{RunsTmp: filepath.Join(base, "run"), RunsVol: filepath.Join(base, "vol"), Agents: filepath.Join(base, "agents")},
		lookupUser: func() (sudoUser, error) { return sudoUser{Name: "alice", Home: home, UID: 1000, GID: 1000}, nil },
		events:     events.Discard,
		adapter:    agent.ClaudeCode{},
		now:        func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) },
		poll:       time.Millisecond,
	}
	return e
}

func (e *agentEnv) run(t *testing.T, args ...string) error {
	t.Helper()
	c := agentCmd(e.d)
	c.SetArgs(args)
	c.SilenceUsage, c.SilenceErrors = true, true // as under the jauntctl root
	c.SetOut(e.out)
	c.SetErr(e.errOut)
	return c.Execute()
}

// addRun saves a run of the checkout with a running, labelled container.
func (e *agentEnv) addRun(t *testing.T, id string) agentrun.Run {
	t.Helper()
	r := agentrun.Run{ID: id, Repo: "acme/widget", Checkout: e.root, Branch: "feat/x", Mode: agentrun.ModeInteractive,
		StartedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), Max: 8 * time.Hour}
	if err := e.d.paths.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.d.paths.RunsTmp, id), 0o750); err != nil {
		t.Fatal(err)
	}
	e.docker.running[agentrun.ContainerName(id)] = true
	e.docker.labeled = append(e.docker.labeled, id)
	return r
}

const runID = "widget-20261111-090000"

func TestAgentRunsRootPartWithSudo(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	e.reply = agentrun.Run{ID: runID, Checkout: e.root, Branch: "feat/x", Mode: agentrun.ModeUnattended}
	if err := e.run(t, "--unattended", "fix the tests", "--branch", "feat/x", "--max", "2h"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"agent", "start-run", "--dir", e.root, "--branch", "feat/x", "--unattended", "fix the tests", "--max", "2h0m0s", "--json"}}
	if !reflect.DeepEqual(e.sudos, want) {
		t.Fatalf("sudo calls\n got %q\nwant %q", e.sudos, want)
	}
	if len(e.execs) != 0 {
		t.Fatalf("unattended run exec'd %v", e.execs)
	}
	if got := e.out.String(); got != "run "+runID+" started; watch it with: jauntctl agent logs --follow\n" {
		t.Fatalf("output %q", got)
	}
}

func TestAgentInteractiveExecsAttach(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/main\n")
	e.reply = agentrun.Run{ID: runID, Checkout: e.root, Branch: "agent/widget-20261111", Mode: agentrun.ModeInteractive}
	if err := e.run(t, "--skip-permissions"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"agent", "start-run", "--dir", e.root, "--skip-permissions", "--json"}; !reflect.DeepEqual(e.sudos[0], want) {
		t.Fatalf("start-run argv %q", e.sudos[0])
	}
	want := []execCall{{"/usr/bin/sudo", []string{"sudo", "-n", "/usr/local/bin/jauntctl", "agent", "attach", runID}}}
	if !reflect.DeepEqual(e.execs, want) {
		t.Fatalf("exec\n got %q\nwant %q", e.execs, want)
	}
	if got := e.out.String(); got != "switched "+e.root+" to agent/widget-20261111\n" {
		t.Fatalf("output %q", got)
	}
}

func TestAgentStartRefusesUnderRoot(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	if err := e.run(t); err == nil || len(e.sudos) != 0 {
		t.Fatalf("err=%v sudo=%v", err, e.sudos)
	}
}

func TestAttachArgvFirstAndLater(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	r := e.addRun(t, runID)
	r.SkipPermissions = true
	if err := e.d.paths.Save(r); err != nil {
		t.Fatal(err)
	}
	e.docker.hasCont = true
	prefix := []string{"docker", "exec", "-it", "-w", "/work", "--user", "1000:1000"}
	t.Setenv("TERM", "xterm-256color")
	if err := e.run(t, "attach", runID); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "attach", runID); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "attach", runID, "--shell"); err != nil {
		t.Fatal(err)
	}
	name := agentrun.ContainerName(runID)
	tail := []string{"-e", "TERM=xterm-256color", name}
	want := []execCall{
		{"/usr/bin/docker", append(append(append([]string{}, prefix...), tail...), "claude", "--dangerously-skip-permissions", agent.ResumePrompt)},
		{"/usr/bin/docker", append(append(append([]string{}, prefix...), tail...), "claude", "--dangerously-skip-permissions", "--continue")},
		{"/usr/bin/docker", append(append(append([]string{}, prefix...), tail...), "bash")},
	}
	if !reflect.DeepEqual(e.execs, want) {
		t.Fatalf("exec\n got %q\nwant %q", e.execs, want)
	}
	fi, err := os.Lstat(filepath.Join(e.d.paths.RunsVol, runID, "first-attach-done"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("marker: %v", err)
	}
}

func TestAttachFirstWithoutContinuation(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	if err := e.run(t, "attach", runID); err != nil {
		t.Fatal(err)
	}
	argv := e.execs[0].argv
	if argv[len(argv)-1] != "claude" {
		t.Fatalf("argv %q", argv)
	}
}

func TestAttachWithoutIDUsesCheckoutRun(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	// an older finished run of the same checkout, and a run of another checkout
	old := e.addRun(t, "widget-20261110-090000")
	old.FinishedAt = old.StartedAt.Add(time.Hour)
	if err := e.d.paths.Save(old); err != nil {
		t.Fatal(err)
	}
	other := agentrun.Run{ID: "gadget-20261111-095000", Repo: "acme/gadget", Checkout: filepath.Join(e.home, "src", "gadget"),
		Mode: agentrun.ModeInteractive, StartedAt: time.Date(2026, 11, 11, 9, 50, 0, 0, time.UTC)}
	if err := e.d.paths.Save(other); err != nil {
		t.Fatal(err)
	}
	e.docker.running[agentrun.ContainerName(other.ID)] = true
	e.docker.labeled = append(e.docker.labeled, other.ID)
	e.addRun(t, runID)

	if err := e.run(t, "attach", "--dir", filepath.Join(e.root, "sub")); err == nil {
		t.Fatal("a directory that does not exist was accepted")
	}
	if err := os.MkdirAll(filepath.Join(e.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "attach", "--dir", filepath.Join(e.root, "sub")); err != nil {
		t.Fatal(err)
	}
	argv := e.execs[0].argv
	if argv[len(argv)-2] != agentrun.ContainerName(runID) {
		t.Fatalf("attached to %q", argv)
	}
}

func TestAttachUserSideReexecsThroughSudo(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	if err := e.run(t, "attach", "--shell"); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "attach", runID); err != nil {
		t.Fatal(err)
	}
	want := []execCall{
		{"/usr/bin/sudo", []string{"sudo", "-n", "/usr/local/bin/jauntctl", "agent", "attach", "--shell", "--dir", e.root}},
		{"/usr/bin/sudo", []string{"sudo", "-n", "/usr/local/bin/jauntctl", "agent", "attach", runID}},
	}
	if !reflect.DeepEqual(e.execs, want) {
		t.Fatalf("exec\n got %q\nwant %q", e.execs, want)
	}
	if err := e.run(t, "attach", "../../etc-20261111-090000"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestAgentRefusesOtherUsersRuns(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	// bob's run: labelled and running, but its checkout is outside alice's home
	bob := agentrun.Run{ID: "bobrepo-20261111-090000", Repo: "acme/bobrepo", Checkout: "/home/bob/src/bobrepo",
		Mode: agentrun.ModeInteractive, StartedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	if err := e.d.paths.Save(bob); err != nil {
		t.Fatal(err)
	}
	e.docker.running[agentrun.ContainerName(bob.ID)] = true
	e.docker.labeled = append(e.docker.labeled, bob.ID)
	// alice's run whose container lost (or never had) the agent label
	r := e.addRun(t, runID)
	e.docker.labeled = []string{bob.ID}

	// acting on a container needs the label too; reading needs only ownership
	for _, id := range []string{bob.ID, r.ID, "nosuch-20261111-090000"} {
		for _, args := range [][]string{{"attach", id}, {"stop", id}, {"logs", id}} {
			if args[0] == "logs" && id == r.ID {
				continue
			}
			err := e.run(t, args...)
			if err == nil || err.Error() != "no such run "+id {
				t.Errorf("%v: err = %v", args, err)
			}
		}
	}
	e.d.lookupUser = func() (sudoUser, error) {
		return sudoUser{Name: "alice", Home: e.home, UID: os.Getuid(), GID: os.Getgid()}, nil
	}
	logDir := filepath.Join(e.d.paths.RunsVol, r.ID, "log")
	os.MkdirAll(logDir, 0o750)
	os.WriteFile(filepath.Join(logDir, "claude.jsonl"), []byte("mine\n"), 0o640)
	if err := e.run(t, "logs", r.ID); err != nil || e.out.String() != "mine\n" {
		t.Fatalf("own unlabelled run's log: err=%v out=%q", err, e.out.String())
	}
	if len(e.execs) != 0 {
		t.Fatalf("exec'd %v", e.execs)
	}
	if _, err := os.Stat(filepath.Join(e.d.paths.RunsTmp, bob.ID, "wrapup")); err == nil {
		t.Fatal("bob's run got a wrap-up flag")
	}

	// list shows only alice's runs
	e.out.Reset()
	if err := e.run(t, "list", "--json"); err != nil {
		t.Fatal(err)
	}
	var sts []agentrun.Status
	if err := json.Unmarshal(e.out.Bytes(), &sts); err != nil {
		t.Fatal(err)
	}
	if len(sts) != 1 || sts[0].ID != runID {
		t.Fatalf("list %+v", sts)
	}
}

func TestAgentRootPartNeedsSudoUser(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	e.d.lookupUser = func() (sudoUser, error) { return sudoUser{}, errors.New("run through sudo") }
	for _, args := range [][]string{{"attach", runID}, {"stop", runID}, {"logs", runID}, {"start-run", "--dir", e.root}} {
		if err := e.run(t, args...); err == nil {
			t.Errorf("%v accepted without a sudo user", args)
		}
	}
}

func TestAgentStopWrapsUpAndNow(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	if err := e.run(t, "stop"); err != nil { // the run of the current checkout
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.d.paths.RunsTmp, runID, "wrapup"))
	if err != nil || string(b) != "stop-requested" {
		t.Fatalf("flag %q %v", b, err)
	}
	if err := e.run(t, "stop", runID, "--now"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.d.paths.Load(runID)
	if r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
	name := agentrun.ContainerName(runID)
	want := []string{"Exec " + name + "  jaunt-wrapup-fallback stop-requested", "Stop " + name, "Remove " + name, "NetworkRemove " + name}
	if !reflect.DeepEqual(e.docker.calls, want) {
		t.Fatalf("calls %q", e.docker.calls)
	}
}

func TestAgentLogs(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	logDir := filepath.Join(e.d.paths.RunsVol, runID, "log")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		t.Fatal(err)
	}
	e.d.lookupUser = func() (sudoUser, error) {
		return sudoUser{Name: "alice", Home: e.home, UID: os.Getuid(), GID: os.Getgid()}, nil
	}
	if err := e.run(t, "logs", runID); err == nil {
		t.Fatal("missing log accepted")
	}
	if err := os.WriteFile(filepath.Join(logDir, "claude.jsonl"), []byte("{\"a\":1}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	if err := e.run(t, "logs", runID); err != nil {
		t.Fatal(err)
	}
	if e.out.String() != "{\"a\":1}\n" {
		t.Fatalf("logs %q", e.out.String())
	}
	// --follow returns once the container stops
	e.docker.running[agentrun.ContainerName(runID)] = false
	e.out.Reset()
	if err := e.run(t, "logs", runID, "--follow"); err != nil {
		t.Fatal(err)
	}
	if e.out.String() != "{\"a\":1}\n" {
		t.Fatalf("logs --follow %q", e.out.String())
	}
	// a symlink is never followed
	os.Remove(filepath.Join(logDir, "claude.jsonl"))
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("SECRET"), 0o600)
	os.Symlink(secret, filepath.Join(logDir, "claude.jsonl"))
	e.out.Reset()
	if err := e.run(t, "logs", runID); err == nil || strings.Contains(e.out.String(), "SECRET") {
		t.Fatalf("followed a symlink: err=%v out=%q", err, e.out.String())
	}
}

func TestAgentListTable(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	r2 := e.addRun(t, "widget-20261111-100000")
	r2.HostPorts = []int{5432, 6379}
	if err := e.d.paths.Save(r2); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "list"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(e.out.String()), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "RUN REPO BRANCH MODE STATE STARTED MAX IP PORTS" {
		t.Fatalf("table:\n%s", e.out.String())
	}
	f := strings.Fields(lines[1])
	if f[0] != runID || f[1] != "acme/widget" || f[4] != "running" || f[len(f)-2] != "172.18.0.2" || f[len(f)-1] != "-" {
		t.Fatalf("row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); f[len(f)-1] != "5432,6379" {
		t.Fatalf("row %q", lines[2])
	}
}

func TestAgentListUserSideReexecs(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	if err := e.run(t, "list", "--json"); err != nil {
		t.Fatal(err)
	}
	want := []execCall{{"/usr/bin/sudo", []string{"sudo", "-n", "/usr/local/bin/jauntctl", "agent", "list", "--json"}}}
	if !reflect.DeepEqual(e.execs, want) {
		t.Fatalf("exec %q", e.execs)
	}
}

func TestLaptopAgentIsWrongSide(t *testing.T) {
	root := rootFor("jauntbox")
	root.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "c.yaml"), "agent", "--unattended", "x"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	want := "\"agent\" is a box command: run `jauntctl agent` on the box, in your repository's folder"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestJauntctlHasAgent(t *testing.T) {
	v, _ := names(rootFor("jauntctl"))
	if !v["agent"] {
		t.Fatal("jauntctl lacks agent")
	}
}

func TestAgentLogsOfFinishedRun(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.d.lookupUser = func() (sudoUser, error) {
		return sudoUser{Name: "alice", Home: e.home, UID: os.Getuid(), GID: os.Getgid()}, nil
	}
	older := e.addRun(t, "widget-20261111-080000")
	r := e.addRun(t, runID)
	for _, x := range []agentrun.Run{older, r} {
		x.FinishedAt = x.StartedAt.Add(time.Hour)
		if err := e.d.paths.Save(x); err != nil {
			t.Fatal(err)
		}
		logDir := filepath.Join(e.d.paths.RunsVol, x.ID, "log")
		if err := os.MkdirAll(logDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logDir, "claude.jsonl"), []byte(x.ID+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	// containers removed by Finish: no container, no label
	e.docker.running, e.docker.labeled = map[string]bool{}, nil
	for _, args := range [][]string{{"logs", runID}, {"logs"}, {"logs", "--follow"}} {
		e.out.Reset()
		if err := e.run(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if e.out.String() != runID+"\n" {
			t.Fatalf("%v: %q", args, e.out.String())
		}
	}
}

func TestAgentStopNowWithoutTmpfsDir(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	e.addRun(t, runID)
	if err := os.RemoveAll(filepath.Join(e.d.paths.RunsTmp, runID)); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t, "stop", runID); err == nil {
		t.Fatal("stop without --now and without a run dir succeeded")
	}
	if err := e.run(t, "stop", runID, "--now"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.d.paths.Load(runID)
	if !r.FinishedAt.Equal(e.d.now()) {
		t.Fatalf("FinishedAt %v", r.FinishedAt)
	}
}

func TestStartRunJSONIsTheOnlyStdout(t *testing.T) {
	e := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
	var got agentrun.StartOpts
	e.d.startRun = func(_ context.Context, u sudoUser, o agentrun.StartOpts) (agentrun.Run, error) {
		got = o
		return agentrun.Run{ID: runID, Repo: "acme/widget", Checkout: e.root, Branch: "feat/x", Mode: agentrun.ModeUnattended, Prompt: "p"}, nil
	}
	if err := e.run(t, "start-run", "--dir", e.root, "--unattended", "p", "--max", "2h", "--skip-permissions", "--json"); err != nil {
		t.Fatal(err)
	}
	if got.Dir != e.root || got.Prompt != "p" || got.Max != 2*time.Hour || !got.SkipPermissions {
		t.Fatalf("opts %+v", got)
	}
	// progress notes (the first image build) go to stderr, not stdout
	if got.Progress != io.Writer(e.errOut) {
		t.Fatalf("progress writer %v, want the command's stderr", got.Progress)
	}
	dec := json.NewDecoder(bytes.NewReader(e.out.Bytes()))
	dec.DisallowUnknownFields()
	var r agentrun.Run
	if err := dec.Decode(&r); err != nil || r.ID != runID {
		t.Fatalf("stdout %q: %v", e.out.String(), err)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); strings.TrimSpace(string(rest)) != "" || dec.More() {
		t.Fatalf("stdout has more than one JSON object: %q", e.out.String())
	}
	// errors go to the caller (stderr via main), never stdout
	e.out.Reset()
	e.d.startRun = func(context.Context, sudoUser, agentrun.StartOpts) (agentrun.Run, error) {
		return agentrun.Run{}, errors.New("boom")
	}
	if err := e.run(t, "start-run", "--dir", e.root, "--json"); err == nil || err.Error() != "boom" || e.out.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, e.out.String())
	}
}

func TestAgentMaxBounds(t *testing.T) {
	for _, v := range []string{"0", "-1h", "25h"} {
		e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
		err := e.run(t, "--max", v)
		if err == nil || !strings.Contains(err.Error(), "--max") || len(e.sudos) != 0 {
			t.Errorf("agent --max %s: err=%v sudos=%v", v, err, e.sudos)
		}
		r := newAgentEnv(t, 0, "ref: refs/heads/feat/x\n")
		called := false
		r.d.startRun = func(context.Context, sudoUser, agentrun.StartOpts) (agentrun.Run, error) {
			called = true
			return agentrun.Run{}, nil
		}
		err = r.run(t, "start-run", "--dir", r.root, "--max", v, "--json")
		if err == nil || !strings.Contains(err.Error(), "--max") || called {
			t.Errorf("start-run --max %s: err=%v called=%v", v, err, called)
		}
	}
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	e.reply = agentrun.Run{ID: runID, Checkout: e.root, Branch: "feat/x", Mode: agentrun.ModeUnattended}
	if err := e.run(t, "--unattended", "p", "--max", "24h"); err != nil {
		t.Fatalf("24h refused: %v", err)
	}
}

func TestAgentForwardsHostPorts(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	e.reply = agentrun.Run{ID: runID, Checkout: e.root, Branch: "feat/x", Mode: agentrun.ModeUnattended}
	if err := e.run(t, "--unattended", "x", "--host-port", "5432", "--host-port", "6379"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"agent", "start-run", "--dir", e.root, "--unattended", "x", "--host-port", "5432", "--host-port", "6379", "--json"}}
	if !reflect.DeepEqual(e.sudos, want) {
		t.Fatalf("sudo calls\n got %q\nwant %q", e.sudos, want)
	}
}

func TestAgentHostPortValidatedBeforeSudo(t *testing.T) {
	e := newAgentEnv(t, 1000, "ref: refs/heads/feat/x\n")
	err := e.run(t, "--host-port", "70000")
	if err == nil || !strings.Contains(err.Error(), "--host-port: port 70000 is out of range") {
		t.Fatalf("err %v", err)
	}
	if len(e.sudos) != 0 || len(e.execs) != 0 {
		t.Fatalf("sudo %v exec %v", e.sudos, e.execs)
	}
}
