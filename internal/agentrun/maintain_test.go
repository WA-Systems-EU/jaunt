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

func TestWrapUpAllAndFinishAll(t *testing.T) {
	e := newMT(t, running())
	if err := e.m.WrapUpAll(at.Add(time.Hour), "night"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.p.Load(stopID)
	if r.WrapUpReason != "night" {
		t.Fatalf("run %+v", r)
	}
	if err := e.m.WrapUpAll(at.Add(2*time.Hour), "other"); err != nil {
		t.Fatal(err)
	}
	if r, _ = e.p.Load(stopID); r.WrapUpReason != "night" {
		t.Fatal("second wrap-up changed the reason")
	}
	if err := e.m.FinishAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ = e.p.Load(stopID); r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
}
