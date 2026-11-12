// Package version reports which build a binary is.
package version

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
)

// Set by -ldflags "-X .../internal/version.version=..." and friends.
var (
	version string
	commit  string
	date    string
)

// readBuildInfo is stubbed in tests.
var readBuildInfo = debug.ReadBuildInfo

// Build describes one binary.
type Build struct {
	Version string // without "v"; "dev" for a non-release
	Commit  string // 7 characters, may be empty
	Date    string // YYYY-MM-DD, may be empty
	Dirty   bool
}

var (
	pseudoRe  = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}$`)
	releaseRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	lineRe    = regexp.MustCompile(`^\S+ (\S+) \(`)
)

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Current returns the build of the running binary: ldflags first, then the
// module version (go install), then VCS info (local build).
func Current() Build {
	b := Build{Version: "dev", Commit: short(commit, 7), Date: short(date, 10)}
	var bi *debug.BuildInfo
	if readBuildInfo != nil {
		if info, ok := readBuildInfo(); ok {
			bi = info
		}
	}
	switch {
	case version != "":
		b.Version = strings.TrimPrefix(version, "v")
	case bi != nil:
		mv := bi.Main.Version
		if mv != "" && mv != "(devel)" && !pseudoRe.MatchString(mv) && !strings.HasSuffix(mv, "+dirty") {
			b.Version = strings.TrimPrefix(mv, "v")
		}
	}
	if bi != nil {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = short(s.Value, 7)
				}
			case "vcs.time":
				if b.Date == "" {
					b.Date = short(s.Value, 10)
				}
			case "vcs.modified":
				b.Dirty = s.Value == "true"
			}
		}
	}
	return b
}

// Release reports whether Version is a semver release or pre-release.
func (b Build) Release() bool { return releaseRe.MatchString(b.Version) }

// Line is the --version line.
func (b Build) Line(name string) string {
	var parts []string
	if b.Commit != "" {
		c := "commit " + b.Commit
		if b.Dirty {
			c += "+dirty"
		}
		parts = append(parts, c)
	}
	if b.Date != "" {
		parts = append(parts, "built "+b.Date)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s %s", name, b.Version)
	}
	return fmt.Sprintf("%s %s (%s)", name, b.Version, strings.Join(parts, ", "))
}

// ParseLine returns the version word of a --version line, or "".
func ParseLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if m := lineRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}
