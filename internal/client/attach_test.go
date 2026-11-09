package client

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/session"
)

type scriptRemote struct {
	cmds             []string
	src              string // output for the ls call
	list             string // output for the workspace list call
	tests, testFails int    // `test -x herdr` calls; first testFails fail
	noSupervisor     bool   // the supervisor check fails
}

func (r *scriptRemote) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	switch {
	case strings.Contains(line, "is-enabled"):
		if r.noSupervisor {
			return nil, errors.New("exit 1")
		}
		return nil, nil
	case args[0] == "test":
		r.tests++
		if r.tests <= r.testFails {
			return nil, errors.New("exit 1")
		}
		return nil, nil
	case args[0] == "ls":
		if r.src == "" {
			return []byte("ls: cannot access"), errors.New("exit 2")
		}
		return []byte(r.src), nil
	case strings.HasSuffix(line, "workspace list") && args[0] == "env":
		return []byte(r.list), nil
	}
	return nil, nil
}
func (r *scriptRemote) Copy(_ context.Context, _, _, remote string) error {
	r.cmds = append(r.cmds, "copy:"+remote)
	return nil
}

func attachFixture(t *testing.T) (Deps, *fakeProv, *scriptRemote) {
	t.Helper()
	d, p, _, _, _, _ := newDeps(t)
	d.Cfg.DefaultProfile = "small"
	r := &scriptRemote{}
	d.Remote = r
	oldExe, oldOut, oldSleep := osExecutable, attachOut, sleepFn
	oldProv := provisionPoll
	provisionPoll = time.Millisecond
	t.Cleanup(func() { provisionPoll = oldProv })
	osExecutable = func() (string, error) { return "/bin/true", nil }
	attachOut = io.Discard
	sleepFn = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { osExecutable, attachOut, sleepFn = oldExe, oldOut, oldSleep })
	return d, p, r
}

func TestAttachUpThenExec(t *testing.T) {
	d, p, r := attachFixture(t)
	r.src = "/home/alice/src/a/\n/home/alice/src/b/\n"
	r.list = `{"result":{"workspaces":[{"label":"a"}]}}`
	var got [][]string
	err := Attach(context.Background(), d, session.Herdr{}, func(argv []string) (int, error) {
		got = append(got, argv)
		return 0, nil
	}, func(string) (string, error) { return "/usr/bin/herdr", nil })
	if err != nil {
		t.Fatal(err)
	}
	if p.count("create") != 1 {
		t.Fatalf("creates = %d", p.count("create"))
	}
	want := []string{"herdr", "--remote", "alice@jauntbox-alice", "--session", "jauntbox"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("exec = %v", got)
	}
	joined := strings.Join(r.cmds, "\n")
	pushed := strings.Index(joined, "copy:/tmp/jauntbox")
	created := strings.Index(joined, "workspace create --cwd /home/alice/src/b --label b --no-focus")
	if pushed < 0 || created < pushed {
		t.Fatalf("push must precede workspace creation:\n%s", joined)
	}
	if strings.Contains(joined, "--label a ") {
		t.Fatalf("existing workspace a recreated:\n%s", joined)
	}
}

func TestAttachExistingBoxSkipsPush(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "", errors.New("none") })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "copy:") {
			t.Fatalf("unexpected push: %v", r.cmds)
		}
	}
	if p.count("create") != 0 {
		t.Fatal("created a second box")
	}
}

func TestAttachExistingBoxEnsuresSupervisorBeforeAttach(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	r.noSupervisor = true
	attached := false
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) {
		attached = true
		joined := strings.Join(r.cmds, "\n")
		if !strings.Contains(joined, "copy:/tmp/jauntbox") || !strings.Contains(joined, "systemctl enable --now jauntbox-supervise.timer") {
			t.Errorf("supervisor not installed before attach:\n%s", joined)
		}
		return 0, nil
	}, func(string) (string, error) { return "", errors.New("none") })
	if err != nil || !attached {
		t.Fatalf("err=%v attached=%v", err, attached)
	}
}

func TestEnsureSupervisorSkipsPushWhenInstalled(t *testing.T) {
	d, _, r := attachFixture(t)
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "copy:") {
			t.Fatalf("pushed although installed: %v", r.cmds)
		}
	}
	if len(r.cmds) != 1 || r.cmds[0] != supervisorCheck {
		t.Fatalf("cmds = %v", r.cmds)
	}
}

func TestInstallSupervisorOnExistingBox(t *testing.T) {
	d, _, r := attachFixture(t)
	r.noSupervisor = true
	spawned := 0
	d.Spawn = func(args ...string) error {
		if len(args) != 1 || args[0] != "internal-push" {
			t.Errorf("spawn args %v", args)
		}
		spawned++
		return nil
	}
	if err := InstallSupervisor(context.Background(), d, false); err != nil || spawned != 1 || len(r.cmds) != 0 {
		t.Fatalf("background: err=%v spawned=%d cmds=%v", err, spawned, r.cmds)
	}
	if err := InstallSupervisor(context.Background(), d, true); err != nil || spawned != 1 {
		t.Fatalf("foreground: err=%v spawned=%d", err, spawned)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "copy:/tmp/jauntbox") {
		t.Errorf("foreground did not push: %v", r.cmds)
	}
}

func TestAttachFallsBackToSSHWithoutLocalHerdr(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	var got []string
	_ = Attach(context.Background(), d, session.Herdr{}, func(a []string) (int, error) { got = a; return 0, nil },
		func(string) (string, error) { return "", errors.New("none") })
	if got[0] != "ssh" {
		t.Fatalf("exec = %v", got)
	}
}

func TestAttachReconnectsOnTransportFailure(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	codes := []int{255, 255, 0}
	var slept []time.Duration
	sleepFn = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	calls := 0
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) {
		c := codes[calls]
		calls++
		return c, nil
	}, func(string) (string, error) { return "x", nil })
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if !reflect.DeepEqual(slept, []time.Duration{2 * time.Second, 4 * time.Second}) {
		t.Fatalf("backoff = %v", slept)
	}
}

func TestAttachBackoffCapsAndGivesUp(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	clock := time.Now()
	nowFn = func() time.Time { return clock }
	t.Cleanup(func() { nowFn = time.Now })
	var slept []time.Duration
	sleepFn = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clock = clock.Add(d)
		return nil
	}
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { return 255, nil },
		func(string) (string, error) { return "x", nil })
	if err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range slept {
		if s > 30*time.Second {
			t.Fatalf("backoff %v exceeds cap", s)
		}
	}
	if slept[len(slept)-1] != 30*time.Second {
		t.Fatalf("backoff never reached cap: %v", slept)
	}
}

func TestAttachStopsOnUserExit(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	for _, code := range []int{0, 1, 130} {
		calls := 0
		err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { calls++; return code, nil },
			func(string) (string, error) { return "x", nil })
		if err != nil || calls != 1 {
			t.Fatalf("code %d: calls=%d err=%v", code, calls, err)
		}
	}
}

func TestAttachSkipsUnsafeProjectNames(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	r.src = "/home/alice/src/ok/\n/home/alice/src/a;rm -rf x/\n"
	r.list = `{"result":{"workspaces":[]}}`
	_ = Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "x", nil })
	joined := strings.Join(r.cmds, "\n")
	if !strings.Contains(joined, "--label ok") || strings.Contains(joined, "rm -rf") && strings.Contains(joined, "create --cwd /home/alice/src/a") {
		t.Fatalf("cmds:\n%s", joined)
	}
}

func TestAttachWaitsForOnlineThenFails(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	d.Mesh.(*fakeMesh).falses = -1
	clock := time.Now()
	nowFn = func() time.Time { return clock }
	t.Cleanup(func() { nowFn = time.Now })
	sleepFn = func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil }
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { t.Fatal("exec"); return 0, nil },
		func(string) (string, error) { return "x", nil })
	if err == nil || !strings.Contains(err.Error(), "not on the tailnet") {
		t.Fatalf("err = %v", err)
	}
}

func TestAttachWaitsForProvisioning(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	r.testFails = 3
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { return 0, nil },
		func(string) (string, error) { return "x", nil })
	if err != nil || r.tests != 4 {
		t.Fatalf("tests=%d err=%v", r.tests, err)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "test -x /home/alice/.local/bin/herdr") {
		t.Fatalf("cmds: %v", r.cmds)
	}
}

func TestAttachProvisioningTimeout(t *testing.T) {
	d, p, r := attachFixture(t)
	withBox(p)
	r.testFails = 1 << 30
	clock := time.Now()
	nowFn = func() time.Time { return clock }
	t.Cleanup(func() { nowFn = time.Now })
	sleepFn = func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil }
	provisionPoll = 10 * time.Second
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) { t.Fatal("exec"); return 0, nil },
		func(string) (string, error) { return "x", nil })
	if err == nil || !strings.Contains(err.Error(), "jauntbox ssh") || !strings.Contains(err.Error(), "cloud-init status --long") {
		t.Fatalf("err = %v", err)
	}
}

func TestAttachReconnectResetsAfterHealthySession(t *testing.T) {
	d, p, _ := attachFixture(t)
	withBox(p)
	clock := time.Now()
	nowFn = func() time.Time { return clock }
	t.Cleanup(func() { nowFn = time.Now })
	var slept []time.Duration
	sleepFn = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clock = clock.Add(d)
		return nil
	}
	// drop, drop, 2h healthy session then drop, then user exit
	runs := []struct {
		dur  time.Duration
		code int
	}{{0, 255}, {0, 255}, {2 * time.Hour, 255}, {0, 0}}
	i := 0
	err := Attach(context.Background(), d, session.Herdr{}, func([]string) (int, error) {
		r := runs[i]
		i++
		clock = clock.Add(r.dur)
		return r.code, nil
	}, func(string) (string, error) { return "x", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 2 * time.Second}
	if !reflect.DeepEqual(slept, want) {
		t.Fatalf("backoff = %v want %v", slept, want)
	}
}
