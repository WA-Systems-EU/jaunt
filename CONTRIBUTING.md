# Contributing to jauntbox

Thanks for wanting to help. A few things to know before you open a pull request.

## Licence

jauntbox is open source under the [MIT licence](LICENSE). By opening a pull request you
agree that your contribution is licensed under the same terms. Sign off each commit
(`git commit -s`) to certify that you wrote it or otherwise have the right to submit it,
as described in the [Developer Certificate of Origin](https://developercertificate.org/).

WA Systems builds an enterprise edition with extra features and a hosted control plane
on top of jauntbox, in a separate repository. This repository stays the complete,
open-source CLI that a single person needs to run their own box.

## How to contribute

- Open an issue first for anything bigger than a small fix, so we can agree on the
  approach.
- Keep commits to plain conventional messages (`fix(jauntbox): …`, `feat(jauntbox): …`).
- The laptop client is `cmd/jauntbox`, the box side `cmd/jauntctl`; code both need lives
  under `internal/`. On Linux, build both with `go build -o <dir> ./cmd/jauntbox ./cmd/jauntctl`.
  `jauntctl` does not compile for macOS or FreeBSD; there, build `./cmd/jauntbox` and then
  `GOOS=linux GOARCH=amd64 go build -o <dir> ./cmd/jauntctl`.
- Tests never call real Hetzner, Tailscale, GitHub or Docker. Use the existing fakes and
  `httptest`. Run `go vet ./... && go test ./...` and the shell tests under `provision/`
  before you open a pull request.
- Never commit secrets: API tokens, private keys, tfvars or state files.

## Version numbers

Semantic versioning, still 0.x:

- **Minor** (0.4 → 0.5): anything you must act on when upgrading (a config key renamed or newly required, a tailnet policy rule to add, a `down`/`up` needed to get a new box image, a `box.json` format change), and any large new feature (provider layer, new forge).
- **Patch** (0.4.0 → 0.4.1): fixes and small improvements that need nothing beyond replacing the binary.
- **Pre-releases** (`v0.5.0-rc.1`): optional, to try a release on your own box first. Marked as pre-release on GitHub; `install.sh` never picks them unless asked by exact version.
- **1.0**: after a period of daily use with no breaking change to the config file or `box.json`. From then on, breaking changes need a major.
- Every release has a `CHANGELOG.md` entry; anything you must act on goes first, under **Upgrade notes**.

## Release checklist

Pushing a `v*` tag builds and publishes everything (`.github/workflows/release.yml`).
Before you push it:

1. Write the `CHANGELOG.md` section for the version. **Upgrade notes** come first. The
   release notes are taken from this section.
2. Update the version marker on the site (`site/index.html`, "Pack light").
3. Make sure CI is green on `trunk`.
4. If the release touches the box, run the live smoke (`up`, `status`, `down`, see
   `docs/jauntbox/SMOKE.md`) with an `-rc` tag first, for example `v0.5.0-rc.1`. An `-rc` tag uses the CHANGELOG section of its
   version (`## [0.5.0]`) unless the changelog has an exact `## [0.5.0-rc.1]` section.
5. Push the tag: `git tag -a v0.5.0 -m "jauntbox 0.5.0" && git push origin v0.5.0`.

After the workflow finishes, check three things:

- The release page has all eight archives and `checksums.txt`, and the notes match the
  CHANGELOG section.
- The build provenance: `gh attestation verify <archive> -R WA-Systems-EU/jaunt`.
- `install.sh` (https://www.jauntbox.eu/install.sh) installs the new release, and
  `jauntbox --version` prints it.
