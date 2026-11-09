package client

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
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
	if len(r.order) != 2 || r.order[0] != "copy:/tmp/jauntbox" || r.order[1] != "run" {
		t.Fatalf("order = %v", r.order)
	}
	want := "[" + installCmd + "]"
	if r.cmds[0] != want || !strings.Contains(installCmd, "sudo install -m 0755 /tmp/jauntbox /usr/local/bin/jauntbox && sudo systemctl enable --now jauntbox-supervise.timer") {
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
	if err == nil || !strings.Contains(err.Error(), "did not join the tailnet") {
		t.Fatalf("err = %v", err)
	}
	if len(r.order) != 0 {
		t.Fatalf("remote used: %v", r.order)
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
