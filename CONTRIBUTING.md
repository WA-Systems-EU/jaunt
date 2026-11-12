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
  under `internal/`. Build both with `go build -o <dir> ./cmd/jauntbox ./cmd/jauntctl`.
- Tests never call real Hetzner, Tailscale, GitHub or Docker. Use the existing fakes and
  `httptest`. Run `go vet ./... && go test ./...` and the shell tests under `provision/`
  before you open a pull request.
- Never commit secrets: API tokens, private keys, tfvars or state files.
