package agentrun

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	base := realTemp(t)
	return Paths{RunsTmp: filepath.Join(base, "run"), RunsVol: filepath.Join(base, "vol"), Agents: filepath.Join(base, "agents")}
}

func saveRun(t *testing.T, p Paths, r Run) {
	t.Helper()
	if err := p.Save(r); err != nil {
		t.Fatal(err)
	}
}

func TestListStates(t *testing.T) {
	p := testPaths(t)
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	mk := func(id string, n int) Run {
		return Run{ID: id, Repo: "acme/" + id[:1], Checkout: "/home/alice/src/" + id[:1], Branch: "b", Mode: ModeInteractive,
			StartedAt: t0.Add(time.Duration(n) * time.Minute), Max: 8 * time.Hour}
	}
	running := mk("a-20261110-090000", 0)
	wrapping := mk("b-20261110-090100", 1)
	wrapping.WrapUpAt, wrapping.WrapUpReason = t0.Add(time.Hour), "stop-requested"
	finished := mk("c-20261110-090200", 2)
	finished.FinishedAt = t0.Add(2 * time.Hour)
	gone := mk("d-20261110-090300", 3)
	exited := mk("e-20261110-090400", 4)
	exited.Mode = ModeUnattended
	for _, r := range []Run{running, wrapping, finished, gone, exited} {
		saveRun(t, p, r)
	}
	d := &fakeDocker{states: map[string]State{
		ContainerName(running.ID):  {Exists: true, Running: true, IP: "172.18.0.2"},
		ContainerName(wrapping.ID): {Exists: true, Running: true, IP: "172.19.0.2"},
		ContainerName(exited.ID):   {Exists: true, ExitCode: 0},
	}}
	got, err := List(context.Background(), d, p)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, state, ip string }{
		{running.ID, "running", "172.18.0.2"},
		{wrapping.ID, "wrapping-up", "172.19.0.2"},
		{finished.ID, "finished", ""},
		{gone.ID, "gone", ""},
		{exited.ID, "finished", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statuses: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].State != w.state || got[i].IP != w.ip {
			t.Errorf("status %d = %s %s %q, want %s %s %q", i, got[i].ID, got[i].State, got[i].IP, w.id, w.state, w.ip)
		}
	}
	// A finished run's container is not inspected.
	for _, c := range d.calls {
		if c == "Inspect "+ContainerName(finished.ID) {
			t.Errorf("inspected a finished run")
		}
	}
}

func TestValidRunID(t *testing.T) {
	for _, id := range []string{"widget-20261110-233000", "my.repo_x-20261110-233000"} {
		if !ValidRunID(id) {
			t.Errorf("%q rejected", id)
		}
	}
	for _, id := range []string{"", "widget", "../x-20261110-233000", "Widget-20261110-233000", "-x-20261110-233000",
		"a/b-20261110-233000", "a..b-20261110-233000", "widget-2026111-233000", "widget-20261110-233000x"} {
		if ValidRunID(id) {
			t.Errorf("%q accepted", id)
		}
	}
}
