# Contributing to jauntbox

Thanks for wanting to help. A few things to know before you open a pull request.

## Licence and the contributor agreement

jauntbox is free software under the [GNU AGPL v3.0 or later](LICENSE).

WA Systems also builds commercial products on top of it (an enterprise edition with
extra features and a hosted control plane) in a separate repository. To keep that
possible, every contribution needs a signed **Contributor Licence Agreement (CLA)**.
With it you keep the copyright of your work, and you grant WA Systems the right to
use and relicense it, including under commercial terms. The open-source jauntbox stays
under the AGPL.

The first time you open a pull request, a bot will ask you to sign the CLA. We can't
merge a pull request until it is signed.

## What lives where

- **This repository:** the jauntbox CLI and everything a single person needs to run
  their own box. It works on its own and stays open source.
- **Not here:** enterprise features and the hosted control plane. They run on top of
  this code through its public commands and interfaces. They are not mixed into it.

## How to contribute

- Open an issue first for anything bigger than a small fix, so we can agree on the
  approach.
- Keep commits to plain conventional messages (`fix(jauntbox): …`, `feat(jauntbox): …`).
- Tests never call real Hetzner, Tailscale, GitHub or Docker. Use the existing fakes and
  `httptest`. Run `go vet ./... && go test ./...` and the shell tests under `provision/`
  before you open a pull request.
- Never commit secrets: API tokens, private keys, tfvars or state files.

## Name

"jauntbox" is the name of WA Systems' project. Forks are welcome under the AGPL, but
please give yours a different name.
