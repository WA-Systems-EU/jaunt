package client

import (
	"errors"
	"github.com/WA-Systems-EU/jaunt/internal/events"

	"context"
	"fmt"
	"github.com/WA-Systems-EU/jaunt/internal/session"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/ui"
)

// withRec installs a Recorder as the deps' step stream.
func withRec(d *Deps) *ui.Recorder {
	r := &ui.Recorder{}
	d.Steps = r
	return r
}

// assertEvents compares the recorded events with want.
func assertEvents(t *testing.T, r *ui.Recorder, want ...string) {
	t.Helper()
	if got := strings.Join(r.Events, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// assertAllStartedFinished checks every started step got an outcome.
func assertAllStartedFinished(t *testing.T, r *ui.Recorder) {
	t.Helper()
	open := map[string]bool{}
	for _, e := range r.Events {
		switch {
		case strings.HasPrefix(e, "start "):
			open[strings.TrimPrefix(e, "start ")] = true
		case strings.HasPrefix(e, "done "):
			delete(open, strings.TrimPrefix(e, "done "))
		case strings.HasPrefix(e, "skip "), strings.HasPrefix(e, "fail "):
			label := strings.SplitN(strings.SplitN(e, " ", 2)[1], ":", 2)[0]
			delete(open, label)
		}
	}
	if len(open) != 0 {
		t.Fatalf("steps left without an outcome: %v\n%v", open, r.Events)
	}
}

// captureStderr runs f and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = pw
	done := make(chan string)
	go func() { b, _ := io.ReadAll(pr); done <- string(b) }()
	f()
	os.Stderr = old
	pw.Close()
	return <-done
}

func fastPoll(t *testing.T) {
	t.Helper()
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

func TestUpRecordsSteps(t *testing.T) {
	d, _, _, _, _, _ := newDeps(t)
	r := withRec(&d)
	if _, err := Up(context.Background(), d, "small"); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, r,
		"start Policy and limits", "done Policy and limits",
		"start SSH key, firewall, volume", "done SSH key, firewall, volume",
		"start Server created", "detail Server created: #9", "done Server created")
}

func TestPushBinaryRecordsSteps(t *testing.T) {
	fastPoll(t)
	d, _, _, _, _, _ := newDeps(t)
	r := withRec(&d)
	osExecutable = func() (string, error) { return "/bin/true", nil }
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, r,
		"start Joined the tailnet", "detail Joined the tailnet: jauntbox-alice", "done Joined the tailnet",
		"start Box reachable", "done Box reachable",
		"start Install supervisor", "done Install supervisor",
		"start Unlock secrets", "done Unlock secrets")
}

func TestPushBinaryTimeoutFailsBoxReachable(t *testing.T) {
	fastPoll(t)
	d, _, _, _, _, _ := newDeps(t)
	r := withRec(&d)
	d.Root = &flakyCopyRemote{copyFails: 1 << 30}
	osExecutable = func() (string, error) { return "/bin/true", nil }
	err := PushBinary(context.Background(), d, 20*time.Millisecond)
	if err == nil {
		t.Fatal("want error")
	}
	want := "fail Box reachable: " + err.Error()
	if !contains(r.Events, want) {
		t.Fatalf("no %q in %v", want, r.Events)
	}
	if contains(r.Events, "start Install supervisor") {
		t.Fatalf("install started: %v", r.Events)
	}
	if !strings.Contains(err.Error(), "joined the tailnet but is not accepting ssh") {
		t.Fatalf("err changed: %v", err)
	}
	assertAllStartedFinished(t, r)
}

func contains(events []string, e string) bool {
	for _, x := range events {
		if x == e {
			return true
		}
	}
	return false
}

func TestRootSSHRetryIsAWarning(t *testing.T) {
	oldP, oldR := pollInterval, rootSSHWait
	pollInterval, rootSSHWait = time.Millisecond, 20*time.Millisecond
	defer func() { pollInterval, rootSSHWait = oldP, oldR }()
	d, _, _, _, _, _ := newDeps(t)
	rec := withRec(&d)
	d.Root = &rootRefusedRemote{}
	osExecutable = func() (string, error) { return "/bin/true", nil }
	var err error
	out := captureStderr(t, func() { err = PushBinary(context.Background(), d, time.Hour) })
	if err == nil {
		t.Fatal("want error")
	}
	if out != "" {
		t.Fatalf("wrote to stderr: %q", out)
	}
	n := 0
	for _, e := range rec.Events {
		if strings.HasPrefix(e, "warn Box reachable: ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("warnings = %d: %v", n, rec.Events)
	}
}

func TestUnlockStepOutcomes(t *testing.T) {
	ctx := context.Background()
	t.Run("empty store skips", func(t *testing.T) {
		d, _, _, r, _, _ := newDeps(t)
		rec := withRec(&d)
		withSecrets(t, &d)
		r.locked = true
		tryUnlock(ctx, d)
		assertEvents(t, rec, "start Unlock secrets", "skip Unlock secrets: no secrets set")
	})
	t.Run("error warns then done", func(t *testing.T) {
		d, _, _, r, _, _ := newDeps(t)
		rec := withRec(&d)
		withSecrets(t, &d, "agents.env", "K=v\n")
		r.locked, r.inputErr = true, os.ErrPermission
		tryUnlock(ctx, d)
		if len(rec.Events) != 3 || !strings.HasPrefix(rec.Events[1], "warn Unlock secrets: load secrets on the box:") || rec.Events[2] != "done Unlock secrets" {
			t.Fatalf("events = %v", rec.Events)
		}
	})
	t.Run("ok", func(t *testing.T) {
		d, _, _, _, _, _ := newDeps(t)
		rec := withRec(&d)
		withSecrets(t, &d, "agents.env", "K=v\n")
		tryUnlock(ctx, d)
		assertEvents(t, rec, "start Unlock secrets", "done Unlock secrets")
	})
}

func TestExistingBoxSteps(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	withBox(p)
	rec := withRec(&d)
	res, err := Up(context.Background(), d, "small")
	if err != nil || res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, rec,
		"start Policy and limits", "done Policy and limits",
		"start SSH key, firewall, volume", "done SSH key, firewall, volume",
		"start Box found", "detail Box found: jauntbox-alice (running)", "done Box found",
		"start Install supervisor", "skip Install supervisor: already running",
		"start Unlock secrets", "done Unlock secrets")
}

func downDetail(t *testing.T, d Deps) string {
	t.Helper()
	pol, err := d.Policy.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wait := downWait(time.Duration(pol.Limits.WrapUpGrace))
	return fmt.Sprintf("detail Wait for the box to delete itself: up to %d min", int((wait+time.Minute-1)/time.Minute))
}

func TestDownRecordsSteps(t *testing.T) {
	t.Run("graceful", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		withBox(p)
		rec := withRec(&d)
		if err := Down(context.Background(), d, false); err != nil {
			t.Fatal(err)
		}
		assertEvents(t, rec,
			"start Ask the box to wrap up", "done Ask the box to wrap up",
			"start Wait for the box to delete itself", downDetail(t, d), "done Wait for the box to delete itself",
			"start Box deleted", "detail Box deleted: volume kept", "done Box deleted")
	})
	t.Run("falls back to direct delete", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		withBox(p)
		p.waitErr, p.clearWaitErrOnDelete = context.DeadlineExceeded, true
		rec := withRec(&d)
		if err := Down(context.Background(), d, false); err != nil {
			t.Fatal(err)
		}
		assertEvents(t, rec,
			"start Ask the box to wrap up", "done Ask the box to wrap up",
			"start Wait for the box to delete itself", downDetail(t, d),
			"warn Wait for the box to delete itself: did not delete itself in time", "done Wait for the box to delete itself",
			"start Delete the box directly", "done Delete the box directly",
			"start Box deleted", "detail Box deleted: volume kept", "done Box deleted")
	})
	t.Run("unreachable box is deleted directly", func(t *testing.T) {
		d, p, _, r, _, _ := newDeps(t)
		withBox(p)
		r.err = exitErr(t, 255)
		d.Mesh.(*fakeMesh).falses = -1
		rec := withRec(&d)
		if err := Down(context.Background(), d, false); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(rec.Events[1], "warn Ask the box to wrap up: could not reach the box") ||
			!contains(rec.Events, "start Delete the box directly") || contains(rec.Events, "start Wait for the box to delete itself") {
			t.Fatalf("events = %v", rec.Events)
		}
		assertAllStartedFinished(t, rec)
	})
	t.Run("force", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		withBox(p)
		rec := withRec(&d)
		if err := Down(context.Background(), d, true); err != nil {
			t.Fatal(err)
		}
		assertEvents(t, rec,
			"start Delete the box", "done Delete the box",
			"start Box deleted", "detail Box deleted: volume kept", "done Box deleted")
	})
}

func TestDownNoBox(t *testing.T) {
	d, _, _, _, _, _ := newDeps(t)
	rec := withRec(&d)
	out := captureStdout(t, func() {
		if err := Down(context.Background(), d, false); err != nil {
			t.Fatal(err)
		}
	})
	if out != "" {
		t.Fatalf("stdout = %q", out)
	}
	assertEvents(t, rec, "end fail | no box running")
}

func TestResizeWaitsForNewBox(t *testing.T) {
	fastPoll(t)
	d, p, _, _, _, spawns := newDeps(t)
	withBox(p)
	rec := withRec(&d)
	osExecutable = func() (string, error) { return "/bin/true", nil }
	if _, err := Resize(context.Background(), d, "big"); err != nil {
		t.Fatal(err)
	}
	if *spawns != 0 {
		t.Fatalf("spawned %d times", *spawns)
	}
	idx := func(e string) int {
		for i, x := range rec.Events {
			if x == e {
				return i
			}
		}
		return -1
	}
	order := []string{"start Ask the box to wrap up", "start Box deleted", "start Server created", "start Joined the tailnet", "start Box reachable", "start Install supervisor", "start Unlock secrets", "start Dev tools installed"}
	last := -1
	for _, e := range order {
		i := idx(e)
		if i <= last {
			t.Fatalf("%q out of order (%d after %d): %v", e, i, last, rec.Events)
		}
		last = i
	}
	assertAllStartedFinished(t, rec)
}

func TestAttachRecordsStepsAndPrintsNothingElse(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	rec := withRec(&d)
	var buf strings.Builder
	attachOut = &buf
	err := Attach(context.Background(), d, session.Herdr{}, false, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "", errors.New("none") })
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("attach printed %q", buf.String())
	}
	assertEvents(t, rec,
		"start Policy and limits", "done Policy and limits",
		"start SSH key, firewall, volume", "done SSH key, firewall, volume",
		"start Box found", "detail Box found: jauntbox-alice (running)", "done Box found",
		"start Box online", "done Box online",
		"start Dev tools installed", "done Dev tools installed",
		"start Install supervisor", "skip Install supervisor: already running",
		"start Unlock secrets", "done Unlock secrets")
}

func TestResizeFailureModes(t *testing.T) {
	fastPoll(t)
	osExecutable = func() (string, error) { return "/bin/true", nil }
	t.Run("push never gets ssh", func(t *testing.T) {
		d, p, _, _, _, spawns := newDeps(t)
		withBox(p)
		rec := withRec(&d)
		d.Root = &flakyCopyRemote{copyFails: 1 << 30}
		old := resizePushWait
		resizePushWait = 20 * time.Millisecond
		defer func() { resizePushWait = old }()
		_, err := Resize(context.Background(), d, "big")
		if err == nil || !strings.Contains(err.Error(), "joined the tailnet but is not accepting ssh") {
			t.Fatalf("err = %v", err)
		}
		if *spawns != 0 {
			t.Fatalf("spawned %d", *spawns)
		}
		if !contains(rec.Events, "fail Box reachable: "+err.Error()) {
			t.Fatalf("events = %v", rec.Events)
		}
	})
	t.Run("event record failure still pushes", func(t *testing.T) {
		d, p, _, r, _, spawns := newDeps(t)
		withBox(p)
		rec := withRec(&d)
		// the down request also emits; let only box.created fail
		d.Events = kindFailSink{kind: events.BoxCreated}
		_, err := Resize(context.Background(), d, "big")
		if err == nil || !strings.Contains(err.Error(), "recording the box.created event failed") {
			t.Fatalf("err = %v", err)
		}
		if *spawns != 0 || !contains(rec.Events, "done Install supervisor") {
			t.Fatalf("spawns=%d events=%v", *spawns, rec.Events)
		}
		_ = r
	})
}

type kindFailSink struct{ kind string }

func (s kindFailSink) Emit(e events.Event) error {
	if e.Kind == s.kind {
		return errors.New("disk full")
	}
	return nil
}

// cancelProv cancels the context while deleting, like Ctrl-C.
type cancelProv struct {
	*fakeProv
	cancel context.CancelFunc
}

func (c cancelProv) DeleteBox(context.Context, int64) error {
	c.cancel()
	return context.Canceled
}

func TestDownCancelledDuringDeleteLeavesStepOpen(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	withBox(p)
	rec := withRec(&d)
	ctx, cancel := context.WithCancel(context.Background())
	d.Prov = cancelProv{p, cancel}
	err := Down(ctx, d, true)
	if err == nil {
		t.Fatal("want error")
	}
	rec.Close()
	for _, e := range rec.Events {
		if strings.HasPrefix(e, "fail ") {
			t.Fatalf("failed instead of interrupted: %v", rec.Events)
		}
	}
	if !contains(rec.Events, "interrupted Delete the box") {
		t.Fatalf("events = %v", rec.Events)
	}
}

func TestStatusSurfacesUnlockError(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		d, p, _, r, _, _ := newDeps(t)
		withSecrets(t, &d)
		withBox(p)
		r.locked = true
		r.out = []byte(`{"locked":true}`)
		rep, err := Status(context.Background(), d)
		if err != nil || !strings.Contains(rep.UnlockErr, "jauntbox setup github") {
			t.Fatalf("%+v %v", rep, err)
		}
	})
	t.Run("push error", func(t *testing.T) {
		d, p, _, r, _, _ := newDeps(t)
		withSecrets(t, &d, "agents.env", "K=v\n")
		withBox(p)
		r.locked, r.inputErr = true, os.ErrPermission
		r.out = []byte(`{"locked":true}`)
		rep, _ := Status(context.Background(), d)
		if !strings.Contains(rep.UnlockErr, "permission denied") {
			t.Fatalf("%+v", rep)
		}
	})
	t.Run("ok", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		withSecrets(t, &d, "agents.env", "K=v\n")
		withBox(p)
		rep, _ := Status(context.Background(), d)
		if rep.UnlockErr != "" {
			t.Fatalf("%+v", rep)
		}
	})
}

// sshDown is a transport failure (ssh exits 255); notInstalledErr is the check
// command itself exiting non-zero.
func sshDown(t *testing.T) error         { return exitErr(t, 255) }
func notInstalledErr(t *testing.T) error { return exitErr(t, 1) }

func TestSupervisorCheckRetriesSSHFailure(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	rec := withRec(&d)
	r.checkFails, r.checkErr = 2, sshDown(t)
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "copy:") {
			t.Fatalf("re-pushed after a transient ssh failure: %v", r.cmds)
		}
	}
	assertEvents(t, rec, "start Install supervisor", "skip Install supervisor: already running",
		"start Unlock secrets", "done Unlock secrets")
}

func TestSupervisorCheckGivesUpAfterThreeSSHFailures(t *testing.T) {
	d, _, r := attachFixture(t)
	r.checkFails, r.checkErr = 1<<30, sshDown(t)
	rec := withRec(&d)
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range r.cmds {
		if c == supervisorCheck {
			n++
		}
	}
	if n != 3 || !contains(rec.Events, "start Install supervisor") || !contains(rec.Events, "done Install supervisor") {
		t.Fatalf("checks=%d events=%v", n, rec.Events)
	}
}

func TestSupervisorCheckNotInstalledDecidesAtOnce(t *testing.T) {
	d, _, r := attachFixture(t)
	r.noSupervisor = true
	rec := withRec(&d)
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range r.cmds {
		if c == supervisorCheck {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("checked %d times, want 1", n)
	}
	// One step only: no tailnet join, no "Box reachable".
	assertEvents(t, rec, "start Install supervisor", "done Install supervisor",
		"start Unlock secrets", "done Unlock secrets")
}

func TestRePushRetriesAndWarningsGoOnInstallStep(t *testing.T) {
	oldP, oldR := pollInterval, rootSSHWait
	pollInterval, rootSSHWait = time.Millisecond, 20*time.Millisecond
	defer func() { pollInterval, rootSSHWait = oldP, oldR }()
	d, _, r := attachFixture(t)
	r.noSupervisor = true
	rec := withRec(&d)
	d.Root = &rootRefusedRemote{}
	err := EnsureSupervisor(context.Background(), d)
	if err == nil {
		t.Fatal("want error")
	}
	if len(rec.Events) < 3 || rec.Events[0] != "start Install supervisor" ||
		!strings.HasPrefix(rec.Events[1], "warn Install supervisor: ") ||
		rec.Events[2] != "fail Install supervisor: "+err.Error() {
		t.Fatalf("events = %v", rec.Events)
	}
	for _, e := range rec.Events {
		if strings.Contains(e, "Box reachable") || strings.Contains(e, "Joined the tailnet") {
			t.Fatalf("unexpected %q", e)
		}
	}
}

func TestAttachExistingBoxSupervisorCheckBlipNoRePush(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	rec := withRec(&d)
	r.checkFails, r.checkErr = 1, sshDown(t)
	err := Attach(context.Background(), d, session.Herdr{}, false, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "", errors.New("none") })
	if err != nil {
		t.Fatal(err)
	}
	if contains(rec.Events, "start Joined the tailnet") || contains(rec.Events, "start Box reachable") {
		t.Fatalf("repeated steps: %v", rec.Events)
	}
	if !contains(rec.Events, "skip Install supervisor: already running") {
		t.Fatalf("events = %v", rec.Events)
	}
}

func TestAttachExistingBoxReallyNotInstalled(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	rec := withRec(&d)
	r.noSupervisor = true
	err := Attach(context.Background(), d, session.Herdr{}, false, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "", errors.New("none") })
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for k, e := range rec.Events {
		if e == "done Dev tools installed" {
			i = k
		}
	}
	if i < 0 {
		t.Fatalf("no dev tools step: %v", rec.Events)
	}
	assertEvents(t, &ui.Recorder{Events: rec.Events[i+1:]}, "start Install supervisor", "done Install supervisor",
		"start Unlock secrets", "done Unlock secrets")
	if contains(rec.Events, "start Joined the tailnet") || contains(rec.Events, "start Box reachable") {
		t.Fatalf("repeated steps: %v", rec.Events)
	}
}

func TestReadyExistingStepOrder(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	rec := withRec(&d)
	if err := ReadyExisting(context.Background(), d, *p.box); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, rec, "start Box online", "done Box online",
		"start Dev tools installed", "done Dev tools installed",
		"start Install supervisor", "skip Install supervisor: already running",
		"start Unlock secrets", "done Unlock secrets")
}

func TestResizeEndsWithDevTools(t *testing.T) {
	fastPoll(t)
	d, p, _, _, _, _ := newDeps(t)
	withBox(p)
	rec := withRec(&d)
	osExecutable = func() (string, error) { return "/bin/true", nil }
	if _, err := Resize(context.Background(), d, "big"); err != nil {
		t.Fatal(err)
	}
	n := len(rec.Events)
	if n < 2 || rec.Events[n-2] != "start Dev tools installed" || rec.Events[n-1] != "done Dev tools installed" {
		t.Fatalf("tail = %v", rec.Events[max(0, n-4):])
	}
}

// onlineAfter reports the device online once the fake clock passes at.
type onlineAfter struct {
	*fakeMesh
	now *time.Time
	at  time.Time
}

func (m onlineAfter) DeviceOnline(context.Context, string) (bool, error) {
	return !m.now.Before(m.at), nil
}

func TestWaitOnlineGivesRecentBoxTheJoinWindow(t *testing.T) {
	run := func(t *testing.T, created time.Duration) error {
		d, p, _ := attachFixture(t)
		withBox(p)
		clock := time.Now()
		nowFn = func() time.Time { return clock }
		t.Cleanup(func() { nowFn = time.Now })
		sleepFn = func(_ context.Context, dur time.Duration) error { clock = clock.Add(dur); return nil }
		d.Mesh = onlineAfter{d.Mesh.(*fakeMesh), &clock, clock.Add(onlineWait + 2*time.Minute)}
		box := *p.box
		box.Created = clock.Add(-created)
		return waitOnline(context.Background(), d, "jauntbox-alice", box)
	}
	if err := run(t, time.Minute); err != nil {
		t.Fatalf("recent box should wait past onlineWait: %v", err)
	}
	if err := run(t, 2*time.Hour); err == nil || !strings.Contains(err.Error(), "is not on the tailnet after") {
		t.Fatalf("old box err = %v", err)
	}
}

func TestWaitOnlineErrorNamesTheTimeAllowed(t *testing.T) {
	run := func(t *testing.T, created time.Duration) error {
		d, p, _ := attachFixture(t)
		withBox(p)
		clock := time.Now()
		nowFn = func() time.Time { return clock }
		t.Cleanup(func() { nowFn = time.Now })
		sleepFn = func(_ context.Context, dur time.Duration) error { clock = clock.Add(dur); return nil }
		d.Mesh = onlineAfter{d.Mesh.(*fakeMesh), &clock, clock.Add(24 * time.Hour)}
		box := *p.box
		box.Created = clock.Add(-created)
		return waitOnline(context.Background(), d, "jauntbox-alice", box)
	}
	// plain wait: onlineWait
	if err := run(t, 2*time.Hour); err == nil || !strings.Contains(err.Error(), "not on the tailnet after "+onlineWait.String()) {
		t.Fatalf("plain: %v", err)
	}
	// created a minute ago: the join window (DefaultPushWait from creation) is longer
	want := (DefaultPushWait - time.Minute).Round(time.Second).String()
	err := run(t, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not on the tailnet after "+want) {
		t.Fatalf("extended: want %q in %v", want, err)
	}
}

type killedRemote struct{ cancel context.CancelFunc }

func (r killedRemote) Run(context.Context, string, ...string) ([]byte, error) {
	r.cancel()
	err := exec.Command("sh", "-c", "kill -9 $$").Run()
	return nil, fmt.Errorf("ssh x: %w: ", err)
}
func (r killedRemote) RunInput(context.Context, string, []byte, ...string) ([]byte, error) {
	return nil, nil
}
func (r killedRemote) Copy(context.Context, string, string, string) error { return nil }

func TestSupervisorCheckInterruptedStartsNoStep(t *testing.T) {
	d, _, _ := attachFixture(t)
	rec := withRec(&d)
	ctx, cancel := context.WithCancel(context.Background())
	d.Remote = killedRemote{cancel}
	err := EnsureSupervisor(ctx, d)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(rec.Events) != 0 {
		t.Fatalf("events = %v", rec.Events)
	}
}
