package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

type recSink struct{ kinds []string }

func (s *recSink) Emit(e events.Event) error { s.kinds = append(s.kinds, e.Kind); return nil }

var fixedNow = time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)

func newDeps(t *testing.T) (Deps, *fakeProv, *fakeMesh, *fakeRemote, *recSink, *int) {
	t.Helper()
	cfg := config.Default()
	cfg.User = "alice"
	cfg.Timezone = "Europe/Paris"
	cfg.SSHPublicKey = "ssh-ed25519 AAAA"
	p, m, r, s := &fakeProv{}, &fakeMesh{}, &fakeRemote{}, &recSink{}
	spawns := 0
	d := Deps{Cfg: cfg, Policy: policy.Local{Cfg: cfg}, Prov: p, Mesh: m, Root: r, Remote: &fakeRemote{out: []byte("jauntctl dev (commit abc1234)\n")}, Events: s,
		Now: func() time.Time { return fixedNow }, HetznerToken: "hc-T",
		Spawn: func(args ...string) error {
			if len(args) != 1 || args[0] != "internal-push" {
				t.Errorf("spawn args = %v", args)
			}
			spawns++
			return nil
		}}
	return d, p, m, r, s, &spawns
}

func TestUpCreatesOnce(t *testing.T) {
	d, p, m, _, s, spawns := newDeps(t)
	ctx := context.Background()
	res, err := Up(ctx, d, "small")
	if err != nil || !res.Created {
		t.Fatalf("first up: %+v %v", res, err)
	}
	if *spawns != 1 || m.keys != 1 || p.count("create") != 1 {
		t.Fatalf("spawns=%d keys=%d creates=%d", *spawns, m.keys, p.count("create"))
	}
	if len(s.kinds) != 1 || s.kinds[0] != events.BoxCreated {
		t.Fatalf("events = %v", s.kinds)
	}
	res, err = Up(ctx, d, "small")
	if err != nil || res.Created {
		t.Fatalf("second up: %+v %v", res, err)
	}
	if *spawns != 1 || m.keys != 1 || p.count("create") != 1 || len(s.kinds) != 1 {
		t.Fatalf("second up had side effects: spawns=%d keys=%d creates=%d", *spawns, m.keys, p.count("create"))
	}
	if p.count("vol") != 2 {
		t.Fatalf("EnsureVolume should run each time (idempotent)")
	}
}

func TestUpNamesLabelsAndIDs(t *testing.T) {
	d, p, m, _, _, _ := newDeps(t)
	if _, err := Up(context.Background(), d, "small"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		got        named
		name, role string
	}{{p.key, "jauntbox-alice", "key"}, {p.fw, "jauntbox-alice", "fw"}, {p.vol, "jauntbox-alice-home", "home"}} {
		if c.got.name != c.name || !reflect.DeepEqual(c.got.labels, map[string]string{"jauntbox": "alice", "role": c.role}) {
			t.Errorf("%s: %+v", c.role, c.got)
		}
	}
	s := p.spec
	if s.VolumeID != volID || s.SSHKeyID != keyID || s.FirewallID != fwID || s.Name != "jauntbox-alice" {
		t.Errorf("spec ids = %+v", s)
	}
	if !reflect.DeepEqual(m.tags, []string{"tag:jauntbox-alice"}) || m.ttl != time.Hour {
		t.Errorf("auth key tags=%v ttl=%v", m.tags, m.ttl)
	}
	if !strings.Contains(s.UserData, volDevice) || !strings.Contains(s.UserData, "tag:jauntbox-alice") {
		t.Error("user-data lacks volume device or tag")
	}
}

func TestUpConcurrentLoserReturnsExisting(t *testing.T) {
	d, p, _, _, s, spawns := newDeps(t)
	p.failCreate = true
	p.racedBox = &provider.Box{ID: 5, Name: "jauntbox-alice"}
	res, err := Up(context.Background(), d, "small")
	if err != nil || res.Created || res.Box.ID != 5 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if *spawns != 0 || len(s.kinds) != 0 {
		t.Fatalf("spawns=%d events=%v", *spawns, s.kinds)
	}
}

func TestUpCreateFailureHintsRerun(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	p.failCreate = true
	_, err := Up(context.Background(), d, "small")
	if err == nil || !strings.Contains(err.Error(), "re-run `jauntbox up`") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpEmitFailureSurfacedNotFatal(t *testing.T) {
	d, _, _, _, _, spawns := newDeps(t)
	d.Events = failSink{}
	res, err := Up(context.Background(), d, "small")
	if !res.Created || err == nil || !strings.Contains(err.Error(), "box.created") || *spawns != 1 {
		t.Fatalf("res=%+v err=%v spawns=%d", res, err, *spawns)
	}
}

type failSink struct{}

func (failSink) Emit(events.Event) error { return errors.New("disk full") }

func TestPushBinaryRetriesTransientErrors(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, m, r, _, _ := newDeps(t)
	m.errs = 2
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(r.order) != 3 { // copy, install, unlock check
		t.Fatalf("order = %v", r.order)
	}
}

func TestUpUnknownProfile(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	_, err := Up(context.Background(), d, "xl")
	if err == nil || err.Error() != `unknown profile "xl"` {
		t.Fatalf("err = %v", err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("provider called: %v", p.calls)
	}
}

func TestUpUserDataCarriesBoxInfo(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	if _, err := Up(context.Background(), d, "big"); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		WriteFiles []struct{ Path, Encoding, Content string } `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(p.spec.UserData), &doc); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range doc.WriteFiles {
		if f.Path != boxinfo.Path {
			continue
		}
		found = true
		c := f.Content
		if f.Encoding != "gz+b64" {
			t.Fatalf("box info encoding %q, want gz+b64", f.Encoding)
		}
		b, _ := base64.StdEncoding.DecodeString(c)
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		c = string(raw)
		tmp := t.TempDir() + "/box.json"
		if err := writeFile(tmp, c); err != nil {
			t.Fatal(err)
		}
		bi, err := boxinfo.Read(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if bi.Profile != "big" || !bi.UpAt.Equal(fixedNow) || bi.Timezone != "Europe/Paris" || bi.User != "alice" || bi.Hostname != "jauntbox-alice" {
			t.Fatalf("boxinfo = %+v", bi)
		}
		if bi.Limits != d.Cfg.Limits {
			t.Fatalf("limits not frozen: %+v", bi.Limits)
		}
	}
	if !found {
		t.Fatal("box.json not in user-data")
	}
	if p.spec.ServerType != "ccx33" || p.spec.Labels["role"] != "box" || p.spec.Labels["jauntbox"] != "alice" {
		t.Fatalf("spec = %+v", p.spec)
	}
}

func TestPushBinaryWaitsForMesh(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, m, r, _, _ := newDeps(t)
	m.falses = 2
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if m.polls != 3 {
		t.Fatalf("polls = %d", m.polls)
	}
	if len(r.order) != 3 || r.order[0] != "copy:/tmp/jauntctl" || r.order[1] != "run" || r.order[2] != "run" {
		t.Fatalf("order = %v", r.order)
	}
	want := "[" + installCmd("alice") + "]"
	if r.cmds[0] != want || installCmd("alice") != "cd / && install -m 0755 /tmp/jauntctl /usr/local/bin/jauntctl && ln -sf jauntctl /usr/local/bin/jauntbox && systemctl enable --now jauntbox-supervise.timer && runuser -u alice -- git config --global --replace-all credential.https://github.com.helper '' && runuser -u alice -- git config --global --add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential' && runuser -u alice -- git config --global credential.https://github.com.useHttpPath true && runuser -u alice -- git config --global diff.ignoreSubmodules all && runuser -u alice -- git config --global status.submoduleSummary false && runuser -u alice -- git config --global submodule.recurse false && runuser -u alice -- git config --global core.fsmonitor false && { systemctl enable --now jauntbox-agent-net.service || true; }" {
		t.Fatalf("cmd = %q", r.cmds[0])
	}
	assertRemoteUnused(t, d)
}

func TestPushBinaryTimesOut(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, m, r, _, _ := newDeps(t)
	m.falses = -1
	err := PushBinary(context.Background(), d, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "never joined the tailnet") {
		t.Fatalf("err = %v", err)
	}
	if len(r.order) != 0 {
		t.Fatalf("remote used: %v", r.order)
	}
}

func TestPushBinaryNeverJoinedDeletesBox(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, p, m, _, _, _ := newDeps(t)
	withBox(p)
	m.falses = -1
	err := PushBinary(context.Background(), d, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "never joined the tailnet") || !strings.Contains(err.Error(), "deleted it") {
		t.Fatalf("err = %v", err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != 9 {
		t.Fatalf("deleted = %v", p.deleted)
	}
}

func TestPushBinaryMeshErrorDoesNotDelete(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, p, m, _, _, _ := newDeps(t)
	withBox(p)
	m.errs = 1 << 30
	err := PushBinary(context.Background(), d, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "NOT deleted") || !strings.Contains(err.Error(), "transient") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "safety timer") {
		t.Errorf("false safety-timer claim remains: %v", err)
	}
	if len(p.deleted) != 0 {
		t.Fatalf("deleted despite API error: %v", p.deleted)
	}
}

func TestWithoutSpawnNeverSpawns(t *testing.T) {
	d, _, _, _, _, spawns := newDeps(t)
	if _, err := Up(context.Background(), WithoutSpawn(d), "small"); err != nil {
		t.Fatal(err)
	}
	if *spawns != 0 {
		t.Fatalf("spawned %d times", *spawns)
	}
}

type flakyCopyRemote struct {
	fakeRemote
	copyFails int
	copies    int
}

func (r *flakyCopyRemote) Copy(ctx context.Context, host, local, remote string) error {
	r.copies++
	if r.copies <= r.copyFails {
		return errors.New("user not created yet")
	}
	return r.fakeRemote.Copy(ctx, host, local, remote)
}

func TestPushBinaryRetriesCopyUntilUserExists(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, _, _, _, _ := newDeps(t)
	r := &flakyCopyRemote{copyFails: 2}
	d.Root = r
	osExecutable = func() (string, error) { return fakeExe, nil }
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if r.copies != 3 {
		t.Fatalf("copies = %d", r.copies)
	}
}

func TestPushBinaryCopyTimeoutReportsLastError(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, _, _, _, _ := newDeps(t)
	d.Root = &flakyCopyRemote{copyFails: 1 << 30}
	osExecutable = func() (string, error) { return fakeExe, nil }
	err := PushBinary(context.Background(), d, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "user not created yet") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpWaitsForVolumeDetachBeforeCreate(t *testing.T) {
	d, p, m, _, _, _ := newDeps(t)
	var seq []string
	p.seq = &seq
	p.volAttached, p.volDetachPolls = true, 2
	res, err := Up(context.Background(), d, "small")
	if err != nil || !res.Created {
		t.Fatalf("up: %+v %v", res, err)
	}
	if len(seq) != 2 || seq[0] != "waitdetached" || !strings.HasPrefix(seq[1], "create:") {
		t.Fatalf("seq = %v", seq)
	}
	if m.keys != 1 {
		t.Fatalf("keys = %d", m.keys)
	}
}

func TestUpVolumeNeverDetachesWastesNoAuthKey(t *testing.T) {
	setDetachTimeout(t, 20*time.Millisecond)
	d, p, m, _, _, spawns := newDeps(t)
	p.volAttached, p.volNeverDetaches = true, true
	_, err := Up(context.Background(), d, "small")
	if err == nil || !strings.Contains(err.Error(), "still attached") || !strings.Contains(err.Error(), "jauntbox up") {
		t.Fatalf("err = %v", err)
	}
	if m.keys != 0 || p.count("create") != 0 || *spawns != 0 {
		t.Fatalf("keys=%d creates=%d spawns=%d", m.keys, p.count("create"), *spawns)
	}
}

func TestUpExistingBoxDoesNotWaitForVolume(t *testing.T) {
	d, p, _, _, _, _ := newDeps(t)
	withBox(p)
	if _, err := Up(context.Background(), d, "small"); err != nil {
		t.Fatal(err)
	}
	if p.count("waitdetached") != 0 {
		t.Fatal("waited for volume although the box exists")
	}
}

func setDetachTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := volumeDetachTimeout
	volumeDetachTimeout = d
	t.Cleanup(func() { volumeDetachTimeout = old })
}

func TestUpWaitFailureReturnsRacedBox(t *testing.T) {
	d, p, m, _, _, spawns := newDeps(t)
	p.volAttached, p.volInUse = true, true // the winner's server holds the volume
	p.racedBox = &provider.Box{ID: 77, Name: "jauntbox-alice"}
	start := time.Now()
	res, err := Up(context.Background(), d, "small")
	if err != nil || res.Created || res.Box.ID != 77 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if time.Since(start) > time.Second || m.keys != 0 || p.count("create") != 0 || *spawns != 0 {
		t.Fatalf("keys=%d creates=%d spawns=%d", m.keys, p.count("create"), *spawns)
	}
}

func TestUpWaitErrorWording(t *testing.T) {
	setDetachTimeout(t, 20*time.Millisecond)
	t.Run("deadline", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		p.volNeverDetaches = true
		_, err := Up(context.Background(), d, "small")
		if err == nil || !strings.Contains(err.Error(), "still attached") || !strings.Contains(err.Error(), "re-run `jauntbox up`") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("in use", func(t *testing.T) {
		d, p, m, _, _, _ := newDeps(t)
		p.volInUse = true
		_, err := Up(context.Background(), d, "small")
		if !errors.Is(err, provider.ErrVolumeInUse) || !strings.Contains(err.Error(), "stranger") ||
			!strings.Contains(err.Error(), "not a jauntbox box") || strings.Contains(err.Error(), "re-run") || m.keys != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		p.volNeverDetaches = true
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Up(ctx, d, "small")
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "still attached") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("other", func(t *testing.T) {
		d, p, _, _, _, _ := newDeps(t)
		p.volWaitErr = errors.New("boom")
		_, err := Up(context.Background(), d, "small")
		if err == nil || !strings.Contains(err.Error(), "check volume attachment: boom") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSupervisorCheckNeedsJauntctl(t *testing.T) {
	if supervisorCheck != "test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer && { /usr/local/bin/jauntctl --version 2>/dev/null || true; }" {
		t.Fatalf("supervisorCheck = %q", supervisorCheck)
	}
}

func TestInstallCmdRunsAsRoot(t *testing.T) {
	cmd := installCmd("alice")
	if !strings.HasPrefix(cmd, "cd / && install ") {
		t.Fatalf("installCmd must start with cd /: %q", cmd)
	}
	if strings.Contains(cmd, "sudo") {
		t.Fatalf("installCmd must not use sudo: %q", cmd)
	}
	parts := []string{
		"runuser -u alice -- git config --global --replace-all credential.https://github.com.helper ''",
		"runuser -u alice -- git config --global --add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential'",
		"runuser -u alice -- git config --global credential.https://github.com.useHttpPath true",
	}
	pos := strings.Index(cmd, "systemctl enable")
	for _, p := range parts {
		i := strings.Index(cmd, p)
		if i < pos {
			t.Fatalf("%q missing or out of order in %q", p, cmd)
		}
		pos = i
	}
	if n := strings.Count(cmd, "git config"); n != strings.Count(cmd, "runuser -u alice -- git config") || n != 7 {
		t.Fatalf("every git config must run via runuser (%d): %q", n, cmd)
	}
}

func TestRootSSHFailureNamesACLSnippet(t *testing.T) {
	cause := mkExit(255)
	run := map[string]func(Deps) error{
		"down":   func(d Deps) error { return Down(context.Background(), d, false) },
		"night":  func(d Deps) error { return Night(context.Background(), d, "07:00") },
		"unlock": func(d Deps) error { return PushSecrets(context.Background(), d) },
		"push":   func(d Deps) error { return PushBinary(context.Background(), d, 20*time.Millisecond) },
		"status": nil,
	}
	want := "root ssh to jauntbox-alice failed; your tailnet policy must allow ssh as root to tag:jauntbox-alice: run `jauntbox acl-snippet` and update the policy"
	for name, f := range run {
		d, p, _, r, _, _ := newDeps(t)
		withSecrets(t, &d, "agents.env", "K=v\n")
		withBox(p)
		p.waitErr = errors.New("still there") // down: the box is not already gone
		r.err, r.inputErr = cause, cause
		osExecutable = func() (string, error) { return fakeExe, nil }
		var err error
		if f == nil {
			var rep StatusReport
			rep, err = Status(context.Background(), d)
			if !strings.Contains(rep.Err, want) {
				t.Errorf("status Err = %q", rep.Err)
			}
		} else {
			err = f(d)
		}
		if name != "status" {
			if !errors.Is(err, ErrRootSSH) || !errors.Is(err, cause) || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		assertRemoteUnused(t, d)
	}
}

func assertRemoteUnused(t *testing.T, d Deps) {
	t.Helper()
	if rem := d.Remote.(*fakeRemote); len(rem.order) != 0 {
		t.Fatalf("Remote received calls (no sudo fallback allowed): %v", rem.cmds)
	}
}

type rootRefusedRemote struct {
	fakeRemote
	copies int
}

func (r *rootRefusedRemote) Copy(context.Context, string, string, string) error {
	r.copies++
	return mkExit(255)
}

func TestPushBinaryRootRefusedFailsFast(t *testing.T) {
	oldP, oldR := pollInterval, rootSSHWait
	pollInterval, rootSSHWait = time.Millisecond, 20*time.Millisecond
	defer func() { pollInterval, rootSSHWait = oldP, oldR }()
	d, _, _, _, _, _ := newDeps(t)
	r := &rootRefusedRemote{}
	d.Root = r
	osExecutable = func() (string, error) { return fakeExe, nil }
	start := time.Now()
	err := PushBinary(context.Background(), d, time.Hour)
	if !errors.Is(err, ErrRootSSH) || !strings.Contains(err.Error(), "acl-snippet") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second || r.copies < 2 {
		t.Fatalf("took %v, copies %d", time.Since(start), r.copies)
	}
}

func TestInstallCmdSetsHostGitGuards(t *testing.T) {
	cmd := installCmd("alice")
	for _, p := range []string{
		"runuser -u alice -- git config --global diff.ignoreSubmodules all",
		"runuser -u alice -- git config --global status.submoduleSummary false",
		"runuser -u alice -- git config --global submodule.recurse false",
		"runuser -u alice -- git config --global core.fsmonitor false",
	} {
		if !strings.Contains(cmd, p) {
			t.Errorf("installCmd lacks %q: %s", p, cmd)
		}
	}
	const step = "{ systemctl enable --now jauntbox-agent-net.service || true; }"
	i := strings.Index(cmd, step)
	if i < 0 {
		t.Fatalf("agent-net step must be best-effort (|| true): %s", cmd)
	}
	if !strings.HasSuffix(cmd, step) || i < strings.Index(cmd, "core.fsmonitor false") {
		t.Fatalf("agent-net step must come last, after the git guards: %s", cmd)
	}
}

func TestBoxBinarySourceSibling(t *testing.T) {
	old := osExecutable
	t.Cleanup(func() { osExecutable = old })
	dir := t.TempDir()
	osExecutable = func() (string, error) { return filepath.Join(dir, "jauntbox"), nil }
	pretendLinuxAmd64(t)

	want := "no jauntctl to install on the box: build it next to jauntbox (go build -o <dir> ./cmd/jauntbox ./cmd/jauntctl) or set box_binary"
	if _, err := boxBinarySource(Deps{}); err == nil || err.Error() != want {
		t.Fatalf("missing sibling: err = %v, want %q", err, want)
	}
	sib := filepath.Join(dir, "jauntctl")
	if err := os.WriteFile(sib, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := boxBinarySource(Deps{}); err != nil || s.Local != sib {
		t.Fatalf("sibling: s=%+v err=%v", s, err)
	}
}

// fakeExe is a jauntbox executable path with a jauntctl next to it, so that
// BoxBinaryPath finds a box binary in every test that pushes one.
var fakeExe string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jauntbox-client-test")
	if err != nil {
		panic(err)
	}
	fakeExe = filepath.Join(dir, "jauntbox")
	for _, n := range []string{"jauntbox", "jauntctl"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			panic(err)
		}
	}
	osExecutable = func() (string, error) { return fakeExe, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
