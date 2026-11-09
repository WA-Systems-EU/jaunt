package boxinfo

import (
	"path/filepath"
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
