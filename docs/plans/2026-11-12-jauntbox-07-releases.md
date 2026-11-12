# jauntbox releases (v0.4.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship jauntbox as versioned GitHub Releases for six targets, installable with `install.sh`, with the box always running the laptop's release version.

**Architecture:** A small `internal/version` package names every build (ldflags for releases, `debug.ReadBuildInfo` otherwise). A new `internal/release` package knows release asset names, builds the box-side fetch script and does the laptop fallback download. `internal/client` picks the box binary source (release fetch on the box, laptop copy for dev builds or `box_binary`) and reinstalls a box whose version differs. GoReleaser and a tag workflow publish; `site/install.sh` installs.

**Tech Stack:** Go (cobra), GoReleaser v2 (`goreleaser/goreleaser-action@v6`, `version: "~> v2"`), GitHub Actions (`actions/attest-build-provenance@v2`), POSIX sh.

**Spec:** `docs/specs/2026-11-12-jauntbox-releases-design.md`

## Global Constraints

- Targets: `linux`, `darwin`, `freebsd` × `amd64`, `arm64`; `CGO_ENABLED=0`, `-trimpath`. No Windows.
- Archive name: `jauntbox_<version>_<os>_<arch>.tar.gz` (version without `v`), containing `jauntbox`, `LICENSE`, `README.md`; one `checksums.txt` (sha256, `<hex>  <file>` lines).
- Download base: `https://github.com/WA-Systems-EU/jaunt/releases/download/v<version>`.
- First release `v0.4.0`. The existing tag `v0.1.0` is never moved or deleted.
- `--version` line: `jauntbox 0.4.0 (commit abc1234, built 2026-11-13)`; dev: `jauntbox dev (commit abc1234+dirty)` (`+dirty` only when modified). `jauntctl --version` prints the same with `jauntctl` as the first word.
- `install.sh`: POSIX sh, shellcheck clean, default dir `~/.local/bin`, never root by default, never installs without a checksum match, never picks a pre-release unless given `--version`.
- Box: a checksum mismatch fails hard (no fallback); `box_binary` always wins when set.
- Tests never call real GitHub, Hetzner or Tailscale: `httptest`, `file://` URLs, fakes.
- Commits: plain conventional messages, no `Co-Authored-By`/`Claude-Session` or any attribution lines. Stage files by explicit path. Never touch `tofu/`, tfvars or `lenovo.txt`. Base branch `trunk`.
- Pushing tags, publishing releases, merging and pushing are Thomas's actions.

## Review Focus

1. A release laptop whose release assets are not published yet (tag pushed, workflow running or failed): box fetch and laptop fallback both 404; expect one clear error naming the version and the release URL, and the "Install supervisor" step failed, not hung. Test in Task 3.
2. Box `uname -m` of `x86_64`, `aarch64` or something else: the first two map to `amd64`/`arm64`, anything else fails with "unsupported box architecture". Test in Task 2.
3. `install.sh` on a machine with only `shasum` (macOS) or only `sha256` (FreeBSD), no `sha256sum`: still verifies. Test in Task 5.
4. A binary from `go install …@v0.4.0` (BuildInfo `v0.4.0`) counts as release `0.4.0`; one from an untagged commit (pseudo-version `v0.4.1-0.20261113…-abcdef123456`) counts as `dev`. Test in Task 1.
5. A tag with no `CHANGELOG.md` section: the release workflow stops before publishing. Test in Task 4.

---

### Task 1: Version in every binary, six targets build

**Files:**
- Create: `internal/version/version.go`, `internal/version/version_test.go`
- Modify: `internal/agentrun/guard.go:58` (the `isType` line), `cmd/jauntbox/main.go` (both roots), `cmd/jauntbox/main.go` box-status output map (~line 310), `.github/workflows/ci.yml`
- Test: `cmd/jauntbox/version_test.go`

**Interfaces:**
- Produces:
  - `package version`; ldflags targets `version.version`, `version.commit`, `version.date` (unexported `string` vars, empty by default).
  - `type Build struct { Version, Commit, Date string; Dirty bool }` — `Version` without `v`, `"dev"` for non-release.
  - `func Current() Build`
  - `func (b Build) Release() bool` — true when `Version` is a semver release or pre-release (`0.4.0`, `0.5.0-rc.1`), false for `dev`.
  - `func (b Build) Line(name string) string` — the `--version` line.
  - `func ParseLine(s string) string` — the version word from a `--version` line (`"jauntctl 0.4.0 (…)"` → `"0.4.0"`), `""` when it isn't one.
  - box-status JSON gains `"version": <Build.Version>`.

- [ ] **Step 1: Write the failing tests** in `internal/version/version_test.go`, with `readBuildInfo` (a package `var` of type `func() (*debug.BuildInfo, bool)`) stubbed:
  - `TestLdflagsWin`: `version="0.4.0"`, `commit="abc1234def"`, `date="2026-11-13T10:00:00Z"` → `Current()` = `{0.4.0, abc1234, 2026-11-13, false}`; `Line("jauntbox")` == `"jauntbox 0.4.0 (commit abc1234, built 2026-11-13)"`; `Release()` true.
  - `TestGoInstallTag`: BuildInfo `Main.Version="v0.4.0"` → `Version=="0.4.0"`, `Release()` true.
  - `TestPseudoVersionIsDev`: `Main.Version="v0.4.1-0.20261113101500-abcdef123456"` → `Version=="dev"`, `Release()` false.
  - `TestLocalBuild`: `Main.Version="(devel)"`, settings `vcs.revision=abc1234def…`, `vcs.modified=true` → `Line("jauntbox")=="jauntbox dev (commit abc1234+dirty)"`.
  - `TestPrerelease`: `version="0.5.0-rc.1"` → `Release()` true.
  - `TestParseLine`: `"jauntctl 0.4.0 (commit a, built b)\n"`→`"0.4.0"`; `"jauntctl dev (commit a)"`→`"dev"`; `"Error: unknown flag: --version"`→`""`.
- [ ] **Step 2: Run** `go test ./internal/version/` — FAIL (package missing).
- [ ] **Step 3: Implement `internal/version/version.go`.** Commit shortened to 7 chars, date to `YYYY-MM-DD`. Pseudo-version test: `module.IsPseudoVersion` from `golang.org/x/mod/module` if already in `go.sum`, else the regex `-\d{14}-[0-9a-f]{12}$`.
- [ ] **Step 4: Run** `go test ./internal/version/` — PASS.
- [ ] **Step 5: Wire `--version`.** Set `root.Version = version.Current().Version` and `root.SetVersionTemplate(version.Current().Line("jauntbox") + "\n")` on the client root and the same with `"jauntctl"` on the box root. Add `"version": version.Current().Version` to the box-status output map. Test in `cmd/jauntbox/version_test.go`: `TestVersionFlag` runs `newClientCmd()` and `newBoxCmd()` with `--version`, asserts stdout starts with `jauntbox ` / `jauntctl ` and no error; `--version` on `jauntctl` must not trip `boxPreRun`.
- [ ] **Step 6: Fix the cross-build.** `internal/agentrun/guard.go:58` becomes `uint32(st.Mode)&unix.S_IFMT == t`. Verify: `for os in linux darwin freebsd; do for a in amd64 arm64; do GOOS=$os GOARCH=$a CGO_ENABLED=0 go build -o /dev/null ./cmd/jauntbox || exit 1; done; done` — all six succeed.
- [ ] **Step 7: CI cross-build job.** In `.github/workflows/ci.yml` add job `cross` (ubuntu-latest, setup-go from `go.mod`) running the loop from Step 6.
- [ ] **Step 8: Run** `go vet ./... && go test ./...` — PASS.
- [ ] **Step 9: Commit** `feat(jauntbox): --version on jauntbox and jauntctl; build for linux, macOS and FreeBSD`.

### Task 2: Release assets: names, box fetch script, laptop fetch

**Files:**
- Create: `internal/release/release.go`, `internal/release/release_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (versions are plain strings without `v`).
- Produces:
  - `const GitHubBase = "https://github.com/WA-Systems-EU/jaunt/releases/download"`
  - `func BaseURL(root, version string) string` → `root + "/v" + version`
  - `func ArchiveName(version, goos, goarch string) string`
  - `func ValidVersion(v string) bool` — semver release or pre-release only (guards shell interpolation).
  - `func BoxFetchScript(base, version string) (string, error)` — a `sh` script run as root on the box that leaves the binary at `/tmp/jauntbox` (mode 0755). Exit codes: `2` download failed, `3` checksum missing or wrong, `4` unsupported architecture. Error for an invalid version.
  - `const (ExitDownload = 2; ExitChecksum = 3; ExitArch = 4)`
  - `var ErrChecksum = errors.New("checksum mismatch")`
  - `func Fetch(ctx context.Context, hc *http.Client, base, version, goos, goarch, dest string) error` — downloads the archive and `checksums.txt`, verifies (wrapping `ErrChecksum` on a missing line or wrong sum), extracts the `jauntbox` entry to `dest` with mode 0755. A non-200 is an error naming the URL.
  - `func ArchFromUname(m string) (string, error)` — `x86_64|amd64`→`amd64`, `aarch64|arm64`→`arm64`.

- [ ] **Step 1: Write failing tests** with a fixture helper that builds a `.tar.gz` holding `jauntbox` (content `"#!/bin/sh\necho fake\n"`), `LICENSE`, `README.md`, and a matching `checksums.txt`:
  - `TestArchiveName`: `("0.4.0","darwin","arm64")` → `"jauntbox_0.4.0_darwin_arm64.tar.gz"`.
  - `TestValidVersion`: true for `0.4.0`, `0.5.0-rc.1`; false for `dev`, `v0.4.0`, `0.4.0; rm -rf /`, `""`.
  - `TestFetchOK` (httptest serving `/v0.4.0/<archive>` and `/v0.4.0/checksums.txt`): dest holds the fake binary, mode 0755.
  - `TestFetchChecksumMismatch` and `TestFetchMissingLine`: `errors.Is(err, ErrChecksum)`; dest not created.
  - `TestFetch404`: error contains the archive URL.
  - `TestArchFromUname`: the four mappings; `"riscv64"` → error containing `unsupported box architecture`.
  - `TestBoxFetchScript` (skip unless `sh`, `curl`, `sha256sum`, `tar` exist): fixtures in a temp dir served as `file://<dir>`; run the script with `uname` shimmed via a `PATH` dir holding a fake `uname` printing `x86_64`, and `/tmp/jauntbox` redirected by making the script's output path a parameter of a test-only helper `boxFetchScript(base, version, out string)`. Cases: OK → out file runs and prints `fake`; corrupted archive → exit 3; missing archive → exit 2; fake `uname` printing `riscv64` → exit 4. `BoxFetchScript("…","dev")` → error.
- [ ] **Step 2: Run** `go test ./internal/release/` — FAIL.
- [ ] **Step 3: Implement `internal/release/release.go`.** The script, with `<base>`, `<version>` and `<out>` substituted (all validated or constant):

```sh
set -eu
case "$(uname -m)" in x86_64|amd64) a=amd64 ;; aarch64|arm64) a=arm64 ;; *) echo "unsupported box architecture: $(uname -m)" >&2; exit 4 ;; esac
d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
f="jauntbox_<version>_linux_$a.tar.gz"
curl -fsSL --retry 3 -o "$d/$f" "<base>/$f" || exit 2
curl -fsSL --retry 3 -o "$d/checksums.txt" "<base>/checksums.txt" || exit 2
(cd "$d" && grep " $f\$" checksums.txt | sha256sum -c --status -) || { echo "checksum mismatch for $f" >&2; exit 3; }
tar -xzf "$d/$f" -C "$d" jauntbox
install -m 0755 "$d/jauntbox" "<out>"
```

- [ ] **Step 4: Run** `go test ./internal/release/` — PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): release asset names, the box fetch script and the laptop download`.

### Task 3: The box gets the laptop's release

**Files:**
- Modify: `internal/client/up.go` (`BoxBinaryPath` → replaced), `internal/client/push.go` (`EnsureSupervisor`, `PushBinary`, `copyAndInstall`), `internal/client/deps.go` or wherever `Deps` lives (one new field)
- Test: `internal/client/release_test.go`

**Interfaces:**
- Consumes: `version.Build`, `version.Current()` (Task 1); everything in `internal/release` (Task 2).
- Produces:
  - `Deps.Build version.Build` (zero value → callers use `version.Current()`; set in tests) and `Deps.ReleaseRoot string` (empty → `release.GitHubBase`).
  - `type boxSource struct { Local string; Version string }` — exactly one set.
  - `func boxBinarySource(d Deps) (boxSource, error)`: `cfg.BoxBinary` set → `Local`; build `Release()` → `Version`; else linux/amd64 → `Local: osExecutable()`; else error `box_binary must be set for a development build on a non-linux/amd64 laptop`.
  - `func stageBinary(ctx context.Context, d Deps, src boxSource) error` — leaves the binary at `/tmp/jauntbox` on the box: `Local` → `rootCopy` (today); `Version` → `rootRun(sh -c <BoxFetchScript>)`; on exit `release.ExitDownload` → laptop fallback: `rootRun("uname", "-m")` → `release.ArchFromUname` → `release.Fetch` to a temp file → `rootCopy`; exit `ExitChecksum` or `ExitArch` → hard error (`ExitChecksum` wraps `release.ErrChecksum`). ssh failures keep their `ErrRootSSH`/`ErrSSHUnreachable` meaning so `copyAndInstall`'s retry loop is unchanged.
  - `copyAndInstall` takes a `boxSource` instead of `bin string` and calls `stageBinary` where it called `rootCopy`.

- [ ] **Step 1: Write failing tests** in `internal/client/release_test.go` using the existing `newDeps`/fake remote helpers:
  - `TestSourceBoxBinaryWins`: `cfg.BoxBinary="/x"`, release build → `Local=="/x"`.
  - `TestSourceRelease`: build `{Version:"0.4.0"}` → `Version=="0.4.0"`.
  - `TestSourceDevNonLinux` (skip on linux/amd64): dev build, no `box_binary` → the error above.
  - `TestStageReleaseRunsFetchOnBox`: release source; the fake root remote records a `sh -c` call whose script contains `jauntbox_0.4.0_linux_` and `<ReleaseRoot>/v0.4.0`; no `Copy`.
  - `TestStageFallsBackToLaptop`: fake root returns exit 2 for the script and `x86_64` for `uname -m`; `ReleaseRoot` points at an httptest server with valid fixtures → one `Copy` of a file whose content is the fixture binary.
  - `TestStageChecksumOnBoxIsFatal`: exit 3 → `errors.Is(err, release.ErrChecksum)`, no `Copy`.
  - `TestPushBinaryReleaseMissing` (Review Focus 1): exit 2 on the box, httptest 404 for everything → `PushBinary` returns an error containing `0.4.0` and the release URL; the "Install supervisor" step (or "Box reachable") is recorded as failed.
- [ ] **Step 2: Run** `go test ./internal/client/ -run 'Source|Stage|ReleaseMissing'` — FAIL.
- [ ] **Step 3: Implement** the interfaces above; delete `BoxBinaryPath` and update its callers (`EnsureSupervisor`, `PushBinary`).
- [ ] **Step 4: Run** `go test ./internal/client/` — PASS (existing push tests keep passing with a dev `Deps.Build` on linux/amd64).
- [ ] **Step 5: Commit** `feat(jauntbox): the box downloads the laptop's release, with the laptop as fallback`.

### Task 4: Version checks on an existing box, and in status

**Files:**
- Modify: `internal/client/push.go` (`supervisorCheck`, `supervisorInstalled`, `EnsureSupervisor`, `InstallSupervisor`), `internal/client/status.go` (`StatusReport`), `cmd/jauntbox/ops.go` (`printStatus`)
- Test: `internal/client/release_test.go`, `cmd/jauntbox/status_test.go` (or the existing status test file)

**Interfaces:**
- Consumes: Tasks 1 and 3.
- Produces:
  - `supervisorCheck` becomes `test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer && { /usr/local/bin/jauntctl --version 2>/dev/null || true; }`; its stdout goes through `version.ParseLine` (`""` = unknown).
  - `func versionPlan(laptop version.Build, box string) (reinstall bool, detail string, warn string)`:
    - laptop release, box `""` → reinstall, detail `"unknown → 0.4.0"`;
    - laptop release, box release ≠ laptop → reinstall, detail `"0.4.0 → 0.4.1"`;
    - laptop release, box `dev` → warn `"the box runs a development build; this laptop is 0.4.0"`;
    - laptop dev, box ≠ `dev` → warn `"the box runs 0.4.0; this laptop is a development build"`;
    - equal → nothing.
  - `supervisorInstalled` skips "Install supervisor" ("already running") only when installed and `versionPlan` says no reinstall; a reinstall runs the step with `detail`; a warn is a `Warn` on the skipped step (shown, run still ends Ready).
  - `StatusReport.BoxVersion string` (from box-status JSON `version`, `""` when absent) and `StatusReport.LaptopVersion string`.
  - `printStatus` prints `version  0.4.1` when equal, `version  0.4.0 (this laptop 0.4.1)` in yellow when different, `version  unknown (this laptop 0.4.1)` when absent.

- [ ] **Step 1: Write failing tests:** `TestVersionPlan` (table of the five cases with exact strings); `TestExistingBoxOldVersionReinstalls` (fake check output `jauntctl 0.4.0 (…)`, laptop `0.4.1` → recorder shows `start Install supervisor`, `detail Install supervisor: 0.4.0 → 0.4.1`, a fetch call); `TestExistingBoxNoVersionReinstalls` (check output empty); `TestExistingBoxSameVersionSkips`; `TestStatusShowsVersions` (box-status JSON with `"version":"0.4.0"`, laptop `0.4.1` → output contains `version  0.4.0 (this laptop 0.4.1)`; JSON without version → `unknown`).
- [ ] **Step 2: Run** them — FAIL.
- [ ] **Step 3: Implement.** `InstallSupervisor` with `wait=false` spawns `internal-push` for a reinstall too.
- [ ] **Step 4: Run** `go vet ./... && go test ./...` — PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): keep the box on the laptop's release; status shows both versions`.

### Task 5: Publishing: GoReleaser, the tag workflow, CHANGELOG, install.sh

**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/release.yml`, `CHANGELOG.md`, `scripts/changelog-section.sh`, `scripts/changelog-section_test.sh`, `site/install.sh`, `scripts/install_test.sh`
- Modify: `.github/workflows/ci.yml` (shellcheck and run the two new shell tests)

**Interfaces:**
- Consumes: ldflags targets from Task 1 (`-X github.com/WA-Systems-EU/jaunt/internal/version.version={{.Version}}`, `.commit={{.FullCommit}}`, `.date={{.Date}}`); asset names from Global Constraints.
- Produces: `scripts/changelog-section.sh <version>` prints that version's `CHANGELOG.md` section body to stdout, exit 1 when missing. `site/install.sh [--version X.Y.Z] [--dir DIR]`; env `JAUNTBOX_RELEASE_ROOT` (default the GitHub download root) and `JAUNTBOX_LATEST_URL` (default `https://github.com/WA-Systems-EU/jaunt/releases/latest`) for tests.

- [ ] **Step 1: `CHANGELOG.md`** (Keep a Changelog). Section `## [0.4.0] - 2026-11-13` with: a one-paragraph summary (personal self-deleting Hetzner box on your tailnet; secrets and a private GitHub App; agents in their own containers; reaching your own services; the terminal UI); `### Upgrade notes`: "First release. Install with `install.sh`. `box_binary` is now only needed for development builds; the next `jauntbox up` reinstalls the box binary at 0.4.0."
- [ ] **Step 2: `scripts/changelog-section.sh` and its test** — test cases: `0.4.0` prints the summary and not the next section's heading; `9.9.9` exits 1.
- [ ] **Step 3: `.goreleaser.yaml`** (`version: 2`): one build `./cmd/jauntbox`, binary `jauntbox`, `env: [CGO_ENABLED=0]`, `flags: [-trimpath]`, `goos: [linux, darwin, freebsd]`, `goarch: [amd64, arm64]`, ldflags above plus `-s -w`; archive `name_template: "jauntbox_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, `formats: [tar.gz]`, files `LICENSE`, `README.md`; `checksum.name_template: checksums.txt`; `release.prerelease: auto`; `changelog.disable: true`; `release.extra_files: [{glob: site/install.sh}]`.
- [ ] **Step 4: Verify locally:** `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish` produces six archives and `checksums.txt` in `dist/`; `tar -tzf dist/jauntbox_*_linux_amd64.tar.gz` lists `jauntbox`, `LICENSE`, `README.md`; the extracted linux/amd64 binary prints a `jauntbox <snapshot> (commit …)` line. Add `dist/` to `.gitignore`.
- [ ] **Step 5: `.github/workflows/release.yml`**: `on: push: tags: ["v*"]`; `permissions: contents: write, id-token: write, attestations: write`; job 1 reruns CI (`uses: ./.github/workflows/ci.yml` after adding `workflow_call:` to `ci.yml`); job 2 (needs 1): checkout with `fetch-depth: 0`, setup-go, `scripts/changelog-section.sh "${GITHUB_REF_NAME#v}" > notes.md` (fails the job when missing — Review Focus 5), `goreleaser/goreleaser-action@v6` with `version: "~> v2"` and `args: release --clean --release-notes notes.md`, then `actions/attest-build-provenance@v2` with `subject-path: 'dist/*.tar.gz, dist/checksums.txt'`.
- [ ] **Step 6: Write `scripts/install_test.sh` first** (fails while `site/install.sh` is missing), using the snapshot `dist/` from Step 4 copied to a temp dir laid out as `<root>/v0.0.0-test/` with the archive renamed to version `0.0.0-test` and a regenerated `checksums.txt`, served as `file://`. Cases: `--version 0.0.0-test --dir $tmp/bin` installs and the binary runs; a corrupted archive → non-zero exit, nothing installed; `PATH` shim dirs that offer only `shasum` (and only `sha256`) still verify (Review Focus 3); `uname` shim printing `MINGW64_NT` → "unsupported" error; target dir not on `PATH` → output mentions adding it to `PATH`; latest resolution with `JAUNTBOX_LATEST_URL` pointing at a `file://…/v0.0.0-test` path.
- [ ] **Step 7: Write `site/install.sh`** per the spec's install.sh section: `uname -s` → `linux|darwin|freebsd`, `uname -m` → `amd64|arm64`; latest = last path segment of `curl -fsSLI -o /dev/null -w '%{url_effective}' "$JAUNTBOX_LATEST_URL"` with the `v` stripped; checksum with `sha256sum`, else `shasum -a 256`, else `sha256 -q`; install with `mkdir -p` + `install -m 0755` (or `cp` + `chmod`); print `jauntbox --version` at the end.
- [ ] **Step 8: Run** `sh scripts/changelog-section_test.sh && sh scripts/install_test.sh && shellcheck site/install.sh scripts/*.sh` — PASS. Add both tests and the shellcheck paths to `ci.yml`.
- [ ] **Step 9: Commit** `build: GoReleaser, the tag workflow, CHANGELOG and install.sh`.

### Task 6: Docs, site and the release checklist

**Files:**
- Modify: `README.md`, `CONTRIBUTING.md`, `site/index.html`, `site/quickstart.html`, `site/docs.html`, `site/roadmap.html`, `site/faq.html` (only if it exists on the branch by then), `docs/jauntbox/SMOKE.md`, `examples/jauntbox.yaml` (the `box_binary` comment)

- [ ] **Step 1: README and quick start:** Install section first: `curl -fsSL https://www.jauntbox.eu/install.sh | sh`, pinning with `sh -s -- --version 0.4.0`, then `go install github.com/WA-Systems-EU/jaunt/cmd/jauntbox@v0.4.0`, then building from source. Remove "During the beta you build it from source; packaged installs come later" and every claim that `box_binary` is required off Linux amd64; say it is only for development builds. Mention `jauntbox --version` and the version line in `status`.
- [ ] **Step 2: `CONTRIBUTING.md`:** the numbering rules verbatim from the spec's "Numbering rules" section, then the release checklist: CHANGELOG section with Upgrade notes first; update the site version marker; CI green on `trunk`; when the release touches the box, live smoke (`up`, `status`, `down`) with an `-rc` tag first; push the tag; check the release page, the attestation (`gh attestation verify <archive> -R WA-Systems-EU/jaunt`) and `install.sh`.
- [ ] **Step 3: Site:** "Pack light" on `index.html` shows the `install.sh` line instead of the clone/build block and a small `v0.4.0` marker (existing classes only, so `styles.css` needs no rebuild; rebuild it if a new class is unavoidable); docs `#config` row for `box_binary`; roadmap marks releases done; FAQ "What do I need on my laptop?" drops "built from source during the beta".
- [ ] **Step 4: `SMOKE.md`:** add "Plan 07 checks (releases)": `install.sh` on Linux and on GhostBSD/FreeBSD; `jauntbox --version`; `up` from a release laptop on a new box (the box fetches; `jauntctl --version` on the box matches); `up` on a box running an older version reinstalls with `old → new`; `status` shows the version line; `gh attestation verify`.
- [ ] **Step 5: Drift audit:** a read-only agent compares README, the four (or five) site pages and `CONTRIBUTING.md` with the code and the spec and lists drift; fold its findings in.
- [ ] **Step 6: Commit** `docs: install from releases, numbering rules and the release checklist`.

---

**After the plan:** hand the branch to Thomas for merge and push. Releasing is his: push `trunk`, wait for Pages to publish `install.sh`, then `git tag -a v0.4.0 -m "jauntbox 0.4.0" && git push origin v0.4.0`, and run the Plan 07 checks.
