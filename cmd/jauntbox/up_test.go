package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/client"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
)

// --- fakes -----------------------------------------------------------------

type tLog struct{ lines []string }

func (l *tLog) add(s string) { l.lines = append(l.lines, s) }
func (l *tLog) count(s string) int {
	n := 0
	for _, x := range l.lines {
		if x == s {
			n++
		}
	}
	return n
}
func (l *tLog) index(s string) int {
	for i, x := range l.lines {
		if x == s {
			return i
		}
	}
	return -1
}

type tProv struct {
	box *provider.Box
	log *tLog
	// onPolicy-like hook: called at the start of EnsureSSHKey.
	hook func()
}

func (p *tProv) EnsureSSHKey(context.Context, string, string, map[string]string) (int64, error) {
	if p.hook != nil {
		p.hook()
	}
	return 1, nil
}
func (p *tProv) EnsureFirewall(context.Context, string, map[string]string) (int64, error) {
	return 2, nil
}
func (p *tProv) EnsureVolume(context.Context, string, int, string, map[string]string) (provider.Volume, error) {
	return provider.Volume{ID: 3, Device: "/dev/x"}, nil
}
func (p *tProv) FindBox(context.Context, map[string]string) (*provider.Box, error) {
	return p.box, nil
}
func (p *tProv) CreateBox(_ context.Context, s provider.CreateBoxSpec) (provider.Box, error) {
	b := provider.Box{ID: 9, Name: s.Name, Status: "running", ServerType: s.ServerType, Created: time.Now()}
	p.box = &b
	return b, nil
}
func (p *tProv) DeleteBox(context.Context, int64) error { p.box = nil; return nil }
func (p *tProv) WaitDeleted(context.Context, int64) error {
	p.box = nil
	return nil
}
func (p *tProv) WaitVolumeDetached(context.Context, int64) error { return nil }

type tMesh struct {
	online func() bool
}

func (m *tMesh) CreateAuthKey(context.Context, []string, time.Duration) (string, error) {
	return "k", nil
}
func (m *tMesh) DeviceOnline(context.Context, string) (bool, error) {
	if m.online != nil {
		return m.online(), nil
	}
	return true, nil
}

type tRemote struct {
	log        *tLog
	failUnlock bool
	boxStatus  string
}

func (r *tRemote) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	j := strings.Join(args, " ")
	if r.failUnlock && strings.Contains(j, ".loaded") {
		return nil, errors.New("boom")
	}
	if strings.Contains(j, "box-status") {
		return []byte(r.boxStatus), nil
	}
	return nil, nil
}
func (r *tRemote) RunInput(context.Context, string, []byte, ...string) ([]byte, error) {
	return nil, nil
}
func (r *tRemote) Copy(context.Context, string, string, string) error { return nil }

type tSink struct{}

func (tSink) Emit(events.Event) error { return nil }

type fx struct {
	d     client.Deps
	prov  *tProv
	mesh  *tMesh
	rem   *tRemote
	log   *tLog
	spawn []string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	cfg := config.Default()
	cfg.User = "alice"
	cfg.Timezone = "Europe/Paris"
	cfg.SSHPublicKey = "ssh-ed25519 AAAA"
	f := &fx{log: &tLog{}}
	f.prov, f.mesh = &tProv{log: f.log}, &tMesh{}
	f.rem = &tRemote{log: f.log}
	f.d = client.Deps{
		Cfg: cfg, Policy: policy.Local{Cfg: cfg}, Prov: f.prov, Mesh: f.mesh,
		Remote: f.rem, Root: f.rem, Events: tSink{},
		Secrets: secrets.Store{Dir: t.TempDir()},
		Now:     time.Now,
		Spawn: func(args ...string) error {
			f.log.add("spawn:" + strings.Join(args, " "))
			f.spawn = append(f.spawn, strings.Join(args, " "))
			return nil
		},
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return f
}

func (f *fx) load(string) (client.Deps, error) { return f.d, nil }

// run executes root with args, returning stdout, stderr and the error.
func (f *fx) run(ctx context.Context, args ...string) (string, string, error) {
	root := newClientCmdWith(f.load)
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"--config", "x.yaml"}, args...))
	err := root.ExecuteContext(ctx)
	return out.String(), errb.String(), err
}

func withRecorder(t *testing.T) *ui.Recorder {
	t.Helper()
	rec := &ui.Recorder{}
	old := stepsFor
	stepsFor = func(*cobra.Command, bool) ui.Steps { return rec }
	t.Cleanup(func() { stepsFor = old })
	return rec
}

// --- tests -----------------------------------------------------------------

func TestUpPlainWhenNotTTY(t *testing.T) {
	f := newFx(t)
	_, stderr, err := f.run(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("escape bytes in plain output:\n%q", stderr)
	}
	if !strings.Contains(stderr, "● Ready in") {
		t.Fatalf("no Ready line:\n%s", stderr)
	}
	if !strings.Contains(stderr, "jauntbox · alice · small (cpx42) · fsn1") {
		t.Fatalf("header:\n%s", stderr)
	}
	if strings.Contains(stderr, "·  ") || strings.Contains(stderr, "\n· ") {
		t.Fatalf("pending line left:\n%s", stderr)
	}
	if len(f.spawn) != 0 {
		t.Fatalf("foreground up spawned %v", f.spawn)
	}
}

func TestUpReadyWithWarnings(t *testing.T) {
	f := newFx(t)
	f.rem.failUnlock = true
	_, stderr, err := f.run(context.Background(), "up")
	if err != nil {
		t.Fatalf("exit must be 0: %v", err)
	}
	if !strings.Contains(stderr, ", with warnings.") {
		t.Fatalf("summary:\n%s", stderr)
	}
}

func TestUpPlansOnlyWhatRuns(t *testing.T) {
	for _, existing := range []bool{false, true} {
		f := newFx(t)
		if existing {
			f.prov.box = &provider.Box{ID: 5, Name: "jauntbox-alice", Status: "running"}
		}
		rec := withRecorder(t)
		if _, _, err := f.run(context.Background(), "up"); err != nil {
			t.Fatal(err)
		}
		var planned []string // plan events; labels may contain commas
		outcome := map[string]bool{}
		for _, e := range rec.Events {
			switch {
			case strings.HasPrefix(e, "plan "):
				planned = append(planned, strings.TrimPrefix(e, "plan "))
			case strings.HasPrefix(e, "done "), strings.HasPrefix(e, "skip "), strings.HasPrefix(e, "fail "):
				l := strings.SplitN(strings.SplitN(e, " ", 2)[1], ":", 2)[0]
				outcome[l] = true
			}
		}
		rest := strings.Join(planned, ",")
		for l := range outcome {
			rest = strings.Replace(rest, l, "", 1)
		}
		if strings.Trim(rest, ",") != "" {
			t.Errorf("existing=%v: planned labels without an outcome: %q\n%v", existing, rest, rec.Events)
		}
		if len(planned) == 0 {
			t.Errorf("existing=%v: nothing planned", existing)
		}
	}
}

func TestReadyLines(t *testing.T) {
	cfg := config.Default()
	cfg.User = "alice"
	cfg.Timezone = "Europe/Paris"
	cfg.Limits.MaxUptime = config.Duration(12 * time.Hour)
	loc, _ := time.LoadLocation("Europe/Paris")
	up := time.Date(2026, 6, 2, 9, 0, 0, 0, loc)
	sum, lines := readyLinesAt(cfg, cfg.Limits, up, up.Add(41*time.Second), up, "alice")
	if sum != "Ready in 41s." {
		t.Fatalf("summary %q", sum)
	}
	want := []string{"Deletes itself at 19:00 (evening cutoff) or after 2h idle.", "ssh jauntbox-alice"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines %q", lines)
	}
	if s, _ := readyLines(cfg, time.Now(), "alice"); !strings.HasPrefix(s, "Ready in ") {
		t.Fatalf("readyLines %q", s)
	}
}

func TestUpInterruptAfterCreateHandsOff(t *testing.T) {
	f := newFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mesh.online = func() bool {
		f.log.add("poll")
		cancel() // Ctrl-C while the foreground push waits for the tailnet
		return false
	}
	_, stderr, err := f.run(ctx, "up")
	if !errors.Is(err, errReported) {
		t.Fatalf("want errReported (exit 1, nothing more printed), got %v", err)
	}
	if f.log.count("spawn:internal-push") != 1 {
		t.Fatalf("spawns: %v", f.log.lines)
	}
	if f.log.index("spawn:internal-push") < f.log.index("poll") {
		t.Fatalf("spawned before the foreground push returned: %v", f.log.lines)
	}
	iv := strings.Index(stderr, "Joined the tailnet")
	ii := strings.Index(stderr, "interrupted")
	in := strings.Index(stderr, "the box keeps getting ready in the background; log: ")
	if ii < 0 || in < ii || iv < 0 || !strings.Contains(stderr, "push.log") {
		t.Fatalf("want the interrupted line, then the note:\n%s", stderr)
	}
	if strings.Contains(stderr, "context canceled") {
		t.Fatalf("context canceled printed:\n%s", stderr)
	}
}

func TestUpExistingBoxDeadlineWording(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	at := func(d, h int) time.Time { return time.Date(2026, 6, d, h, 0, 0, 0, loc) }
	cases := []struct {
		name         string
		created, now time.Time
		want         string
		noCutoff     bool
		maxUp        time.Duration
	}{
		{"future today", at(2, 8), at(2, 12), "Deletes itself at 18:00 (max uptime) or after 2h idle.", false, 0},
		{"future another day", at(2, 16), at(2, 17), "Deletes itself at tomorrow 02:00 (max uptime) or after 2h idle.", true, 0},
		{"future in three days", at(2, 8), at(2, 9), "Deletes itself at Thu 4 Jun 20:00 (max uptime) or after 2h idle.", true, 60 * time.Hour},
		{"passed yesterday", at(1, 8), at(2, 12), "Past its deadline (18:00 yesterday, max uptime); deletes itself once you're idle for 30m.", false, 0},
		{"passed today", at(2, 8), at(2, 20), "Past its deadline (18:00, max uptime); deletes itself once you're idle for 30m.", false, 0},
		{"passed days ago", at(1, 8), at(4, 12), "Past its deadline (18:00 Mon 1 Jun, max uptime); deletes itself once you're idle for 30m.", false, 0},
	}
	for _, c := range cases {
		f := newFx(t)
		f.d.Cfg.Limits.MaxUptime = config.Duration(10 * time.Hour)
		f.d.Cfg.Limits.EveningCutoff = "19:00"
		if c.noCutoff {
			f.d.Cfg.Limits.EveningCutoff = ""
		}
		if c.maxUp > 0 {
			f.d.Cfg.Limits.MaxUptime = config.Duration(c.maxUp)
		}
		f.d.Policy = policy.Local{Cfg: f.d.Cfg}
		now := c.now
		f.d.Now = func() time.Time { return now }
		f.prov.box = &provider.Box{ID: 5, Name: "jauntbox-alice", Status: "running", Created: c.created}
		_, stderr, err := f.run(context.Background(), "up")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr, c.want) {
			t.Errorf("%s: want %q in:\n%s", c.name, c.want, stderr)
		}
	}
}

func TestUpReadyUsesPolicyLimits(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	f := newFx(t)
	f.d.Cfg.Limits.MaxUptime = config.Duration(10 * time.Hour)
	f.d.Cfg.Limits.EveningCutoff = "23:00"
	pl := f.d.Cfg.Limits
	pl.MaxUptime = config.Duration(4 * time.Hour)
	f.d.Policy = policy.Local{Cfg: func() config.Config { c := f.d.Cfg; c.Limits = pl; return c }()}
	f.d.Now = func() time.Time { return time.Date(2026, 6, 2, 9, 0, 0, 0, loc) }
	_, stderr, err := f.run(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Deletes itself at 13:00 (max uptime)") {
		t.Fatalf("policy limits not used:\n%s", stderr)
	}
}

func TestUpInterruptBeforeCreateNoSpawn(t *testing.T) {
	f := newFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.d.Policy = cancelPolicy{cancel: cancel}
	if _, _, err := f.run(ctx, "up"); err == nil {
		t.Fatal("want error")
	}
	if len(f.spawn) != 0 {
		t.Fatalf("spawned %v before the server existed", f.spawn)
	}
}

type cancelPolicy struct{ cancel func() }

func (c cancelPolicy) Load(ctx context.Context) (policy.Policy, error) {
	c.cancel()
	return policy.Policy{}, ctx.Err()
}

func TestUpDetach(t *testing.T) {
	f := newFx(t)
	f.mesh.online = func() bool { t.Error("detach must not wait for the tailnet"); return false }
	stdout, stderr, err := f.run(context.Background(), "up", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.spawn) != 1 || f.spawn[0] != "internal-push" {
		t.Fatalf("spawn %v", f.spawn)
	}
	all := stdout + stderr
	if !strings.Contains(all, "installing the supervisor in the background; log: ") || !strings.Contains(all, "push.log") {
		t.Fatalf("output:\n%s", all)
	}
	if strings.Contains(all, "● Ready") {
		t.Fatalf("detach is not ready:\n%s", all)
	}
}

func TestUpWaitFlagStillAccepted(t *testing.T) {
	f := newFx(t)
	if _, _, err := f.run(context.Background(), "up", "--wait"); err != nil {
		t.Fatal(err)
	}
}

func TestDownNoBoxPlain(t *testing.T) {
	f := newFx(t)
	_, stderr, err := f.run(context.Background(), "down")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "no box running") || strings.Contains(stderr, "Box deleted. The home volume") {
		t.Fatalf("stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "jauntbox down · alice") {
		t.Fatalf("header:\n%s", stderr)
	}
}

func TestDownEndLine(t *testing.T) {
	f := newFx(t)
	f.prov.box = &provider.Box{ID: 5, Name: "jauntbox-alice"}
	_, stderr, err := f.run(context.Background(), "down", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "● Box deleted. The home volume is kept.") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

const sampleBoxStatus = `{"box":{},"state":{},"verdict":{"action":"keep","reason":"human active"},"deadline":{"time":"2026-06-02T19:00:00+02:00","reason":"evening-cutoff"},"locked":false}`

func statusFx(t *testing.T) *fx {
	f := newFx(t)
	f.prov.box = &provider.Box{ID: 5, Name: "jauntbox-alice", Status: "running", ServerType: "cpx42",
		Created: time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)}
	f.rem.boxStatus = sampleBoxStatus
	return f
}

func TestStatusJSONFlag(t *testing.T) {
	f := statusFx(t)
	stdout, _, err := f.run(context.Background(), "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not only JSON: %v\n%s", err, stdout)
	}
	if _, ok := v["verdict"]; !ok {
		t.Fatalf("json: %s", stdout)
	}
}

func TestStatusSummary(t *testing.T) {
	f := statusFx(t)
	stdout, _, err := f.run(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"jauntbox-alice  running  cpx42",
		"created ",
		"tailnet  ✓",
		"secrets  unlocked",
		"supervisor  keep (human active), next deadline 19:00 (evening cutoff)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("escape in non-tty status: %q", stdout)
	}
}

func TestStatusKeepsUnlockWarning(t *testing.T) {
	f := statusFx(t)
	f.rem.failUnlock = true
	_, stderr, err := f.run(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "warning: could not unlock the box: ") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestStatusNoBox(t *testing.T) {
	f := newFx(t)
	stdout, _, _ := f.run(context.Background(), "status")
	if !strings.Contains(stdout, "no box running") {
		t.Fatalf("stdout: %s", stdout)
	}
}

func TestPlainFlagForcesPlain(t *testing.T) {
	old := isTerminal
	isTerminal = func(int) bool { return true }
	defer func() { isTerminal = old }()
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "")
	tmp, err := os.Create(filepath.Join(t.TempDir(), "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	cmd := &cobra.Command{}
	cmd.SetErr(tmp)
	live := newSteps(cmd, false)
	live.Start("working") // an unfinished step makes the live block redraw with cursor codes
	live.Close()
	if lb, _ := os.ReadFile(tmp.Name()); !strings.Contains(string(lb), "\x1b") {
		t.Fatalf("without --plain a terminal gets the live renderer: %q", lb)
	}
	tmp.Truncate(0)
	tmp.Seek(0, 0)
	// live: a header carries ANSI/colour only in live mode; plain never does.
	s := newSteps(cmd, true)
	s.Header("jauntbox", "x")
	s.Close()
	b, _ := os.ReadFile(tmp.Name())
	if strings.Contains(string(b), "\x1b") || !strings.Contains(string(b), "jauntbox · x") {
		t.Fatalf("--plain output: %q", b)
	}
	if root := newClientCmd(); root.PersistentFlags().Lookup("plain") == nil {
		t.Fatal("no --plain flag")
	}
}

func stepEvents(rec *ui.Recorder) []string {
	var out []string
	for _, e := range rec.Events {
		if strings.HasPrefix(e, "start ") {
			out = append(out, strings.TrimPrefix(e, "start "))
		}
	}
	return out
}

func TestUpNewBoxStepsEndWithDevTools(t *testing.T) {
	f := newFx(t)
	rec := withRecorder(t)
	if _, _, err := f.run(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(stepEvents(rec), " | ")
	want := "Joined the tailnet | Box reachable | Install supervisor | Unlock secrets | Dev tools installed"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("steps = %s", got)
	}
	di, ei := -1, -1
	for i, e := range rec.Events {
		if e == "done Dev tools installed" {
			di = i
		}
		if strings.HasPrefix(e, "end ok") {
			ei = i
		}
	}
	if di < 0 || ei != di+1 {
		t.Fatalf("Ready must directly follow the dev tools: %v", rec.Events)
	}
	if strings.Contains(got, "Provisioning") || strings.Contains(got, "Box provisioned") {
		t.Fatalf("old labels: %s", got)
	}
}

func TestUpExistingBoxWaitsForDevTools(t *testing.T) {
	f := newFx(t)
	f.prov.box = &provider.Box{ID: 5, Name: "jauntbox-alice", Status: "running"}
	rec := withRecorder(t)
	if _, _, err := f.run(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(stepEvents(rec), " | ")
	if !strings.HasSuffix(got, "Box found | Box online | Dev tools installed | Install supervisor | Unlock secrets") {
		t.Fatalf("steps = %s", got)
	}
}
