package version

import (
	"runtime/debug"
	"testing"
)

func stub(t *testing.T, v, c, d string, bi *debug.BuildInfo) {
	t.Helper()
	ov, oc, od, orb := version, commit, date, readBuildInfo
	version, commit, date = v, c, d
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, bi != nil }
	t.Cleanup(func() { version, commit, date, readBuildInfo = ov, oc, od, orb })
}

func TestLdflagsWin(t *testing.T) {
	stub(t, "0.4.0", "abc1234def", "2026-11-13T10:00:00Z", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	want := Build{"0.4.0", "abc1234", "2026-11-13", false}
	if got := Current(); got != want {
		t.Fatalf("Current = %+v, want %+v", got, want)
	}
	if got := Current().Line("jauntbox"); got != "jauntbox 0.4.0 (commit abc1234, built 2026-11-13)" {
		t.Fatalf("Line = %q", got)
	}
	if !Current().Release() {
		t.Fatal("not a release")
	}
}

func TestGoInstallTag(t *testing.T) {
	stub(t, "", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}})
	b := Current()
	if b.Version != "0.4.0" || !b.Release() {
		t.Fatalf("got %+v", b)
	}
}

func TestPseudoVersionIsDev(t *testing.T) {
	stub(t, "", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.1-0.20261113101500-abcdef123456"}})
	b := Current()
	if b.Version != "dev" || b.Release() {
		t.Fatalf("got %+v", b)
	}
}

func TestLocalBuild(t *testing.T) {
	stub(t, "", "", "", &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234def5678"},
			{Key: "vcs.time", Value: "2026-11-10T09:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	if got := Current().Line("jauntbox"); got != "jauntbox dev (commit abc1234+dirty)" {
		t.Fatalf("Line = %q", got)
	}
}

func TestPrerelease(t *testing.T) {
	stub(t, "0.5.0-rc.1", "", "", nil)
	if !Current().Release() {
		t.Fatal("rc is a release")
	}
}

func TestParseLine(t *testing.T) {
	for in, want := range map[string]string{
		"jauntctl 0.4.0 (commit a, built b)\n": "0.4.0",
		"jauntctl dev (commit a)":              "dev",
		"jauntctl 0.4.0\n":                     "0.4.0",
		"jauntctl dev":                         "dev",
		"Error: unknown flag: --version":       "",
	} {
		if got := ParseLine(in); got != want {
			t.Errorf("ParseLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLineRoundTrip(t *testing.T) {
	for _, b := range []Build{
		{"0.4.0", "abc1234", "2026-11-13", false},
		{"dev", "abc1234", "", true},
		{"0.4.0", "", "", false},
	} {
		if got := ParseLine(b.Line("x")); got != b.Version {
			t.Errorf("ParseLine(%q) = %q, want %q", b.Line("x"), got, b.Version)
		}
	}
}
