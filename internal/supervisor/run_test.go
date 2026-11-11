package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
)

var paris, _ = time.LoadLocation("Europe/Paris")

type fakeAgents struct{ n int }

func (f *fakeAgents) Count(context.Context) (int, error) { return f.n, nil }

type recSink struct {
	kinds  []string
	onEmit func(kind string)
	fields map[string]map[string]string
}

func (r *recSink) Emit(e events.Event) error {
	r.kinds = append(r.kinds, e.Kind)
	if r.onEmit != nil {
		r.onEmit(e.Kind)
	}
	if r.fields == nil {
		r.fields = map[string]map[string]string{}
	}
	r.fields[e.Kind] = e.Fields
	return nil
}
func (r *recSink) count(k string) int {
	n := 0
	for _, x := range r.kinds {
		if x == k {
			n++
		}
	}
	return n
}

type env struct {
	s       *Supervisor
	now     time.Time
	agents  *fakeAgents
	sink    *recSink
	deletes int
	syncs   int
	err     error
}

func newEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	d := t.TempDir()
	pts := filepath.Join(d, "pts")
	os.Mkdir(pts, 0o755)
	e := &env{now: now, agents: &fakeAgents{}, sink: &recSink{}}
	e.s = &Supervisor{
		Info: boxinfo.BoxInfo{
			Timezone: "Europe/Paris", UpAt: time.Date(2026, 6, 1, 10, 0, 0, 0, paris),
			Limits: config.Default().Limits,
		},
		Paths: Paths{
			State: filepath.Join(d, "state.json"), Night: filepath.Join(d, "night.json"),
			StopRequest: filepath.Join(d, "stop"), Heartbeat: filepath.Join(d, "hb"), Lock: filepath.Join(d, "run", "lock"),
			WrapUpFlag: filepath.Join(d, "run", "wrapup"), Events: filepath.Join(d, "ev"),
		},
		PtsDir: pts, Agents: e.agents, Events: e.sink,
		Now:        func() time.Time { return e.now },
		DeleteSelf: func(context.Context) error { e.deletes++; return e.err },
		Sync:       func() { e.syncs++ },
	}
	return e
}

func (e *env) pts(name string, age time.Duration) {
	p := filepath.Join(e.s.PtsDir, name)
	os.WriteFile(p, nil, 0o644)
	at := e.now.Add(-age)
	os.Chtimes(p, at, at)
}

func (e *env) run(t *testing.T) lifecycle.Verdict {
	t.Helper()
	v, err := e.s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func at(hh, mm int) time.Time { return time.Date(2026, 6, 1, hh, mm, 0, 0, paris) }

func TestHumanIdleUsesAtime(t *testing.T) {
	d := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"0": 3 * time.Hour, "1": 10 * time.Minute, "ptmx": 0} {
		p := filepath.Join(d, name)
		os.WriteFile(p, nil, 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	if got := HumanIdle(d, now); got != 10*time.Minute {
		t.Fatalf("got %v want 10m", got)
	}
	if got := HumanIdle(t.TempDir(), now); got != 1<<62 {
		t.Fatalf("empty dir: %v", got)
	}
}

func TestAbandonedSessionIsNotActivity(t *testing.T) {
	e := newEnv(t, at(14, 0))
	e.pts("0", 3*time.Hour)
	// recent mtime (output) must not matter, only atime
	os.Chtimes(filepath.Join(e.s.PtsDir, "0"), e.now.Add(-3*time.Hour), e.now)
	if v := e.run(t); v.Reason == "human-active" {
		t.Fatalf("abandoned session counted: %+v", v)
	}
}

func TestRunOnceWrapUpThenDelete(t *testing.T) {
	e := newEnv(t, at(19, 0))
	e.agents.n = 1
	e.pts("0", time.Hour)
	if v := e.run(t); v.Action != lifecycle.StartWrapUp {
		t.Fatalf("got %+v", v)
	}
	if !exists(e.s.Paths.WrapUpFlag) {
		t.Fatal("wrapup flag missing")
	}
	e.now = e.now.Add(5 * time.Minute)
	e.pts("0", time.Hour)
	if v := e.run(t); v.Action != lifecycle.Delete {
		t.Fatalf("got %+v", v)
	}
	if e.deletes != 1 || e.syncs != 1 || e.sink.count(events.BoxDeleted) != 1 {
		t.Fatalf("deletes=%d syncs=%d", e.deletes, e.syncs)
	}
}

func TestRunOnceHumanCancelsWrapUp(t *testing.T) {
	e := newEnv(t, at(19, 0))
	e.agents.n = 1
	e.pts("0", time.Hour)
	e.run(t)
	e.now = e.now.Add(time.Minute)
	e.pts("0", time.Minute)
	if v := e.run(t); v.Action != lifecycle.Keep || v.Reason != "human-active" {
		t.Fatalf("got %+v", v)
	}
	if exists(e.s.Paths.WrapUpFlag) || e.sink.count(events.WrapUpCancelled) != 1 {
		t.Fatal("wrap-up not cancelled")
	}
	// human leaves again: fresh grace, not an instant Delete
	e.now = e.now.Add(time.Hour)
	e.pts("0", time.Hour)
	if v := e.run(t); v.Action != lifecycle.StartWrapUp {
		t.Fatalf("want StartWrapUp, got %+v", v)
	}
	if e.sink.count(events.WrapUpStarted) != 2 {
		t.Fatal("second wrapup.started missing")
	}
}

func TestRunOnceStopRequest(t *testing.T) {
	e := newEnv(t, at(12, 0))
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	if v := e.run(t); v.Action != lifecycle.Delete || v.Reason != "stop-requested" {
		t.Fatalf("got %+v", v)
	}
}

func TestRunOnceHeartbeat(t *testing.T) {
	e := newEnv(t, at(11, 0))
	for i := 0; i < 2; i++ {
		e.run(t)
		fi, err := os.Stat(e.s.Paths.Heartbeat)
		if err != nil || !fi.ModTime().Equal(e.now) {
			t.Fatalf("mtime %v want %v (%v)", fi.ModTime(), e.now, err)
		}
		e.now = e.now.Add(time.Minute)
	}
}

func TestTickOnlyOnReasonChange(t *testing.T) {
	e := newEnv(t, at(11, 0))
	e.run(t)
	e.now = e.now.Add(time.Minute)
	e.run(t)
	if n := e.sink.count(events.SupervisorTick); n != 1 {
		t.Fatalf("ticks = %d", n)
	}
}

func TestBadTimezoneErrors(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.s.Info.Timezone = "Nowhere/Land"
	if _, err := e.s.RunOnce(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

type failAgents struct{}

func (failAgents) Count(context.Context) (int, error) { return 0, errors.New("docker down") }

func TestHeartbeatNotTouchedOnFailure(t *testing.T) {
	cases := map[string]func(e *env){
		"agents":   func(e *env) { e.s.Agents = failAgents{} },
		"delete":   func(e *env) { e.err = errors.New("boom"); os.WriteFile(e.s.Paths.StopRequest, nil, 0o644) },
		"timezone": func(e *env) { e.s.Info.Timezone = "Nowhere/Land" },
		"night":    func(e *env) { os.WriteFile(e.s.Paths.Night, []byte("{"), 0o644) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, at(12, 0))
			setup(e)
			if _, err := e.s.RunOnce(context.Background()); err == nil {
				t.Fatal("want error")
			}
			if exists(e.s.Paths.Heartbeat) {
				t.Fatal("heartbeat touched on failed pass")
			}
		})
	}
}

func TestDeleteNeverTouchesHeartbeat(t *testing.T) {
	e := newEnv(t, at(12, 0))
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if exists(e.s.Paths.Heartbeat) {
		t.Fatal("heartbeat touched on Delete")
	}
	if e.sink.count(events.BoxDeleted) != 1 {
		t.Fatal("box.deleted missing")
	}
}

func TestDeleteFailedEvent(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.err = errors.New("boom")
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.s.RunOnce(context.Background())
	// box.deleted is written before the attempt; box.delete_failed follows it.
	k := e.sink.kinds
	if e.sink.count("box.delete_failed") != 1 || e.sink.count(events.BoxDeleted) != 1 || k[len(k)-1] != "box.delete_failed" || e.syncs != 1 {
		t.Fatalf("%v syncs=%d", e.sink.kinds, e.syncs)
	}
}

func TestCorruptStateFallsBack(t *testing.T) {
	e := newEnv(t, at(11, 0))
	os.WriteFile(e.s.Paths.State, []byte("{garbage"), 0o644)
	if v := e.run(t); v.Reason != "ok" {
		t.Fatalf("got %+v", v)
	}
	if e.sink.count("supervisor.warning") != 1 {
		t.Fatalf("%v", e.sink.kinds)
	}
}

func TestLockExcludesConcurrentRun(t *testing.T) {
	e := newEnv(t, at(11, 0))
	unlock, err := e.s.lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.RunOnce(context.Background()); err == nil {
		t.Fatal("want in-progress error")
	}
	unlock()
	e.run(t)
}

func TestWrapUpFlagRecreated(t *testing.T) {
	e := newEnv(t, at(19, 0))
	e.agents.n = 1
	e.pts("0", time.Hour)
	e.run(t)
	os.Remove(e.s.Paths.WrapUpFlag)
	e.now = e.now.Add(time.Minute)
	e.pts("0", time.Hour)
	if v := e.run(t); v.Reason != "wrapup-in-progress" || !exists(e.s.Paths.WrapUpFlag) {
		t.Fatalf("got %+v", v)
	}
}

func TestDeleteSelfErrorReturned(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.err = errors.New("boom")
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	if _, err := e.s.RunOnce(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestNightFile(t *testing.T) {
	e := newEnv(t, at(23, 0))
	e.agents.n = 2
	e.pts("0", 4*time.Hour)
	if err := SaveNight(e.s.Paths.Night, at(23, 0).Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v := e.run(t); v.Reason != "night" {
		t.Fatalf("got %+v", v)
	}
}

func TestPreviewNoSideEffects(t *testing.T) {
	e := newEnv(t, at(12, 0))
	if _, _, err := e.s.Preview(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exists(e.s.Paths.Heartbeat) || exists(e.s.Paths.State) || len(e.sink.kinds) != 0 {
		t.Fatal("preview had side effects")
	}
}

func TestValidateNightUntil(t *testing.T) {
	now := at(22, 0)
	if err := ValidateNightUntil(now, now.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNightUntil(now, now.Add(-time.Minute)); err == nil {
		t.Fatal("past accepted")
	}
	if err := ValidateNightUntil(now, now.Add(25*time.Hour)); err == nil || !strings.Contains(err.Error(), "24h") {
		t.Fatalf("got %v", err)
	}
}

func TestRunOnceBusyReturnsErrBusy(t *testing.T) {
	e := newEnv(t, at(12, 0))
	unlock, err := e.s.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := e.s.RunOnce(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
}

func TestDockerFailureWithActiveHumanKeepsAndBeats(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.s.Agents = failAgents{}
	e.pts("0", 2*time.Minute)
	v, err := e.s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Action != lifecycle.Keep || v.Reason != "human-active" {
		t.Fatalf("got %+v", v)
	}
	if !exists(e.s.Paths.Heartbeat) {
		t.Error("heartbeat not touched")
	}
	if e.sink.count("supervisor.warning") != 1 {
		t.Errorf("events %v", e.sink.kinds)
	}
	st, _ := LoadState(e.s.Paths.State)
	if st.LastVerdict != "human-active" {
		t.Errorf("state not saved: %+v", st)
	}
}

func TestDockerPsTimesOut(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old := dockerTimeout
	dockerTimeout = 100 * time.Millisecond
	defer func() { dockerTimeout = old }()
	start := time.Now()
	_, err := DockerAgents{}.Count(context.Background())
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestLastActiveIsRealLastInputNotPassTime(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.pts("0", 20*time.Minute)
	e.run(t)
	st, err := LoadState(e.s.Paths.State)
	if err != nil {
		t.Fatal(err)
	}
	if want := at(11, 40); !st.LastActive.Equal(want) {
		t.Fatalf("LastActive = %v, want %v", st.LastActive, want)
	}
	// With agents running, activity is "now".
	e.agents.n = 1
	e.now = at(12, 5)
	e.run(t)
	st, _ = LoadState(e.s.Paths.State)
	if !st.LastActive.Equal(at(12, 5)) {
		t.Fatalf("LastActive with agents = %v", st.LastActive)
	}
}

func TestDeleteRevokesTokensFirst(t *testing.T) {
	e := newEnv(t, at(12, 0))
	var calls []string
	e.s.Revoke = func(context.Context) error { calls = append(calls, "revoke"); return nil }
	e.s.Sync = func() { calls = append(calls, "sync") }
	e.s.DeleteSelf = func(context.Context) error { calls = append(calls, "delete-self"); return nil }
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if got := strings.Join(calls, ","); got != "revoke,sync,delete-self" {
		t.Fatalf("call order %q", got)
	}
}

func TestRevokeFailureDoesNotBlockDelete(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.s.Revoke = func(context.Context) error { return errors.New("forge down") }
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if e.deletes != 1 || e.sink.count(events.BoxDeleted) != 1 {
		t.Fatalf("deletes=%d %v", e.deletes, e.sink.kinds)
	}
	if e.sink.count("token.revoke_failed") != 1 || e.sink.fields["token.revoke_failed"]["error"] != "forge down" {
		t.Fatalf("%v %v", e.sink.kinds, e.sink.fields)
	}
}

func TestPanickingRevokeDoesNotBlockDelete(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.s.Revoke = func(context.Context) error { panic("boom") }
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if e.deletes != 1 || e.sink.count("token.revoke_failed") != 1 {
		t.Fatalf("deletes=%d %v", e.deletes, e.sink.kinds)
	}
}

func TestKeepVerdictDoesNotRevoke(t *testing.T) {
	e := newEnv(t, at(12, 0))
	revoked := 0
	e.s.Revoke = func(context.Context) error { revoked++; return nil }
	if v := e.run(t); v.Action != lifecycle.Keep {
		t.Fatalf("got %+v", v)
	}
	if revoked != 0 {
		t.Fatalf("revoked=%d", revoked)
	}
}

func TestRunUsesOneNow(t *testing.T) {
	e := newEnv(t, at(14, 0))
	calls := 0
	e.s.Now = func() time.Time { calls++; return e.now.Add(time.Duration(calls) * time.Second) }
	e.pts("0", time.Minute)
	e.run(t)
	if calls != 1 {
		t.Fatalf("Now read %d times in one pass, want 1", calls)
	}
}

func TestBoxDeletedWrittenBeforeSyncAndDelete(t *testing.T) {
	// The box loses power right after DeleteSelf: anything emitted later never
	// reaches the volume, so box.deleted must be on disk before the call.
	e := newEnv(t, at(12, 0))
	var calls []string
	e.s.Sync = func() { calls = append(calls, fmt.Sprintf("sync(deleted=%d)", e.sink.count(events.BoxDeleted))) }
	e.s.DeleteSelf = func(context.Context) error {
		calls = append(calls, fmt.Sprintf("delete-self(deleted=%d)", e.sink.count(events.BoxDeleted)))
		return nil
	}
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if got := strings.Join(calls, ","); got != "sync(deleted=1),delete-self(deleted=1)" {
		t.Fatalf("calls %q", got)
	}
	if e.sink.count(events.BoxDeleted) != 1 {
		t.Fatalf("kinds %v", e.sink.kinds)
	}
}

func TestDockerErrorWithActiveHumanAndStopStartsWrapUp(t *testing.T) {
	e := newEnv(t, at(12, 0))
	e.s.Agents = failAgents{}
	e.pts("0", 2*time.Minute)
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	v := e.run(t)
	if v.Action != lifecycle.StartWrapUp || v.Reason != "stop-requested" {
		t.Fatalf("got %+v", v)
	}
	if !exists(e.s.Paths.WrapUpFlag) {
		t.Error("wrapup flag missing")
	}
	if e.deletes != 0 {
		t.Errorf("deleted %d", e.deletes)
	}
}

type fakeRuns struct {
	calls   *[]string
	tickErr error
	finErr  error
	reason  string
}

func (f *fakeRuns) Tick(context.Context, time.Time) error {
	*f.calls = append(*f.calls, "tick")
	return f.tickErr
}
func (f *fakeRuns) WrapUpAll(_ time.Time, reason string) error {
	*f.calls = append(*f.calls, "wrapup-all")
	f.reason = reason
	return nil
}
func (f *fakeRuns) CancelWrapUps(reason string) error {
	*f.calls = append(*f.calls, "cancel:"+reason)
	return nil
}
func (f *fakeRuns) FinishAll(context.Context) error {
	*f.calls = append(*f.calls, "finish-all")
	return f.finErr
}

func TestDeleteOrderFinishRevokeSyncDelete(t *testing.T) {
	e := newEnv(t, at(12, 0))
	var calls []string
	e.s.Runs = &fakeRuns{calls: &calls}
	e.s.Revoke = func(context.Context) error { calls = append(calls, "revoke"); return nil }
	e.sink.onEmit = func(k string) {
		if k == events.BoxDeleted {
			calls = append(calls, "box.deleted")
		}
	}
	e.s.Sync = func() { calls = append(calls, "sync") }
	e.s.DeleteSelf = func(context.Context) error { calls = append(calls, "delete-self"); return nil }
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if got := strings.Join(calls, ","); got != "tick,finish-all,revoke,box.deleted,sync,delete-self" {
		t.Fatalf("call order %q", got)
	}
	if e.sink.count(events.BoxDeleted) != 1 {
		t.Fatalf("kinds %v", e.sink.kinds)
	}
}

func TestStartWrapUpFlagsAllRuns(t *testing.T) {
	e := newEnv(t, at(19, 0))
	e.agents.n = 1
	e.pts("0", time.Hour)
	var calls []string
	fr := &fakeRuns{calls: &calls}
	e.s.Runs = fr
	if v := e.run(t); v.Action != lifecycle.StartWrapUp {
		t.Fatalf("got %+v", v)
	}
	if strings.Join(calls, ",") != "tick,wrapup-all" || fr.reason == "" {
		t.Fatalf("calls %v reason %q", calls, fr.reason)
	}
}

func TestFinishAllFailureDoesNotBlockDelete(t *testing.T) {
	e := newEnv(t, at(12, 0))
	var calls []string
	e.s.Runs = &fakeRuns{calls: &calls, finErr: errors.New("docker hung")}
	os.WriteFile(e.s.Paths.StopRequest, nil, 0o644)
	e.run(t)
	if e.deletes != 1 || e.sink.count("agent.finish_failed") != 1 || e.sink.fields["agent.finish_failed"]["error"] != "docker hung" {
		t.Fatalf("deletes=%d %v", e.deletes, e.sink.kinds)
	}
}

func TestRunsTickFailureDoesNotBlockHeartbeat(t *testing.T) {
	e := newEnv(t, at(12, 0))
	var calls []string
	e.s.Runs = &fakeRuns{calls: &calls, tickErr: errors.New("cannot list")}
	e.run(t)
	if e.sink.count("agent.tick_failed") != 1 || !exists(e.s.Paths.Heartbeat) {
		t.Fatalf("%v", e.sink.kinds)
	}
}

func TestWrapUpCancelCancelsRuns(t *testing.T) {
	e := newEnv(t, at(19, 0))
	e.agents.n = 1
	e.pts("0", time.Hour)
	var calls []string
	fr := &fakeRuns{calls: &calls}
	e.s.Runs = fr
	e.run(t) // StartWrapUp
	e.now = e.now.Add(time.Minute)
	e.pts("0", time.Minute)
	e.run(t) // human back: cancelled
	got := strings.Join(calls, ",")
	if got != "tick,wrapup-all,tick,cancel:"+fr.reason {
		t.Fatalf("calls %q", got)
	}
}

func TestSuperviseUnitTimeoutExceedsPassBudget(t *testing.T) {
	b, err := os.ReadFile("../cloudinit/units/jauntbox-supervise.service")
	if err != nil {
		t.Fatal(err)
	}
	var secs int
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "TimeoutStartSec="); ok {
			fmt.Sscan(v, &secs)
		}
	}
	if time.Duration(secs)*time.Second != PassTimeout {
		t.Fatalf("TimeoutStartSec=%d does not match PassTimeout %v", secs, PassTimeout)
	}
	tb, err := os.ReadFile("../cloudinit/units/jauntbox-supervise.timer")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tb), fmt.Sprintf("\nOnUnitActiveSec=%dmin\n", int(TimerInterval/time.Minute))) {
		t.Fatalf("supervise.timer does not match TimerInterval %v:\n%s", TimerInterval, tb)
	}
	// The 600 s limit covers 2 runs in the worst case (all timeouts hit).
	if need := MaxPassBudget(2); time.Duration(secs)*time.Second < need {
		t.Fatalf("TimeoutStartSec=%d is below the worst-case pass budget %v", secs, need)
	}
	if time.Duration(secs)*time.Second >= 900*time.Second {
		t.Fatalf("TimeoutStartSec=%d must stay below the 900 s safety net", secs)
	}
}
