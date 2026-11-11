package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
)

type mtEnv struct {
	m    *Maintainer
	d    *fakeDocker
	ev   *sinkRec
	p    Paths
	tok  string
	mErr error
	n    int
}

func newMT(t *testing.T, st State) *mtEnv {
	t.Helper()
	p := testPaths(t)
	activeRun(t, p)
	if err := os.WriteFile(filepath.Join(p.RunsTmp, stopID, "token"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	e := &mtEnv{p: p, tok: "old", ev: &sinkRec{}}
	e.d = &fakeDocker{states: map[string]State{ContainerName(stopID): st}}
	e.m = &Maintainer{Docker: e.d, Paths: p, Events: e.ev, Grace: 5 * time.Minute,
		Mint: func(context.Context, string) (forge.Token, error) {
			e.n++
			return forge.Token{Value: e.tok}, e.mErr
		}}
	return e
}

func (e *mtEnv) kinds() []string {
	var k []string
	for _, x := range e.ev.ev {
		k = append(k, x.Kind)
	}
	return k
}

func (e *mtEnv) has(kind string) bool {
	for _, k := range e.kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

func running() State { return State{Exists: true, Running: true} }

func TestTickRefreshesTokenOnlyWhenChanged(t *testing.T) {
	e := newMT(t, running())
	path := filepath.Join(e.p.RunsTmp, stopID, "token")
	if err := e.m.Tick(context.Background(), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if e.has("agent.token_refreshed") {
		t.Fatalf("unchanged token rewritten: %v", e.kinds())
	}
	before, _ := os.Stat(path)
	e.tok = "new"
	if err := e.m.Tick(context.Background(), at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Lstat(path)
	if string(b) != "new" || fi.Mode().Perm() != 0o640 || os.SameFile(before, fi) {
		t.Fatalf("token %q mode %v samefile %v", b, fi.Mode().Perm(), os.SameFile(before, fi))
	}
	if !e.has("agent.token_refreshed") {
		t.Fatalf("events %v", e.kinds())
	}
	if _, err := os.Stat(filepath.Join(e.p.RunsTmp, stopID, ".token.tmp")); err == nil {
		t.Fatal("temp file left behind")
	}
}

func TestTickRunMaxStartsWrapUp(t *testing.T) {
	e := newMT(t, running())
	if err := e.m.Tick(context.Background(), at.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if e.has("agent.wrapup_started") {
		t.Fatal("wrap-up before max")
	}
	now := at.Add(8 * time.Hour)
	if err := e.m.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if !r.WrapUpAt.Equal(now) || r.WrapUpReason != "run-max" || !e.has("agent.wrapup_started") {
		t.Fatalf("run %+v events %v", r, e.kinds())
	}
	b, _ := os.ReadFile(filepath.Join(e.p.RunsTmp, stopID, "wrapup"))
	if string(b) != "run-max" {
		t.Fatalf("flag %q", b)
	}
}

func TestTickGraceElapsedFinishes(t *testing.T) {
	e := newMT(t, running())
	if err := RequestWrapUp(e.p, stopID, "run-max", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.d.execOut = []byte("pushed")
	if err := e.m.Tick(context.Background(), at.Add(time.Hour+4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.p.Load(stopID); !r.FinishedAt.IsZero() {
		t.Fatal("finished before the grace elapsed")
	}
	if err := e.m.Tick(context.Background(), at.Add(time.Hour+5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if r.FinishedAt.IsZero() || !e.has("agent.stopped") {
		t.Fatalf("not finished: %+v %v", r, e.kinds())
	}
	if !strings.Contains(strings.Join(e.d.calls, "\n"), "Exec") {
		t.Fatal("fallback did not run for a running container")
	}
}

func TestTickGoneContainerFinishesWithoutFallback(t *testing.T) {
	e := newMT(t, State{})
	if err := e.m.Tick(context.Background(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
	for _, c := range e.d.calls {
		if strings.HasPrefix(c, "Exec") {
			t.Fatalf("fallback ran: %v", e.d.calls)
		}
	}
	if e.n != 0 {
		t.Fatal("minted for a gone container")
	}
}

func TestTickExitedUnattendedRecordsExitCode(t *testing.T) {
	e := newMT(t, State{Exists: true, ExitCode: 3})
	r, _ := e.p.Load(stopID)
	r.Mode = ModeUnattended
	saveRun(t, e.p, r)
	if err := e.m.Tick(context.Background(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ = e.p.Load(stopID)
	if r.FinishedAt.IsZero() || r.ExitCode != 3 {
		t.Fatalf("run %+v", r)
	}
	var fin *events.Event
	for i, x := range e.ev.ev {
		if x.Kind == "agent.finished" {
			fin = &e.ev.ev[i]
		}
	}
	if fin == nil || fin.Fields["exit_code"] != "3" || fin.Fields["run"] != stopID {
		t.Fatalf("events %+v", e.ev.ev)
	}
	for _, c := range e.d.calls {
		if strings.HasPrefix(c, "Exec") {
			t.Fatal("fallback ran")
		}
	}
}

func TestTickMintFailureContinues(t *testing.T) {
	e := newMT(t, running())
	e.mErr = errors.New("forge down")
	now := at.Add(8 * time.Hour)
	if err := e.m.Tick(context.Background(), now); err != nil {
		t.Fatalf("tick failed on a mint error: %v", err)
	}
	if !e.has("agent.token_refresh_failed") || !e.has("agent.wrapup_started") {
		t.Fatalf("events %v", e.kinds())
	}
	if b, _ := os.ReadFile(filepath.Join(e.p.RunsTmp, stopID, "token")); string(b) != "old" {
		t.Fatalf("token %q", b)
	}
}

func TestTickCorruptRecordReportedOnce(t *testing.T) {
	e := newMT(t, running())
	bad := filepath.Join(e.p.RunsVol, "broken-20261110-000000")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "run.json"), []byte("{"), 0o600)
	if err := e.m.Tick(context.Background(), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range e.kinds() {
		if k == "agent.run_corrupt" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("corrupt events %d", n)
	}
}

func TestTickAndFinishAllShareOneDeadline(t *testing.T) {
	old1, old2, old3, old4 := TickTimeout, FinishAllTimeout, FallbackTimeout, MinFinishBudget
	defer func() { TickTimeout, FinishAllTimeout, FallbackTimeout, MinFinishBudget = old1, old2, old3, old4 }()
	TickTimeout, FinishAllTimeout, FallbackTimeout, MinFinishBudget = 200*time.Millisecond, 200*time.Millisecond, 10*time.Second, 0
	for name, run := range map[string]func(e *mtEnv) error{
		"tick":      func(e *mtEnv) error { return e.m.Tick(context.Background(), at.Add(time.Hour)) },
		"finishall": func(e *mtEnv) error { return e.m.FinishAll(context.Background()) },
	} {
		e := newMT(t, running())
		id2 := "gadget-20261110-233100"
		r2 := Run{ID: id2, Repo: "acme/gadget", Mode: ModeInteractive, StartedAt: at, Max: 8 * time.Hour,
			WrapUpAt: at, WrapUpReason: "run-max"}
		saveRun(t, e.p, r2)
		os.MkdirAll(filepath.Join(e.p.RunsTmp, id2), 0o750)
		e.d.states[ContainerName(id2)] = running()
		e.d.execHang = true
		// the first run is past its grace too
		if err := RequestWrapUp(e.p, stopID, "run-max", at); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_ = run(e)
		if el := time.Since(start); el > 700*time.Millisecond {
			t.Errorf("%s took %v: deadline not shared by the runs", name, el)
		}
		for _, id := range []string{stopID, id2} {
			if r, _ := e.p.Load(id); name == "finishall" && r.FinishedAt.IsZero() {
				t.Errorf("%s: run %s not finished", name, id)
			}
		}
	}
}

func TestCancelWrapUpsClearsOnlyBoxCaused(t *testing.T) {
	e := newMT(t, running())
	id2 := "gadget-20261110-233100"
	saveRun(t, e.p, Run{ID: id2, Repo: "acme/gadget", Mode: ModeInteractive, StartedAt: at, Max: time.Hour})
	os.MkdirAll(filepath.Join(e.p.RunsTmp, id2), 0o750)
	if err := RequestWrapUp(e.p, id2, "run-max", at); err != nil {
		t.Fatal(err)
	}
	if err := e.m.WrapUpAll(at.Add(time.Minute), "stop-requested"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.CancelWrapUps("stop-requested"); err != nil {
		t.Fatal(err)
	}
	r1, _ := e.p.Load(stopID)
	r2, _ := e.p.Load(id2)
	if !r1.WrapUpAt.IsZero() || r1.WrapUpReason != "" {
		t.Fatalf("box wrap-up kept: %+v", r1)
	}
	if _, err := os.Stat(filepath.Join(e.p.RunsTmp, stopID, "wrapup")); err == nil {
		t.Fatal("flag file kept")
	}
	if r2.WrapUpReason != "run-max" || r2.WrapUpAt.IsZero() {
		t.Fatalf("run-max wrap-up cleared: %+v", r2)
	}
	if _, err := os.Stat(filepath.Join(e.p.RunsTmp, id2, "wrapup")); err != nil {
		t.Fatal("run-max flag removed")
	}
}

func TestTickMissingTokenFileIsQuiet(t *testing.T) {
	e := newMT(t, running())
	os.Remove(filepath.Join(e.p.RunsTmp, stopID, "token"))
	e.tok = "new"
	if err := e.m.Tick(context.Background(), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if e.has("agent.token_refresh_failed") {
		t.Fatalf("events %v", e.kinds())
	}
}

func TestTickFinishedEventOnlyAfterSuccessfulFinish(t *testing.T) {
	e := newMT(t, State{Exists: true, ExitCode: 2})
	r, _ := e.p.Load(stopID)
	r.Mode = ModeUnattended
	saveRun(t, e.p, r)
	// make teardown fail: the run dir cannot be removed
	dir := filepath.Join(e.p.RunsTmp, stopID)
	os.Chmod(e.p.RunsTmp, 0o500)
	defer os.Chmod(e.p.RunsTmp, 0o755)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	_ = dir
	_ = e.m.Tick(context.Background(), at.Add(time.Hour))
	if e.has("agent.finished") || !e.has("agent.finish_failed") {
		t.Fatalf("events %v", e.kinds())
	}
}

func TestWrapUpAllAndFinishAll(t *testing.T) {
	e := newMT(t, running())
	if err := e.m.WrapUpAll(at.Add(time.Hour), "night"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if r.WrapUpReason != "box-night" {
		t.Fatalf("run %+v", r)
	}
	if err := e.m.WrapUpAll(at.Add(2*time.Hour), "other"); err != nil {
		t.Fatal(err)
	}
	if r, _ = e.p.Load(stopID); r.WrapUpReason != "box-night" {
		t.Fatal("second wrap-up changed the reason")
	}
	if err := e.m.FinishAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ = e.p.Load(stopID); r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
}

func TestTickSlowFallbackStillFinishesWithLiveTeardown(t *testing.T) {
	old1, old3, old4 := TickTimeout, FallbackTimeout, MinFinishBudget
	defer func() { TickTimeout, FallbackTimeout, MinFinishBudget = old1, old3, old4 }()
	TickTimeout, FallbackTimeout, MinFinishBudget = 200*time.Millisecond, 10*time.Second, 0
	e := newMT(t, running())
	e.d.execHang = true
	e.d.requireLive = true
	if err := RequestWrapUp(e.p, stopID, "run-max", at); err != nil {
		t.Fatal(err)
	}
	// a second run, earlier in the order, needs a token refresh and a wrap-up
	id2 := "alpha-20261110-000000"
	saveRun(t, e.p, Run{ID: id2, Repo: "acme/alpha", Mode: ModeInteractive, StartedAt: at.Add(-9 * time.Hour), Max: time.Hour})
	os.MkdirAll(filepath.Join(e.p.RunsTmp, id2), 0o750)
	os.WriteFile(filepath.Join(e.p.RunsTmp, id2, "token"), []byte("old"), 0o640)
	e.d.states[ContainerName(id2)] = running()
	e.tok = "new"
	if err := e.m.Tick(context.Background(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if r.FinishedAt.IsZero() {
		t.Fatal("run not finished after a timed-out fallback")
	}
	if e.d.deadCtxCalls != 0 {
		t.Fatalf("%d docker calls ran on an expired context", e.d.deadCtxCalls)
	}
	var fb string
	for _, x := range e.ev.ev {
		if x.Kind == "agent.stopped" && x.Fields["run"] == stopID {
			fb = x.Fields["fallback"]
		}
	}
	if fb != "failed: timeout" {
		t.Fatalf("fallback %q", fb)
	}
	// the other run still got its refresh and wrap-up
	b, _ := os.ReadFile(filepath.Join(e.p.RunsTmp, id2, "token"))
	r2, _ := e.p.Load(id2)
	if string(b) != "new" || r2.WrapUpReason != "run-max" {
		t.Fatalf("second run starved: token %q wrapup %q", b, r2.WrapUpReason)
	}
}

func TestTickNoMintWithoutTokenFile(t *testing.T) {
	e := newMT(t, running())
	os.Remove(filepath.Join(e.p.RunsTmp, stopID, "token"))
	e.m.Tick(context.Background(), at.Add(time.Minute))
	if e.n != 0 {
		t.Fatalf("minted %d times", e.n)
	}
}

// TestTickSkipsLateFinishWithoutBudget: when the first grace-elapsed
// fallback leaves less than MinFinishBudget on the Tick deadline, the second
// run is not finished this tick (no truncated push); it stays due and the
// next tick finishes it.
func TestTickSkipsLateFinishWithoutBudget(t *testing.T) {
	old1, old3, old4 := TickTimeout, FallbackTimeout, MinFinishBudget
	defer func() { TickTimeout, FallbackTimeout, MinFinishBudget = old1, old3, old4 }()
	TickTimeout, FallbackTimeout, MinFinishBudget = 600*time.Millisecond, 10*time.Second, 400*time.Millisecond
	e := newMT(t, running())
	id2 := "alpha-20261110-000000"
	saveRun(t, e.p, Run{ID: id2, Repo: "acme/alpha", Mode: ModeInteractive, StartedAt: at, Max: 8 * time.Hour})
	os.MkdirAll(filepath.Join(e.p.RunsTmp, id2), 0o750)
	e.d.states[ContainerName(id2)] = running()
	for _, id := range []string{stopID, id2} {
		if err := RequestWrapUp(e.p, id, "run-max", at); err != nil {
			t.Fatal(err)
		}
	}
	e.d.execOut = []byte("pushed")
	e.d.execDelay = 300 * time.Millisecond // the first fallback eats the budget
	if err := e.m.Tick(context.Background(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var done, due []string
	for _, id := range []string{stopID, id2} {
		r, _ := e.p.Load(id)
		if r.FinishedAt.IsZero() {
			due = append(due, id)
		} else {
			done = append(done, id)
		}
	}
	if len(done) != 1 || len(due) != 1 {
		t.Fatalf("after the first tick: finished %v, still due %v", done, due)
	}
	if n := strings.Count(strings.Join(e.d.calls, "\n"), "jaunt-wrapup-fallback"); n != 1 {
		t.Fatalf("%d fallbacks ran, want 1", n)
	}
	e.d.execDelay = 0
	if err := e.m.Tick(context.Background(), at.Add(time.Hour+2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.p.Load(due[0]); r.FinishedAt.IsZero() {
		t.Fatalf("run %s not finished on the next tick", due[0])
	}
}
