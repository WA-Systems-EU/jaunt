package supervisor

import (
	"context"
	"errors"
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

type recSink struct{ kinds []string }

func (r *recSink) Emit(e events.Event) error { r.kinds = append(r.kinds, e.Kind); return nil }
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
	if e.sink.count("box.delete_failed") != 1 || e.sink.count(events.BoxDeleted) != 0 || e.syncs != 1 {
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
