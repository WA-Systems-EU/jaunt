package client

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

func withBox(p *fakeProv) { p.box = &provider.Box{ID: 9, Name: "jauntbox-alice", Status: "running"} }

func TestDownGraceful(t *testing.T) {
	d, p, _, r, s, _ := newDeps(t)
	withBox(p)
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 1 || r.cmds[0] != "[sudo jauntbox stop]" {
		t.Fatalf("cmds = %v", r.cmds)
	}
	if p.count("delete") != 0 || p.count("waitdeleted") != 1 {
		t.Fatalf("calls = %v", p.calls)
	}
	if len(s.kinds) != 1 || s.kinds[0] != events.BoxDownRequested {
		t.Fatalf("events = %v", s.kinds)
	}
}

func TestDownFallsBackToForce(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	r.err = errors.New("ssh unreachable")
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if p.count("delete") != 1 || len(p.deleted) != 1 || p.deleted[0] != 9 || p.count("waitdeleted") != 1 {
		t.Fatalf("calls = %v", p.calls)
	}
}

func TestDownCancelledNeverForces(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	r.err = errors.New("signal: interrupt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Down(ctx, d, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if p.count("delete") != 0 {
		t.Fatalf("force path taken: %v", p.calls)
	}
}

func TestDownForceSkipsSSH(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	if err := Down(context.Background(), d, true); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 || p.count("delete") != 1 {
		t.Fatalf("cmds=%v calls=%v", r.cmds, p.calls)
	}
}

func TestDownNoBox(t *testing.T) {
	d, p, _, r, s, _ := newDeps(t)
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 || p.count("delete") != 0 || len(s.kinds) != 0 {
		t.Fatalf("side effects: %v %v %v", r.cmds, p.calls, s.kinds)
	}
}

func TestNightParsesNextOccurrence(t *testing.T) {
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 11, 9, 22, 30, 0, 0, time.FixedZone("x", 3600)), "2026-11-10T07:00:00+01:00"},
		{time.Date(2026, 11, 9, 6, 0, 0, 0, time.FixedZone("x", 3600)), "2026-11-09T07:00:00+01:00"},
		{time.Date(2026, 11, 9, 7, 0, 0, 0, time.FixedZone("x", 3600)), "2026-11-10T07:00:00+01:00"},
		{time.Date(2026, 7, 9, 23, 0, 0, 0, time.UTC), "2026-07-10T07:00:00+02:00"},
	} {
		d, _, _, r, _, _ := newDeps(t)
		now := c.now
		d.Now = func() time.Time { return now }
		if err := Night(context.Background(), d, "07:00"); err != nil {
			t.Fatal(err)
		}
		want := "[sudo jauntbox night-set --until " + c.want + "]"
		if len(r.cmds) != 1 || r.cmds[0] != want {
			t.Errorf("now %v: cmds = %v, want %s", c.now, r.cmds, want)
		}
	}
}

func TestNightRejectsBadTime(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	for _, bad := range []string{"", "7", "25:00", "07:60", "tomorrow"} {
		if err := Night(context.Background(), d, bad); err == nil || !strings.Contains(err.Error(), "HH:MM") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	if len(r.cmds) != 0 {
		t.Fatalf("ran %v", r.cmds)
	}
}

func TestResizeOrder(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	var seq []string
	p.seq, r.seq = &seq, &seq
	p.volAttached, p.volDetachPolls = true, 3 // Hetzner lags after the server is gone
	if err := Resize(context.Background(), d, "big"); err != nil {
		t.Fatal(err)
	}
	want := []string{"run:sudo jauntbox stop", "waitdeleted", "waitdetached", "create:" + d.Cfg.Profiles["big"].ServerType}
	if strings.Join(seq, ",") != strings.Join(want, ",") {
		t.Fatalf("seq = %v, want %v", seq, want)
	}
}

func TestStatusReportsBoxAndRemote(t *testing.T) {
	d, p, m, r, _, _ := newDeps(t)
	withBox(p)
	r.out = []byte(`{"verdict":{"action":"keep"}}`)
	rep, err := Status(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Box == nil || !rep.Online || string(rep.BoxStatus) != `{"verdict":{"action":"keep"}}` || m.polls != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if r.cmds[0] != "[sudo jauntbox box-status --json]" {
		t.Fatalf("cmd = %v", r.cmds)
	}
}

func TestStatusNoBoxAndOffline(t *testing.T) {
	d, p, m, r, _, _ := newDeps(t)
	rep, err := Status(context.Background(), d)
	if err != nil || rep.Box != nil || len(r.cmds) != 0 || m.polls != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	withBox(p)
	m.falses = -1
	rep, err = Status(context.Background(), d)
	if err != nil || rep.Online || len(r.cmds) != 0 || rep.Box == nil {
		t.Fatalf("%+v %v", rep, err)
	}
}

func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("no exit error: %v", err)
	}
	return fmt.Errorf("ssh x: %w: out", ee)
}

func TestDownRemoteFailureNot255WaitsForSelfDelete(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	r.err = exitErr(t, 1) // e.g. the box deleted itself under the open session
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if p.count("delete") != 0 || p.count("waitdeleted") != 1 {
		t.Fatalf("calls = %v", p.calls)
	}
}

func TestDownSSHTransportFailureForcesDelete(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withBox(p)
	r.err = exitErr(t, 255)
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if p.count("delete") != 1 {
		t.Fatalf("calls = %v", p.calls)
	}
}

func TestDownGracefulTimeoutForcesDelete(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	withBox(p)
	p.waitErr = context.DeadlineExceeded
	p.clearWaitErrOnDelete = true
	if err := Down(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	if p.count("delete") != 1 || p.count("waitdeleted") != 2 {
		t.Fatalf("calls = %v", p.calls)
	}
}
