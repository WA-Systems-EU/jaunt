package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/release"
	"github.com/WA-Systems-EU/jaunt/internal/ui"
	"github.com/WA-Systems-EU/jaunt/internal/version"
)

const fixtureBin = "#!/bin/sh\necho fixture\n"

func pretendLinuxAmd64(t *testing.T) {
	t.Helper()
	og, oa := runtimeGOOS, runtimeGOARCH
	runtimeGOOS, runtimeGOARCH = "linux", "amd64"
	t.Cleanup(func() { runtimeGOOS, runtimeGOARCH = og, oa })
}

// stageRemote is a root remote that scripts the fetch script's and uname's answers.
type stageRemote struct {
	fetchExit int // exit status of the sh -c script; 0 = success
	uname     string
	cmds      [][]string
	copies    []string // content of each copied file
	copyDsts  []string
}

func (r *stageRemote) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.cmds = append(r.cmds, args)
	if len(args) > 0 && args[0] == "sh" && r.fetchExit != 0 {
		return nil, mkExit(r.fetchExit)
	}
	if len(args) == 2 && args[0] == "uname" {
		return []byte(r.uname + "\n"), nil
	}
	return nil, nil
}
func (r *stageRemote) RunInput(context.Context, string, []byte, ...string) ([]byte, error) {
	return nil, nil
}
func (r *stageRemote) Copy(_ context.Context, _, local, remote string) error {
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	r.copies = append(r.copies, string(b))
	r.copyDsts = append(r.copyDsts, remote)
	return nil
}

// releaseServer serves a valid jauntctl release for version at /v<version>/.
func releaseServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "jauntctl", Mode: 0o755, Size: int64(len(fixtureBin))})
	tw.Write([]byte(fixtureBin))
	tw.Close()
	gz.Close()
	name := release.ArchiveName("jauntctl", version, "linux", "amd64")
	h := sha256.Sum256(buf.Bytes())
	sums := hex.EncodeToString(h[:]) + "  " + name + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/v"+version+"/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(buf.Bytes()) })
	mux.HandleFunc("/v"+version+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(sums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func relDeps(t *testing.T, r *stageRemote, root string) Deps {
	d, _, _, _, _, _ := newDeps(t)
	d.Root = r
	d.Build = version.Build{Version: "0.4.0"}
	d.ReleaseRoot = root
	return d
}

func TestSourceBoxBinaryWins(t *testing.T) {
	d := Deps{Cfg: config.Config{BoxBinary: "/x"}, Build: version.Build{Version: "0.4.0"}}
	s, err := boxBinarySource(d)
	if err != nil || s.Local != "/x" || s.Version != "" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}

func TestSourceRelease(t *testing.T) {
	s, err := boxBinarySource(Deps{Build: version.Build{Version: "0.4.0"}})
	if err != nil || s.Version != "0.4.0" || s.Local != "" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}

func TestSourceDevNonLinux(t *testing.T) {
	og, oa := runtimeGOOS, runtimeGOARCH
	runtimeGOOS, runtimeGOARCH = "darwin", "arm64"
	t.Cleanup(func() { runtimeGOOS, runtimeGOARCH = og, oa })
	_, err := boxBinarySource(Deps{Build: version.Build{Version: "dev"}})
	if err == nil || !strings.Contains(err.Error(), "no jauntctl to install on the box") {
		t.Fatalf("err = %v", err)
	}
}

func TestStageReleaseRunsFetchOnBox(t *testing.T) {
	r := &stageRemote{}
	d := relDeps(t, r, "http://example.invalid/rel")
	if err := stageBinary(context.Background(), d, boxSource{Version: "0.4.0"}); err != nil {
		t.Fatal(err)
	}
	if len(r.copies) != 0 || len(r.cmds) != 1 {
		t.Fatalf("copies=%d cmds=%v", len(r.copies), r.cmds)
	}
	c := r.cmds[0]
	if len(c) != 3 || c[0] != "sh" || c[1] != "-c" ||
		!strings.Contains(c[2], "jauntctl_0.4.0_linux_") || !strings.Contains(c[2], "http://example.invalid/rel/v0.4.0") {
		t.Fatalf("cmd = %v", c)
	}
}

func TestStageFallsBackToLaptop(t *testing.T) {
	srv := releaseServer(t, "0.4.0")
	r := &stageRemote{fetchExit: release.ExitDownload, uname: "x86_64"}
	d := relDeps(t, r, srv.URL)
	if err := stageBinary(context.Background(), d, boxSource{Version: "0.4.0"}); err != nil {
		t.Fatal(err)
	}
	if len(r.copies) != 1 || r.copies[0] != fixtureBin || r.copyDsts[0] != "/tmp/jauntctl" {
		t.Fatalf("copies=%q dsts=%v", r.copies, r.copyDsts)
	}
}

func TestStageChecksumOnBoxIsFatal(t *testing.T) {
	r := &stageRemote{fetchExit: release.ExitChecksum}
	d := relDeps(t, r, "http://example.invalid")
	err := stageBinary(context.Background(), d, boxSource{Version: "0.4.0"})
	if !errors.Is(err, release.ErrChecksum) || len(r.copies) != 0 {
		t.Fatalf("err=%v copies=%d", err, len(r.copies))
	}
}

func TestStageArchIsFatal(t *testing.T) {
	r := &stageRemote{fetchExit: release.ExitArch}
	d := relDeps(t, r, "http://example.invalid")
	var fe *fatalStageError
	if err := stageBinary(context.Background(), d, boxSource{Version: "0.4.0"}); !errors.As(err, &fe) {
		t.Fatalf("err=%v", err)
	}
}

func TestPushBinaryReleaseMissing(t *testing.T) {
	fastPoll(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	r := &stageRemote{fetchExit: release.ExitDownload, uname: "x86_64"}
	d := relDeps(t, r, srv.URL)
	rec := withRec(&d)
	start := time.Now()
	err := PushBinary(context.Background(), d, time.Minute)
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %v: retried a missing release", time.Since(start))
	}
	if !strings.Contains(err.Error(), "0.4.0") || !strings.Contains(err.Error(), release.BaseURL(srv.URL, "0.4.0")) {
		t.Fatalf("err = %v", err)
	}
	if want := "fail Box reachable: " + err.Error(); !contains(rec.Events, want) {
		t.Fatalf("no %q in %v", want, rec.Events)
	}
	if n := len(r.cmds); n != 2 { // one sh -c, one uname
		t.Fatalf("remote commands = %d (%v), want no retry", n, fmt.Sprint(r.cmds))
	}
}

func TestVersionPlan(t *testing.T) {
	rel := version.Build{Version: "0.4.0"}
	dev := version.Build{Version: "dev"}
	cases := []struct {
		name         string
		laptop       version.Build
		box          string
		reinstall    bool
		detail, warn string
	}{
		{"unknown", rel, "", true, "unknown → 0.4.0", ""},
		{"older", rel, "0.3.9", true, "0.3.9 → 0.4.0", ""},
		{"box dev", rel, "dev", false, "", "the box runs a development build; this laptop is 0.4.0"},
		{"laptop dev", dev, "0.4.0", false, "", "the box runs 0.4.0; this laptop is a development build"},
		{"laptop dev, box unknown", dev, "", false, "", "the box's version is unknown; this laptop is a development build"},
		{"equal", rel, "0.4.0", false, "", ""},
		{"both dev", dev, "dev", false, "", ""},
	}
	for _, c := range cases {
		re, detail, warn := versionPlan(c.laptop, c.box)
		if re != c.reinstall || detail != c.detail || warn != c.warn {
			t.Errorf("%s: got (%v, %q, %q)", c.name, re, detail, warn)
		}
	}
}

func existingBox(t *testing.T, laptop, checkOut string) (Deps, *scriptRemote, *ui.Recorder) {
	d, _, r := attachFixture(t)
	r.checkOut = checkOut
	d.Build = version.Build{Version: laptop}
	d.ReleaseRoot = "http://example.invalid/rel"
	return d, r, withRec(&d)
}

func TestExistingBoxOldVersionReinstalls(t *testing.T) {
	d, r, rec := existingBox(t, "0.4.1", "jauntctl 0.4.0 (commit abc1234, built 2026-11-13)\n")
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) < 3 || rec.Events[0] != "start Install supervisor" || rec.Events[1] != "detail Install supervisor: 0.4.0 → 0.4.1" {
		t.Fatalf("events = %v", rec.Events)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "jauntctl_0.4.1_linux_") {
		t.Errorf("no fetch: %v", r.cmds)
	}
}

func TestExistingBoxNoVersionReinstalls(t *testing.T) {
	d, _, rec := existingBox(t, "0.4.1", "")
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) < 2 || rec.Events[1] != "detail Install supervisor: unknown → 0.4.1" {
		t.Fatalf("events = %v", rec.Events)
	}
}

func TestExistingBoxSameVersionSkips(t *testing.T) {
	d, r, rec := existingBox(t, "0.4.1", "jauntctl 0.4.1 (commit abc1234, built 2026-11-13)\n")
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !contains(rec.Events, "skip Install supervisor: already running") || strings.Contains(strings.Join(r.cmds, "\n"), "jauntctl_0.4.1") {
		t.Fatalf("events=%v cmds=%v", rec.Events, r.cmds)
	}
}

func TestExistingBoxDevWarns(t *testing.T) {
	d, _, rec := existingBox(t, "0.4.1", "jauntctl dev (commit abc1234)\n")
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !contains(rec.Events, "warn Install supervisor: the box runs a development build; this laptop is 0.4.1") ||
		!contains(rec.Events, "skip Install supervisor: already running") {
		t.Fatalf("events = %v", rec.Events)
	}
}

func TestInstallSupervisorNoWaitSpawnsForReinstall(t *testing.T) {
	d, _, _ := existingBox(t, "0.4.1", "jauntctl 0.4.0\n")
	spawned := 0
	d.Spawn = func(...string) error { spawned++; return nil }
	if err := InstallSupervisor(context.Background(), d, false); err != nil || spawned != 1 {
		t.Fatalf("err=%v spawned=%d", err, spawned)
	}
}

func TestExistingBoxWarningReachesPlainOutput(t *testing.T) {
	d, _, _ := existingBox(t, "0.4.1", "jauntctl dev (commit abc1234)\n")
	var buf strings.Builder
	d.Steps = ui.NewPlain(&buf, time.Now)
	if err := EnsureSupervisor(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	want := "– Install supervisor already running\n  the box runs a development build; this laptop is 0.4.1\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("out = %q", buf.String())
	}
}
