// Package release knows how jaunt release assets are named and fetched: the
// box side script that downloads jauntctl, and the laptop side download used
// as a fallback when the box cannot reach the release host.
package release

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// GitHubBase is where release assets live; BaseURL adds the version tag.
const GitHubBase = "https://github.com/WA-Systems-EU/jaunt/releases/download"

// Exit codes of the box fetch script.
const (
	ExitDownload = 2
	ExitChecksum = 3
	ExitArch     = 4
	ExitArchive  = 5 // the verified archive holds no jauntctl
)

// ErrChecksum means the archive's checksum was missing or did not match.
var ErrChecksum = errors.New("checksum mismatch")

// maxDownload bounds what FetchBox reads into memory.
const maxDownload = 256 << 20

var versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// BaseURL returns the download directory for a version (no leading "v" in version).
func BaseURL(root, version string) string { return root + "/v" + version }

// ArchiveName is the release archive for a binary ("jauntbox" or "jauntctl").
func ArchiveName(name, version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", name, version, goos, goarch)
}

// ValidVersion reports whether v is a semver release or pre-release without a
// leading "v". It guards shell interpolation.
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

// ArchFromUname maps `uname -m` output to a Go architecture.
func ArchFromUname(m string) (string, error) {
	switch strings.TrimSpace(m) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported box architecture: %s", strings.TrimSpace(m))
}

// shQuote single-quotes s for sh.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// BoxFetchScript returns a sh script, run as root on the box, that downloads
// the jauntctl archive for the box's architecture, verifies its checksum and
// leaves the binary at /tmp/jauntctl. Exit codes: ExitDownload, ExitChecksum,
// ExitArch, ExitArchive. Run it with the script on stdin (`sh -s`): ssh joins
// and re-parses remote arguments, so `sh -c script` would lose set -eu.
func BoxFetchScript(base, version string) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("invalid release version %q", version)
	}
	return boxFetchScript(base, version, "/tmp/jauntctl"), nil
}

func boxFetchScript(base, version, out string) string {
	curl := "curl -fsSL --retry 3"
	if strings.HasPrefix(base, "https://") {
		curl += " --proto '=https' --proto-redir '=https'" // never follow a redirect to plain http
	}
	return `set -eu
case "$(uname -m)" in x86_64|amd64) a=amd64 ;; aarch64|arm64) a=arm64 ;; *) echo "unsupported box architecture: $(uname -m)" >&2; exit 4 ;; esac
d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
f="jauntctl_` + version + `_linux_$a.tar.gz"
b=` + shQuote(base) + `
` + curl + ` -o "$d/$f" "$b/$f" || exit 2
` + curl + ` -o "$d/checksums.txt" "$b/checksums.txt" || exit 2
(cd "$d" && grep " $f\$" checksums.txt | sha256sum -c --status -) || { echo "checksum mismatch for $f" >&2; exit 3; }
tar -xzf "$d/$f" -C "$d" jauntctl || { echo "no jauntctl in $f" >&2; exit 5; }
install -m 0755 "$d/jauntctl" ` + shQuote(out) + `
`
}

func get(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	return b, nil
}

// FetchBox downloads the jauntctl archive for goarch from base, verifies it
// against checksums.txt and writes the jauntctl binary to dest with mode 0755.
// A missing checksum line or a wrong sum wraps ErrChecksum; dest is only
// created after verification.
func FetchBox(ctx context.Context, hc *http.Client, base, version, goarch, dest string) error {
	if !ValidVersion(version) {
		return fmt.Errorf("invalid release version %q", version)
	}
	name := ArchiveName("jauntctl", version, "linux", goarch)
	archive, err := get(ctx, hc, base+"/"+name)
	if err != nil {
		return err
	}
	sums, err := get(ctx, hc, base+"/checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("%w: no checksum for %s", ErrChecksum, name)
	}
	h := sha256.Sum256(archive)
	if got := hex.EncodeToString(h[:]); got != want {
		return fmt.Errorf("%w for %s: got %s, want %s", ErrChecksum, name, got, want)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s has no jauntctl entry", name)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != "jauntctl" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxDownload))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(dest, body, 0o755); err != nil {
			return err
		}
		return os.Chmod(dest, 0o755)
	}
}
