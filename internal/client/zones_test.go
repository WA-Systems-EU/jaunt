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
