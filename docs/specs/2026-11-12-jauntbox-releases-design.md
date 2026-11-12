# jauntbox releases: design

Date: 2026-11-12. Owner: Thomas (WA Systems). Status: agreed in conversation, to be planned.

## Why

Today you build jauntbox from source, and the laptop copies its own binary to the box. That only works on a Linux amd64 laptop. On anything else (the owner's GhostBSD machine, macOS) you must set `box_binary` by hand. There are no releases, so nobody can say which version they run, and nobody outside can install it without Go.

The owner wants "the release work with proper release numbering rules": installable versions, explicit rules for what each number means, and the version visible everywhere. He rates the current state as "a 0.4 or 0.5 release state: past the prototype but not battle tested".

Success: someone who hears about jauntbox runs one command and has the right binary on Linux, macOS or FreeBSD (amd64 or arm64); `jauntbox --version` tells them what they run; the box always runs the same version as the laptop, or the laptop says so; the owner cuts a release by pushing a tag.

## Goals

1. **Prebuilt binaries on GitHub Releases** (the owner chose this as the install method): Linux, macOS and FreeBSD, each amd64 and arm64 (owner's choice). `go install github.com/WA-Systems-EU/jaunt/cmd/jauntbox@vX.Y.Z` keeps working on its own.
2. **First release: v0.4.0** (owner's choice).
3. **Numbering rules**, written in `CONTRIBUTING.md` (owner approved, see below).
4. **Version in every binary** and `--version` on both `jauntbox` and `jauntctl`.
5. **The box runs the laptop's version**, fetched from the release; mismatches are visible.
6. **One-tag releases**: pushing a `v*` tag builds and publishes everything.
7. **`install.sh`** served from the site.
8. **`CHANGELOG.md`**, the source of each release's notes.
9. **Site and README** show how to install and the current version.

## Non-goals (for now)

- Homebrew tap, AUR, FreeBSD port, distro packages. Release artifacts are named so these can be added later without changes.
- cosign signatures. GitHub's build provenance attestation (`actions/attest-build-provenance`) is the only signature.
- Pinning the versions of Docker, Tailscale or Claude Code installed on the box.
- Windows builds.
- Self-update (`jauntbox upgrade`). Upgrading is re-running `install.sh` or `go install`.

## Anti-goals

- A release that can't be reproduced from its tag, or that someone other than the repo's workflow could have built unnoticed.
- The box running a binary nobody can name.
- `install.sh` that installs without checking the checksum, or needs root by default.
- Moving or deleting a published tag. (`v0.1.0` exists already: a July 2026 checkpoint of the old tofu/control-plane design. It stays.)

## Numbering rules (owner approved)

Semantic versioning, still 0.x:

- **Minor** (0.4 → 0.5): anything you must act on when upgrading (a config key renamed or newly required, a tailnet policy rule to add, a `down`/`up` needed to get a new box image, a `box.json` format change), and any large new feature (provider layer, new forge).
- **Patch** (0.4.0 → 0.4.1): fixes and small improvements that need nothing beyond replacing the binary.
- **Pre-releases** (`v0.5.0-rc.1`): optional, to try a release on your own box first. Marked as pre-release on GitHub; `install.sh` never picks them unless asked by exact version.
- **1.0**: after a period of daily use with no breaking change to the config file or `box.json`. From then on, breaking changes need a major.
- Every release has a `CHANGELOG.md` entry; anything you must act on goes first, under **Upgrade notes**.

Why v0.4.0 and not lower: `v0.1.0` is taken, and Go's `@latest` picks the highest tag, so any release must sort above it; 0.4 matches the owner's own rating and leaves 0.5 for the first release shaped by real use.

## The version in the binary

- Release builds set the version, commit and build date at link time (GoReleaser ldflags into one small package, e.g. `internal/version`).
- A binary built without them (`go build`, `go install`) falls back to `debug.ReadBuildInfo`: `go install …@v0.4.0` reports `0.4.0`; a local checkout build reports `dev` plus the VCS revision (and `+dirty` when modified).
- `jauntbox --version` and `jauntctl --version` print one line: `jauntbox 0.4.0 (commit abc1234, built 2026-11-13)`, or `jauntbox dev (commit abc1234+dirty)`.
- `jauntctl box-status --json` gains a `version` field, so the laptop learns the box's version over the existing call.

## The box gets the laptop's version

- **Release laptop build** (a real version, not `dev`; a pre-release laptop counts and fetches its exact pre-release; a Go pseudo-version from `go install` of an untagged commit counts as `dev`): the box downloads `jauntbox_<version>_linux_<box arch>.tar.gz` and `checksums.txt` from that version's GitHub Release, checks the archive's SHA-256 against `checksums.txt`, unpacks and installs it exactly where `installCmd` installs today (`/usr/local/bin/jauntctl`, `jauntbox` symlink). The download runs on the box (it has public egress; this replaces the scp). The box arch comes from the profile's existing `arch` field (`amd64` for every profile today).
- A checksum mismatch fails hard, with no fallback: it points at tampering or a broken release, not at GitHub being unreachable.
- If the box cannot download (GitHub unreachable), the laptop downloads the same two files itself, checks them, and copies the binary as today. Then it fails with a clear message only if both paths fail.
- **Development build** (`dev`): today's behaviour, unchanged: copy the laptop's binary on Linux amd64, else `box_binary`.
- `box_binary`, when set, always wins (for testing a local build on the box). Today `BoxBinaryPath` ignores it on a Linux amd64 laptop; that changes.
- **Mismatch**: `status` shows the box's version next to the laptop's, flagged when they differ. `up` and bare `jauntbox` on an existing box whose version differs from a release laptop reinstall the box binary (the existing "Install supervisor" step), with a detail line `0.4.0 → 0.4.1`. A box whose version can't be read (every box running a pre-0.4.0 binary: no `version` in `box-status`) counts as different, so a release laptop reinstalls it too; the 0.4.0 entry needs no upgrade note for it. A dev laptop against a release box, or the reverse, only warns.

## Building for six targets

Today the code does not compile for macOS or FreeBSD: `internal/agentrun/guard.go` compares `st.Mode & unix.S_IFMT` with mismatched integer types off Linux. That package only runs on the box. The builder makes all six targets build (a Linux build tag on the box-only code, or a type conversion), and CI cross-builds all six on every push so it stays that way.

## Publishing

- `.github/workflows/release.yml`, on push of a `v*` tag:
  - runs the existing CI checks first (tests, shell tests, shellcheck);
  - GoReleaser builds the six targets (`CGO_ENABLED=0`, `-trimpath`), produces `jauntbox_<version>_<os>_<arch>.tar.gz` (binary, `LICENSE`, `README.md`) and `checksums.txt`;
  - publishes the GitHub Release with the notes taken from that version's `CHANGELOG.md` section; a tag with a pre-release suffix is marked pre-release;
  - attests build provenance for the archives and `checksums.txt`.
- The release workflow fails if `CHANGELOG.md` has no section for the tag.
- `.goreleaser.yaml` lives at the repo root.

## install.sh

- Served at `https://www.jauntbox.eu/install.sh` (it lives in `site/`, so the Pages deploy publishes it), and also attached to each release.
- `curl -fsSL https://www.jauntbox.eu/install.sh | sh` installs the latest non-pre-release; `… | sh -s -- --version 0.4.0` pins one; `--dir` changes the target.
- Detects OS (`Linux`, `Darwin`, `FreeBSD`) and arch (`x86_64`/`amd64`, `aarch64`/`arm64`), downloads the archive and `checksums.txt`, verifies with `sha256sum` or `shasum -a 256` or `sha256` (FreeBSD), installs to `~/.local/bin` (no root), and says how to add it to `PATH` if missing.
- POSIX `sh`, passes shellcheck, refuses unknown OS/arch with a clear message. Prints `jauntbox --version` at the end.

## Changelog, docs and site

- `CHANGELOG.md` at the root, Keep a Changelog style; the 0.4.0 entry summarises what exists (plans 01 to 06) in a few lines, not a full history.
- `CONTRIBUTING.md`: the numbering rules and a release checklist (changelog entry, CI green, live smoke of `up`/`status`/`down` with a release-candidate build when the release touches the box, tag, check the release page and `install.sh`).
- README and the site's quick start: Install with `install.sh` first, then `go install`, then building from source; drop "during the beta you build it from source" wording. The home page's "Pack light" block shows the install command. The site shows the current version (one place, updated as part of the release checklist).
- Roadmap: releases marked done.

## Constraints

- Go, existing cobra CLI; tests never call real GitHub, Hetzner or Tailscale (download and checksum logic is tested with `httptest` and fakes; `install.sh` gets a shell test against a local file server or fixtures).
- Commits: plain conventional messages, no attribution trailers.
- Never assume a default branch name; the base is `trunk`.
- Pushing a tag and publishing a release are the owner's actions.
- The GitHub repo `WA-Systems-EU/jaunt` is public, so release downloads, `install.sh`, `go install` and build attestations work without tokens.

## Calls made without the owner (mine, for review)

1. The box downloads its own binary, with the laptop as fallback, rather than the laptop always uploading.
2. `up` and bare `jauntbox` upgrade the box binary to a newer or older release version automatically; only `status` and dev builds just warn.
3. GitHub attestations instead of cosign.
4. `install.sh` installs to `~/.local/bin` by default, not `/usr/local/bin`.
5. Archives are `.tar.gz` everywhere (macOS included).
6. A box with an unreadable version (every box from before 0.4.0) is treated as different and gets reinstalled by a release laptop.
7. A checksum mismatch on the box fails hard rather than falling back to the laptop.

## Left to the builder

- Exact package names, ldflags variable names, how the download step reports itself in the step list.
- GoReleaser config details beyond the targets and names above.
- How the site's version marker is written (static text updated by the checklist is enough).
