package client

import (
	"context"
	"encoding/base64"
	"errors"
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
	d := Deps{Cfg: cfg, Policy: policy.Local{Cfg: cfg}, Prov: p, Mesh: m, Remote: r, Events: s,
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
		if f.Encoding == "b64" {
			b, _ := base64.StdEncoding.DecodeString(c)
			c = string(b)
		}
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
	if len(r.order) != 3 || r.order[0] != "copy:/tmp/jauntbox" || r.order[1] != "run" || r.order[2] != "run" {
		t.Fatalf("order = %v", r.order)
	}
	want := "[" + installCmd + "]"
	if r.cmds[0] != want || installCmd != "sudo install -m 0755 /tmp/jauntbox /usr/local/bin/jauntctl && sudo ln -sf jauntctl /usr/local/bin/jauntbox && sudo systemctl enable --now jauntbox-supervise.timer && git config --global --replace-all credential.https://github.com.helper '' && git config --global --add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential' && git config --global credential.https://github.com.useHttpPath true" {
		t.Fatalf("cmd = %q", r.cmds[0])
	}
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

func TestBoxBinaryPathNonLinuxRequiresConfig(t *testing.T) {
	// On linux/amd64 the running executable is used; elsewhere box_binary is required.
	p, err := BoxBinaryPath(config.Config{BoxBinary: "/x/jauntbox"})
	if err != nil || p == "" {
		t.Fatalf("p=%q err=%v", p, err)
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
	d.Remote = r
	osExecutable = func() (string, error) { return "/bin/true", nil }
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
	d.Remote = &flakyCopyRemote{copyFails: 1 << 30}
	osExecutable = func() (string, error) { return "/bin/true", nil }
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
	if supervisorCheck != "test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer" {
		t.Fatalf("supervisorCheck = %q", supervisorCheck)
	}
}

func TestInstallCmdConfiguresGitHelper(t *testing.T) {
	parts := []string{
		"git config --global --replace-all credential.https://github.com.helper ''",
		"git config --global --add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential'",
		"git config --global credential.https://github.com.useHttpPath true",
	}
	pos := strings.Index(installCmd, "systemctl enable")
	for _, p := range parts {
		i := strings.Index(installCmd, p)
		if i < pos {
			t.Fatalf("%q missing or out of order in %q", p, installCmd)
		}
		pos = i
	}
}
