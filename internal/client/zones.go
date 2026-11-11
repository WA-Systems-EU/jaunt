package client

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// zoneDirs are where the system keeps its IANA zone files, in order.
func zoneDirs() []string {
	dirs := []string{"/usr/share/zoneinfo", "/var/db/timezone/zoneinfo"}
	if z := os.Getenv("ZONEINFO"); z != "" {
		dirs = append(dirs, z)
	}
	return dirs
}

// ZoneNames lists the IANA zone names found in the system zoneinfo directory
// that time.LoadLocation accepts, sorted. It is empty when no directory is
// found; callers then fall back to free text.
func ZoneNames() []string {
	valid := func(name string) bool { _, err := time.LoadLocation(name); return err == nil }
	return zoneNamesFromDirs(zoneDirs(), valid)
}

// zoneNamesFromDirs returns the names from the first directory that has any.
func zoneNamesFromDirs(dirs []string, valid func(string) bool) []string {
	for _, d := range dirs {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r // WalkDir does not follow a symlinked root
		}
		if z := zoneNamesIn(d, valid); len(z) > 0 {
			return z
		}
	}
	return nil
}

// zoneNamesIn walks dir and returns the relative file names that look like
// zone names (start with a capital, no dots) and for which valid is true.
// The posix/ and right/ mirrors and files such as posixrules, zone.tab and
// README never qualify.
func zoneNamesIn(dir string, valid func(name string) bool) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			if name == "posix" || name == "right" {
				return filepath.SkipDir
			}
			return nil
		}
		if c := name[0]; c < 'A' || c > 'Z' || strings.Contains(name, ".") {
			return nil
		}
		if valid(name) {
			out = append(out, name)
		}
		return nil
	})
	sort.Strings(out)
	return out
}
