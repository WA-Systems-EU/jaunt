package boxinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	in := BoxInfo{User: "alice", Hostname: "jauntbox-alice", Profile: "default", Timezone: "Europe/Paris",
		UpAt:   time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC),
		Limits: config.Limits{MaxUptime: config.Duration(10 * time.Hour), EveningCutoff: "19:00"}}
	if err := Write(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if !out.UpAt.Equal(in.UpAt) || out.Limits != in.Limits || out.User != in.User {
		t.Fatalf("got %+v want %+v", out, in)
	}
	if _, err := Read(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestBoxInfoCarriesAgent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	in := BoxInfo{User: "alice", Agent: config.Agent{MaxDuration: config.Duration(6 * time.Hour)}}
	if err := Write(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Agent.MaxDuration != config.Duration(6*time.Hour) {
		t.Fatalf("got %v", time.Duration(out.Agent.MaxDuration))
	}
}

func TestBoxInfoCarriesHostPorts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	in := BoxInfo{User: "alice", Agent: config.Agent{MaxDuration: config.Duration(time.Hour), HostPorts: []int{5432, 6379}}}
	if err := Write(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Agent.HostPorts) != 2 || out.Agent.HostPorts[0] != 5432 || out.Agent.HostPorts[1] != 6379 {
		t.Fatalf("got %v", out.Agent.HostPorts)
	}
	p2 := filepath.Join(t.TempDir(), "box.json")
	if err := Write(p2, BoxInfo{User: "alice"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p2)
	if strings.Contains(string(raw), "host_ports") {
		t.Fatalf("empty list leaked: %s", raw)
	}
}
