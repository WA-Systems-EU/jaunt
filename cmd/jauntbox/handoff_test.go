package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

// assertHandedOff checks the Ctrl-C hand-off contract: one spawn of
// internal-push after the foreground poll, the interrupted line, then the note.
func assertHandedOff(t *testing.T, f *fx, stderr string, err error) {
	t.Helper()
	if !errors.Is(err, errReported) {
		t.Fatalf("want errReported, got %v", err)
	}
	if f.log.count("spawn:internal-push") != 1 {
		t.Fatalf("spawns: %v", f.log.lines)
	}
	ii := strings.Index(stderr, "interrupted")
	in := strings.Index(stderr, "the box keeps getting ready in the background; log: ")
	if ii < 0 || in < ii || !strings.Contains(stderr, "push.log") {
		t.Fatalf("want the interrupted line, then the note:\n%s", stderr)
	}
	if strings.Contains(stderr, "context canceled") {
		t.Fatalf("context canceled printed:\n%s", stderr)
	}
}

func oldBox() *provider.Box {
	return &provider.Box{ID: 5, Name: "jauntbox-alice", Status: "running", Created: time.Now().Add(-time.Hour)}
}

func TestResizeInterruptAfterCreateHandsOff(t *testing.T) {
	f := newFx(t)
	f.prov.box = oldBox()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mesh.online = func() bool {
		f.log.add("poll")
		cancel() // Ctrl-C while the foreground push waits for the new box
		return false
	}
	_, stderr, err := f.run(ctx, "resize", "big")
	assertHandedOff(t, f, stderr, err)
	if f.log.index("spawn:internal-push") < f.log.index("poll") {
		t.Fatalf("spawned before the foreground push returned: %v", f.log.lines)
	}
}

func TestResizeInterruptDuringDownSpawnsNothing(t *testing.T) {
	f := newFx(t)
	f.prov.box = oldBox()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.rem.hook = func(c string) {
		if strings.Contains(c, "jauntctl stop") {
			cancel()
		}
	}
	_, stderr, err := f.run(ctx, "resize", "big")
	if err == nil || errors.Is(err, errReported) {
		t.Fatalf("want the plain error, got %v", err)
	}
	if len(f.spawn) != 0 {
		t.Fatalf("spawns: %v", f.spawn)
	}
	if strings.Contains(stderr, "keeps getting ready") {
		t.Fatalf("note printed:\n%s", stderr)
	}
}

func TestBareInterruptAfterCreateHandsOff(t *testing.T) {
	f := newFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mesh.online = func() bool { cancel(); return false }
	_, stderr, err := f.run(ctx, "--nested")
	assertHandedOff(t, f, stderr, err)
}

func TestBareInterruptExistingBoxHandsOff(t *testing.T) {
	f := newFx(t)
	f.prov.box = oldBox()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mesh.online = func() bool { cancel(); return false }
	_, stderr, err := f.run(ctx, "--nested")
	assertHandedOff(t, f, stderr, err)
}

func TestBareInterruptBeforeBoxSpawnsNothing(t *testing.T) {
	f := newFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.prov.hook = cancel // Ctrl-C while the SSH key is ensured: no server yet
	_, _, err := f.run(ctx, "--nested")
	if err == nil || errors.Is(err, errReported) || len(f.spawn) != 0 {
		t.Fatalf("err=%v spawns=%v", err, f.spawn)
	}
}

func TestReadyLinesAcrossSpringForward(t *testing.T) {
	cfg := config.Default()
	cfg.User, cfg.Timezone = "alice", "Europe/Paris"
	cfg.Limits.EveningCutoff = ""
	cfg.Limits.MaxUptime = config.Duration(12 * time.Hour)
	loc, _ := time.LoadLocation("Europe/Paris")
	// Sunday 29 March 2026: the clocks go forward at 02:00, so noon to noon is 23 hours.
	sat := time.Date(2026, 3, 28, 20, 0, 0, 0, loc)
	_, lines := readyLinesAt(cfg, cfg.Limits, sat, sat, sat, "alice")
	if !strings.Contains(lines[0], "tomorrow") {
		t.Errorf("deadline tomorrow: %q", lines[0])
	}
	sun := time.Date(2026, 3, 29, 12, 0, 0, 0, loc)
	up := time.Date(2026, 3, 28, 6, 0, 0, 0, loc)
	_, lines = readyLinesAt(cfg, cfg.Limits, sun, sun, up, "alice")
	if !strings.Contains(lines[0], "yesterday") {
		t.Errorf("deadline yesterday: %q", lines[0])
	}
}
