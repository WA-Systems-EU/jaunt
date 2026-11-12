package release

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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeBin = "#!/bin/sh\necho fake\n"

// buildArchive returns a jauntctl tar.gz holding the binary, LICENSE and README.md.
func buildArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"jauntctl", fakeBin}, {"LICENSE", "license\n"}, {"README.md", "readme\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// writeFixtures writes the archive and checksums.txt into dir.
func writeFixtures(t *testing.T, dir, version, arch string, archive []byte, checksum string) string {
	t.Helper()
	name := ArchiveName("jauntctl", version, "linux", arch)
	if err := os.WriteFile(filepath.Join(dir, name), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if checksum == "" {
		checksum = sum(archive)
	}
	line := fmt.Sprintf("%s  %s\n", checksum, name)
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestArchiveName(t *testing.T) {
	if got := ArchiveName("jauntbox", "0.4.0", "darwin", "arm64"); got != "jauntbox_0.4.0_darwin_arm64.tar.gz" {
		t.Errorf("got %q", got)
	}
	if got := ArchiveName("jauntctl", "0.4.0", "linux", "amd64"); got != "jauntctl_0.4.0_linux_amd64.tar.gz" {
		t.Errorf("got %q", got)
	}
}

func TestBaseURL(t *testing.T) {
	if got := BaseURL("http://x", "0.4.0"); got != "http://x/v0.4.0" {
		t.Errorf("got %q", got)
	}
}

func TestValidVersion(t *testing.T) {
	for _, v := range []string{"0.4.0", "0.5.0-rc.1"} {
		if !ValidVersion(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"dev", "v0.4.0", "0.4.0; rm -rf /", "", "0.4", "0.4.0 "} {
		if ValidVersion(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

func fetchServer(t *testing.T, archive []byte, checksums string) *httptest.Server {
	name := ArchiveName("jauntctl", "0.4.0", "linux", "amd64")
	return serve(t, map[string][]byte{
		"/v0.4.0/" + name:       archive,
		"/v0.4.0/checksums.txt": []byte(checksums),
	})
}

func TestFetchBoxOK(t *testing.T) {
	a := buildArchive(t)
	name := ArchiveName("jauntctl", "0.4.0", "linux", "amd64")
	srv := fetchServer(t, a, sum(a)+"  "+name+"\n")
	dest := filepath.Join(t.TempDir(), "jauntctl")
	if err := FetchBox(context.Background(), srv.Client(), BaseURL(srv.URL, "0.4.0"), "0.4.0", "amd64", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != fakeBin {
		t.Fatalf("dest = %q, %v", got, err)
	}
	st, _ := os.Stat(dest)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestFetchBoxChecksumMismatch(t *testing.T) {
	a := buildArchive(t)
	name := ArchiveName("jauntctl", "0.4.0", "linux", "amd64")
	srv := fetchServer(t, a, strings.Repeat("0", 64)+"  "+name+"\n")
	dest := filepath.Join(t.TempDir(), "jauntctl")
	err := FetchBox(context.Background(), srv.Client(), BaseURL(srv.URL, "0.4.0"), "0.4.0", "amd64", dest)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Errorf("dest should not exist: %v", serr)
	}
}

func TestFetchBoxMissingLine(t *testing.T) {
	a := buildArchive(t)
	srv := fetchServer(t, a, sum(a)+"  some_other_file.tar.gz\n")
	dest := filepath.Join(t.TempDir(), "jauntctl")
	err := FetchBox(context.Background(), srv.Client(), BaseURL(srv.URL, "0.4.0"), "0.4.0", "amd64", dest)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Errorf("dest should not exist: %v", serr)
	}
}

func TestFetchBox404(t *testing.T) {
	srv := serve(t, map[string][]byte{})
	dest := filepath.Join(t.TempDir(), "jauntctl")
	base := BaseURL(srv.URL, "0.4.0")
	err := FetchBox(context.Background(), srv.Client(), base, "0.4.0", "amd64", dest)
	if err == nil {
		t.Fatal("expected error")
	}
	want := base + "/" + ArchiveName("jauntctl", "0.4.0", "linux", "amd64")
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err %q does not name %q", err, want)
	}
	if errors.Is(err, ErrChecksum) {
		t.Error("404 must not be a checksum error")
	}
}

func TestArchFromUname(t *testing.T) {
	for in, want := range map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"} {
		got, err := ArchFromUname(in)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", in, got, err)
		}
	}
	_, err := ArchFromUname("riscv64")
	if err == nil || !strings.Contains(err.Error(), "unsupported box architecture") {
		t.Errorf("err = %v", err)
	}
}

func TestBoxFetchScriptInvalidVersion(t *testing.T) {
	if _, err := BoxFetchScript("https://example.invalid", "dev"); err == nil {
		t.Error("expected error for dev")
	}
	if _, err := BoxFetchScript("https://example.invalid", "1.0.0; id"); err == nil {
		t.Error("expected error for injection")
	}
	if s, err := BoxFetchScript(GitHubBase+"/v0.4.0", "0.4.0"); err != nil || !strings.Contains(s, "/tmp/jauntctl") {
		t.Errorf("valid version: %v", err)
	}
}

func runScript(t *testing.T, script, unameOut string) (int, string) {
	t.Helper()
	bin := t.TempDir()
	shim := "#!/bin/sh\necho " + unameOut + "\n"
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	t.Fatal(err)
	return -1, ""
}

func TestBoxFetchScript(t *testing.T) {
	for _, tool := range []string{"sh", "curl", "sha256sum", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	archive := buildArchive(t)

	t.Run("ok", func(t *testing.T) {
		dir := t.TempDir()
		writeFixtures(t, dir, "0.4.0", "amd64", archive, "")
		out := filepath.Join(t.TempDir(), "jauntctl")
		code, msg := runScript(t, boxFetchScript("file://"+dir, "0.4.0", out), "x86_64")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, msg)
		}
		st, err := os.Stat(out)
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Fatalf("out: %v %v", st, err)
		}
		got, err := exec.Command(out).Output()
		if err != nil || strings.TrimSpace(string(got)) != "fake" {
			t.Errorf("run: %q %v", got, err)
		}
	})

	t.Run("corrupted", func(t *testing.T) {
		dir := t.TempDir()
		name := writeFixtures(t, dir, "0.4.0", "amd64", archive, "")
		if err := os.WriteFile(filepath.Join(dir, name), append(append([]byte{}, archive...), 'x'), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "jauntctl")
		code, msg := runScript(t, boxFetchScript("file://"+dir, "0.4.0", out), "x86_64")
		if code != ExitChecksum {
			t.Fatalf("exit %d: %s", code, msg)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Error("out should not exist")
		}
	})

	t.Run("missing archive", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(t.TempDir(), "jauntctl")
		code, msg := runScript(t, boxFetchScript("file://"+dir, "0.4.0", out), "x86_64")
		if code != ExitDownload {
			t.Fatalf("exit %d: %s", code, msg)
		}
	})

	t.Run("unsupported arch", func(t *testing.T) {
		dir := t.TempDir()
		writeFixtures(t, dir, "0.4.0", "amd64", archive, "")
		out := filepath.Join(t.TempDir(), "jauntctl")
		code, msg := runScript(t, boxFetchScript("file://"+dir, "0.4.0", out), "riscv64")
		if code != ExitArch {
			t.Fatalf("exit %d: %s", code, msg)
		}
	})
}
