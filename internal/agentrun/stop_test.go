package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
)

const stopID = "widget-20261110-233000"

var finAt = at.Add(3 * time.Hour)

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

func withRunPorts(t *testing.T, p Paths, ports ...int) {
	t.Helper()
	r, err := p.Load(stopID)
	if err != nil {
		t.Fatal(err)
	}
	r.HostPorts, r.RunPorts = ports, ports
	saveRun(t, p, r)
}

func revokeRule(port string) string {
	return "iptables -w -D JAUNTBOX-RUNS -i " + BridgeName(stopID) + " -p tcp --dport " + port + " -j ACCEPT"
}

func TestFinishRunsFallbackBeforeStop(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	withRunPorts(t, p, 6379)
	if err := RequestWrapUp(p, stopID, "stop-requested", at); err != nil {
		t.Fatal(err)
	}
	name := ContainerName(stopID)
	d := &fakeDocker{states: map[string]State{name: {Exists: true, Running: true}}, execOut: []byte("pushed\n")}
	var order []string
	d.onCall = func(c string) {
		if !strings.HasPrefix(c, "Inspect") {
			order = append(order, c)
		}
	}
	net := func(_ context.Context, bin string, args ...string) error {
		order = append(order, bin+" "+strings.Join(args, " "))
		return nil
	}
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, net, stopID, ev, finAt); err != nil {
		t.Fatal(err)
	}
	want := []string{"Exec " + name + "  jaunt-wrapup-fallback stop-requested", "Stop " + name, "Remove " + name, "NetworkRemove " + name,
		revokeRule("6379")}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("calls\n got %q\nwant %q", order, want)
	}
	if _, err := os.Stat(filepath.Join(p.RunsTmp, stopID)); !os.IsNotExist(err) {
		t.Errorf("tmpfs run dir kept: %v", err)
	}
	r, _ := p.Load(stopID)
	if !r.FinishedAt.Equal(finAt) || r.ExitCode != 143 {
		t.Errorf("record FinishedAt %v exit %d", r.FinishedAt, r.ExitCode)
	}
	if len(ev.ev) != 1 || ev.ev[0].Kind != events.AgentStopped || !ev.ev[0].Time.Equal(finAt) {
		t.Fatalf("events %+v", ev.ev)
	}
	f := ev.ev[0].Fields
	if f["run"] != stopID || f["repo"] != "acme/widget" || f["fallback"] != "pushed" || f["reason"] != "stop-requested" || f["exit_code"] != "143" {
		t.Fatalf("fields %v", f)
	}
}

func TestFinishWhenContainerGone(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	d := &fakeDocker{} // no container
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, nil, stopID, ev, finAt); err != nil {
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
	if err := Finish(context.Background(), d, p, nil, stopID, ev, finAt); err != nil || len(d.calls) != n || len(ev.ev) != 1 {
		t.Fatalf("second finish: err=%v calls=%v events=%d", err, d.calls[n:], len(ev.ev))
	}
}

func TestFinishExitedContainerGetsNoFallback(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	name := ContainerName(stopID)
	d := &fakeDocker{states: map[string]State{name: {Exists: true, ExitCode: 3}}}
	if err := Finish(context.Background(), d, p, nil, stopID, &sinkRec{}, finAt); err != nil {
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

func TestFinishFallbackTimeout(t *testing.T) {
	old := FallbackTimeout
	FallbackTimeout = 20 * time.Millisecond
	defer func() { FallbackTimeout = old }()
	p := testPaths(t)
	activeRun(t, p)
	name := ContainerName(stopID)
	d := &fakeDocker{states: map[string]State{name: {Exists: true, Running: true}}, execHang: true}
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, nil, stopID, ev, finAt); err != nil {
		t.Fatal(err)
	}
	if got := ev.ev[0].Fields["fallback"]; got != "failed: timeout" {
		t.Fatalf("fallback %q", got)
	}
	if d.calls[len(d.calls)-1] != "NetworkRemove "+name {
		t.Fatalf("teardown did not go on: %q", d.calls)
	}
	if r, _ := p.Load(stopID); r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
}

func TestFinishUnknownExitCodeOmitted(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	name := ContainerName(stopID)
	// the container still reads as running after the stop: no exit code
	d := &fakeDocker{states: map[string]State{name: {Exists: true, Running: true}}, stopKeeps: true, execOut: []byte("pushed")}
	ev := &sinkRec{}
	if err := Finish(context.Background(), d, p, nil, stopID, ev, finAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := ev.ev[0].Fields["exit_code"]; ok {
		t.Fatalf("exit_code reported for a force-stopped container: %v", ev.ev[0].Fields)
	}
	// a gone container has none either
	p2 := testPaths(t)
	activeRun(t, p2)
	ev2 := &sinkRec{}
	if err := Finish(context.Background(), &fakeDocker{}, p2, nil, stopID, ev2, finAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := ev2.ev[0].Fields["exit_code"]; ok {
		t.Fatalf("exit_code reported for a gone container: %v", ev2.ev[0].Fields)
	}
}

func TestFinishGoneRevokesRunPorts(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	withRunPorts(t, p, 6379, 8080)
	var got []string
	net := func(_ context.Context, bin string, args ...string) error {
		got = append(got, bin+" "+strings.Join(args, " "))
		return nil
	}
	if err := Finish(context.Background(), &fakeDocker{}, p, net, stopID, &sinkRec{}, finAt); err != nil {
		t.Fatal(err)
	}
	if want := []string{revokeRule("6379"), revokeRule("8080")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if r, _ := p.Load(stopID); r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
}

func TestFinishIgnoresRevokeErrors(t *testing.T) {
	p := testPaths(t)
	activeRun(t, p)
	withRunPorts(t, p, 6379)
	calls := 0
	net := func(context.Context, string, ...string) error { calls++; return errors.New("iptables broke") }
	if err := Finish(context.Background(), &fakeDocker{}, p, net, stopID, &sinkRec{}, finAt); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	if r, _ := p.Load(stopID); r.FinishedAt.IsZero() {
		t.Fatal("not finished")
	}
}
