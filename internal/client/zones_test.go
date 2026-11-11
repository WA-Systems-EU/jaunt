package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestZoneNamesFiltersInvalid(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"Europe/Paris", "America/New_York", "posixrules", "README", "zone.tab", "posix/Europe/Paris", "UTC"} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755))
		must(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644))
	}
	valid := func(n string) bool {
		return n == "Europe/Paris" || n == "America/New_York" || n == "UTC" || n == "posixrules"
	}
	got := zoneNamesIn(dir, valid)
	want := []string{"America/New_York", "Europe/Paris", "UTC"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if z := zoneNamesIn(filepath.Join(dir, "missing"), valid); len(z) != 0 {
		t.Fatalf("missing dir gave %v", z)
	}
}

func TestZoneNamesFollowsSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	must(t, os.MkdirAll(filepath.Join(real, "Europe"), 0o755))
	must(t, os.WriteFile(filepath.Join(real, "Europe", "Paris"), []byte("x"), 0o644))
	mid := filepath.Join(base, "mid")
	top := filepath.Join(base, "top")
	must(t, os.Symlink(real, mid))
	must(t, os.Symlink(mid, top))
	t.Setenv("ZONEINFO", top)
	valid := func(string) bool { return true }
	if z := zoneNamesIn(top, valid); len(z) != 0 {
		t.Fatalf("expected WalkDir alone to miss a symlinked root, got %v", z)
	}
	got := zoneNamesFromDirs([]string{filepath.Join(base, "nope"), top}, valid)
	if len(got) != 1 || got[0] != "Europe/Paris" {
		t.Fatalf("got %v", got)
	}
}
