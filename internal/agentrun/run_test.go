package agentrun

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunIDFormat(t *testing.T) {
	at := time.Date(2026, 11, 10, 23, 4, 5, 0, time.FixedZone("CET", 3600))
	if got := NewRunID("My.Repo_X", at); got != "my.repo_x-20261110-220405" {
		t.Fatalf("id %q", got)
	}
	for _, bad := range []string{"", "..", "a/b", "../x", "x y", "-x", "A"} {
		if validID(bad) {
			t.Errorf("validID(%q) = true", bad)
		}
	}
	if !validID("my.repo_x-20261110-220405") {
		t.Error("valid id refused")
	}
}

func TestPathsStore(t *testing.T) {
	dir := t.TempDir()
	p := Paths{RunsTmp: filepath.Join(dir, "tmp"), RunsVol: filepath.Join(dir, "vol"), Agents: filepath.Join(dir, "agents")}
	r1 := Run{ID: "a-20261110-100000", Repo: "o/a", Mode: "interactive", StartedAt: time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), Max: 8 * time.Hour}
	r2 := Run{ID: "b-20261110-090000", Repo: "o/b", Mode: "unattended", Prompt: "p", StartedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	for _, r := range []Run{r1, r2} {
		if err := p.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(p.RunsVol, r1.ID, "run.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("run.json mode %v %v", fi, err)
	}
	got, err := p.Load(r1.ID)
	if err != nil || got.Repo != "o/a" || got.Max != 8*time.Hour || !got.StartedAt.Equal(r1.StartedAt) {
		t.Fatalf("load %+v %v", got, err)
	}
	if _, err := p.Load("../etc"); err == nil {
		t.Fatal("bad id accepted")
	}
	// a directory without run.json (start failed before Save) is skipped
	if err := os.MkdirAll(filepath.Join(p.RunsVol, "c-20261110-080000", "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := p.All()
	if err != nil || len(all) != 2 || all[0].ID != r2.ID || all[1].ID != r1.ID {
		t.Fatalf("all %+v %v", all, err)
	}
	if DefaultPaths() != (Paths{RunsTmp: "/run/jauntbox/runs", RunsVol: "/home/.jauntbox/runs", Agents: "/home/.jauntbox/agents"}) {
		t.Fatal("default paths")
	}
}
