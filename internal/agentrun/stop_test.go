package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
)

const stopID = "widget-20261110-233000"

func activeRun(t *testing.T, p Paths) Run {
	t.Helper()
	r := Run{ID: stopID, Repo: "acme/widget", Checkout: "/home/alice/src/widget", Branch: "feat/x", Mode: ModeInteractive,
		StartedAt: at, Max: 8 * time.Hour}
	saveRun(t, p, r)
	if err := os.MkdirAll(filepath.Join(p.RunsTmp, stopID), 0o750); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRequestWrapUpWritesFlag(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	now := at.Add(time.Hour)
	if err := RequestWrapUp(p, stopID, "stop-requested", now); err != nil {
		t.Fatal(err)
	}
	flag := filepath.Join(p.RunsTmp, stopID, "wrapup")
	b, err := os.ReadFile(flag)
	if err != nil || string(b) != "stop-requested" {
		t.Fatalf("flag %q, %v", b, err)
	}
	if fi, _ := os.Lstat(flag); fi.Mode().Perm() != 0o644 {
		t.Errorf("flag mode %v", fi.Mode().Perm())
	}
	r, err := p.Load(stopID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.WrapUpAt.Equal(now) || r.WrapUpReason != "stop-requested" {
		t.Fatalf("record %v %q", r.WrapUpAt, r.WrapUpReason)
	}
	// A second request keeps the first time and reason (the grace runs from it).
	if err := RequestWrapUp(p, stopID, "run-max", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ = p.Load(stopID)
	if !r.WrapUpAt.Equal(now) || r.WrapUpReason != "stop-requested" {
		t.Fatalf("second request changed the record: %v %q", r.WrapUpAt, r.WrapUpReason)
	}
	if b, _ := os.ReadFile(flag); string(b) != "stop-requested" {
		t.Fatalf("flag rewritten to %q", b)
	}
}

func TestRequestWrapUpRefusesFinishedAndInvalid(t *testing.T) {
	p := testPaths(t)
	r := activeRun(t, p)
	r.FinishedAt = at.Add(time.Hour)
	saveRun(t, p, r)
	if err := RequestWrapUp(p, stopID, "stop-requested", at); err == nil {
		t.Fatal("wrap-up of a finished run accepted")
	}
	if err := RequestWrapUp(p, "../x", "stop-requested", at); err == nil {
		t.Fatal("invalid id accepted")
	}
}

func TestFinishRunsFallbackBeforeStop(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	if err := RequestWrapUp(p, stopID, "stop-requested", at); err != nil {
		t.Fatal(err)
	}
	name := ContainerName(stopID)
	d := &fakeDocker{states: map[string]State{name: {Exists: true, Running: true}}, execOut: []byte("pushed\n")}
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, stopID, ev); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range d.calls {
		if !strings.HasPrefix(c, "Inspect") {
			order = append(order, c)
		}
	}
	want := []string{"Exec " + name + "  jaunt-wrapup-fallback stop-requested", "Stop " + name, "Remove " + name, "NetworkRemove " + name}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("calls\n got %q\nwant %q", order, want)
	}
	if _, err := os.Stat(filepath.Join(p.RunsTmp, stopID)); !os.IsNotExist(err) {
		t.Errorf("tmpfs run dir kept: %v", err)
	}
	r, _ := p.Load(stopID)
	if r.FinishedAt.IsZero() {
		t.Error("FinishedAt not set")
	}
	if len(ev.ev) != 1 || ev.ev[0].Kind != events.AgentStopped {
		t.Fatalf("events %+v", ev.ev)
	}
	f := ev.ev[0].Fields
	if f["run"] != stopID || f["repo"] != "acme/widget" || f["fallback"] != "pushed" || f["reason"] != "stop-requested" {
		t.Fatalf("fields %v", f)
	}
}

func TestFinishWhenContainerGone(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	d := &fakeDocker{} // no container
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, stopID, ev); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.calls {
		if strings.HasPrefix(c, "Exec") || strings.HasPrefix(c, "Stop") {
			t.Errorf("unexpected call %q", c)
		}
	}
	r, _ := p.Load(stopID)
	if r.FinishedAt.IsZero() {
		t.Error("FinishedAt not set")
	}
	if len(ev.ev) != 1 || ev.ev[0].Kind != events.AgentStopped {
		t.Fatalf("events %+v", ev.ev)
	}
	if _, ok := ev.ev[0].Fields["fallback"]; ok {
		t.Errorf("fallback reported for a gone container: %v", ev.ev[0].Fields)
	}
	// Finishing again is a no-op.
	n := len(d.calls)
	if err := Finish(context.Background(), d, p, stopID, ev); err != nil || len(d.calls) != n || len(ev.ev) != 1 {
		t.Fatalf("second finish: err=%v calls=%v events=%d", err, d.calls[n:], len(ev.ev))
	}
}

func TestFinishExitedContainerGetsNoFallback(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	name := ContainerName(stopID)
	d := &fakeDocker{states: map[string]State{name: {Exists: true, ExitCode: 3}}}
	if err := Finish(context.Background(), d, p, stopID, &sinkRec{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.calls {
		if strings.HasPrefix(c, "Exec") {
			t.Errorf("fallback ran on an exited container: %q", c)
		}
	}
	r, _ := p.Load(stopID)
	if r.ExitCode != 3 {
		t.Errorf("exit code %d", r.ExitCode)
	}
}
