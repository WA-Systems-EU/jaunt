package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

func withSecrets(t *testing.T, d *Deps, kv ...string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if err := os.WriteFile(filepath.Join(dir, kv[i]), []byte(kv[i+1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.Secrets = secrets.Store{Dir: dir}
}

const markerCmd = "[test -e /run/jauntbox/secrets/.loaded]"

func TestEnsureUnlockedPushesOnlyWhenLocked(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")

	if err := EnsureUnlocked(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 0 || len(r.cmds) != 1 || r.cmds[0] != markerCmd {
		t.Fatalf("unlocked box: cmds=%v inputs=%d", r.cmds, len(r.inputs))
	}

	r.locked = true
	if err := EnsureUnlocked(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("inputs = %d", len(r.inputs))
	}
	if got := r.cmds[len(r.cmds)-1]; got != "[jauntctl secrets-load]" {
		t.Fatalf("last cmd = %s", got)
	}
	var b secrets.Bundle
	if err := json.Unmarshal(r.inputs[0], &b); err != nil || string(b.Files["agents.env"]) != "K=v\n" {
		t.Fatalf("bundle = %+v %v", b, err)
	}
}

func TestPushSecretsRefusesOpenFile(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")
	if err := os.Chmod(filepath.Join(d.Secrets.Dir, "agents.env"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := PushSecrets(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("err = %v", err)
	}
	if len(r.order) != 0 {
		t.Fatalf("remote used: %v", r.order)
	}
}

func TestPushSecretsErrorOmitsPayload(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "TOPSECRET=hunter2\n")
	r.inputErr = os.ErrPermission
	err := PushSecrets(context.Background(), d)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestPushBinaryUnlocksAfterInstall(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")
	r.locked = true
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	want := []string{"copy:/tmp/jauntbox", "run", "run", "runinput"}
	if strings.Join(r.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v", r.order)
	}
	if r.cmds[0] != "["+installCmd("alice")+"]" || r.cmds[1] != markerCmd || r.cmds[2] != "[jauntctl secrets-load]" {
		t.Fatalf("cmds = %v", r.cmds)
	}
}

func TestPushBinaryUnlockFailureIsOnlyAWarning(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = old }()
	var out strings.Builder
	oldOut := attachOut
	attachOut = &out
	defer func() { attachOut = oldOut }()
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")
	r.locked, r.inputErr = true, os.ErrPermission
	if err := PushBinary(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "warning: could not unlock the box:") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStatusReportsLocked(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")
	withBox(p)
	r.out = []byte(`{"locked":true}`)
	rep, err := Status(context.Background(), d)
	if err != nil || !rep.Locked {
		t.Fatalf("%+v %v", rep, err)
	}
	r.out = []byte(`{"locked":false}`)
	rep, _ = Status(context.Background(), d)
	if rep.Locked {
		t.Fatalf("%+v", rep)
	}
}

func TestStatusTriesToUnlockWhenLocked(t *testing.T) {
	d, p, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "K=v\n")
	withBox(p)
	r.locked = true
	r.out = []byte(`{"locked":false}`)
	if _, err := Status(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("inputs = %d", len(r.inputs))
	}
}

func TestEnsureUnlockedEmptyStoreWarnsAndDoesNotPush(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d)
	r.locked = true
	err := EnsureUnlocked(context.Background(), d)
	want := "no secrets stored on this laptop (" + d.Secrets.Dir + "): copy them from your other client or run `jauntbox setup github`"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	if len(r.inputs) != 0 {
		t.Fatalf("pushed: %d", len(r.inputs))
	}
}

func TestPushSecretsEmptyStoreRefuses(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d)
	err := PushSecrets(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "no secrets stored on this laptop") {
		t.Fatalf("err = %v", err)
	}
	if len(r.order) != 0 {
		t.Fatalf("remote used: %v", r.order)
	}
}

func TestBundleWithOnlyPasswordUnlocks(t *testing.T) {
	d, _, _, r, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "BOX_SUDO_PASSWORD=hunter2\n")
	if err := PushSecrets(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("inputs = %d", len(r.inputs))
	}
}

func TestUnlockNotesMissingPassword(t *testing.T) {
	const want = "note: sudo on the box has no password yet; set one with `jauntbox secrets set BOX_SUDO_PASSWORD` and run `jauntbox unlock`"
	d, _, _, _, _, _ := newDeps(t)
	withSecrets(t, &d, "agents.env", "OTHER=1\n")
	if got := UnlockNote(d); got != want {
		t.Fatalf("note = %q", got)
	}
	d2, _, _, _, _, _ := newDeps(t)
	withSecrets(t, &d2, "agents.env", "OTHER=1\nBOX_SUDO_PASSWORD=x\n")
	if got := UnlockNote(d2); got != "" {
		t.Fatalf("note with password set = %q", got)
	}
	d3, _, _, _, _, _ := newDeps(t)
	withSecrets(t, &d3)
	if got := UnlockNote(d3); got != want {
		t.Fatalf("note without store = %q", got)
	}
}
