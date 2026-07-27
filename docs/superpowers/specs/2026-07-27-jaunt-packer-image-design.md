# jaunt Packer image — bake the system layer for fast boots

**Date:** 2026-07-27
**Status:** Design — for later implementation (after the control-plane walking skeleton)
**Related:** [control-plane design](./2026-07-24-jaunt-control-plane-design.md),
[site design](./2026-07-21-jaunt-tofu-site-design.md)
**Reference model:** `df_infra/packer/devbox-hetzner` (an existing Hetzner Packer devbox).

## Purpose

Today a fresh jauntbox provisions from a stock `ubuntu-26.04` image via cloud-init: `apt-get`
installs, Docker/gh/Neovim/Tailscale downloads, shell tooling — several minutes before the box
is usable, on **every** first boot of a new instance. A Packer-built snapshot with that
**system layer already baked** makes `jaunt up` boot in ~a minute, and shrinks cloud-init to
per-box concerns. This is the piece that makes the daily `jaunt up` *feel* instant.

## The layering insight (this shapes everything)

jaunt splits durable from ephemeral at the **filesystem** level: the root disk is throwaway,
and `/home` is a **persistent per-engineer volume**. That changes the Packer strategy versus
the reference devbox (which has no separate /home volume and bakes everything, including the
user's `~`, into one image).

For jaunt there are **three layers**, and each is fast for a different reason:

| Layer | Lives on | Made fast by | Contents |
|---|---|---|---|
| **System** | root disk (`/usr`, `/opt`, `/usr/local`) | **the Packer image** | apt packages, Docker engine, Neovim, zsh/fzf/starship/bat/fd/ripgrep, gh, Tailscale, build deps |
| **User** | the `/home` volume (`~/.asdf`, `~/.local`, dotfiles) | **volume persistence** (compiled once per engineer, then reused) | asdf + Go/Rust/Node/Ruby, Claude Code, `.zshrc`/dotfiles |
| **Per-box** | runtime | cloud-init (tiny) | engineer user, volume mount, Tailscale join, idle-watch |

**Key consequence:** baking the *user* toolchain into the image is pointless — the `/home`
volume mount shadows the image's `/home` at boot. But that toolchain is already a **one-time**
cost: it compiles on the first-ever box for an engineer and the volume keeps it thereafter. So
the Packer image's job is **only the system layer** — which is exactly the part that otherwise
re-runs `apt`/downloads on every fresh instance. After this: first box for an engineer = fast
system (image) + one-time toolchain compile (volume); every box after = fast system + warm
toolchain. No cold `apt` ever again.

## Architecture

- **Builder:** Packer with the **`hcloud`** (Hetzner Cloud) builder — boots a temp server from
  `ubuntu-26.04`, runs provisioners, snapshots it, destroys the temp server. Output: a snapshot
  ID.
- **Provisioners run the module's own scripts — DRY.** The system-layer provisioning is *the
  same code* cloud-init uses, not a copy. Since `dev-setup.sh` is idempotent (skip-if-present),
  running it at build time bakes the system layer, and cloud-init re-running it at boot is a
  near-instant no-op. One source of truth for provisioning logic.
- **Refactor to enable this:** split today's `dev-setup.sh` into
  - `system-setup.sh` — the generic, engineer-agnostic system installs (apt, Docker binaries,
    Neovim, zsh/fzf/starship, gh, Tailscale). **Packer runs this.**
  - `dev-setup.sh` — the per-box/per-engineer glue (user creation, Docker data-root on the
    volume, `chsh`, then `dev-setup-user.sh`). **cloud-init runs this**; its system steps are
    no-ops on a baked image.
  - `dev-setup-user.sh` — unchanged (user/volume layer).

## What is baked vs. deferred

- **Baked into the image (system):** all apt packages incl. `zsh fzf ripgrep fd-find bat` +
  build deps; Docker engine + CLI; Neovim `v0.12.4` at `/opt/nvim`; starship at
  `/usr/local/bin`; gh; Tailscale (installed, `tailscaled` enabled but **not** joined — no
  identity baked); `fd`/`bat` name symlinks.
- **NOT baked (user/volume, per the layering above):** asdf + language toolchain, Claude Code,
  `.zshrc`/dotfiles — these land in `~` on the volume.
- **NOT baked (per-box, at boot):** engineer user (pinned UID), volume mount at `/home`,
  Tailscale **join** (identity), Docker `data-root` pointed at the volume, idle-watch. No
  secrets and no Tailscale auth key are ever baked into the snapshot.

## Integration with the jaunt roots

The Packer snapshot becomes the `box`'s image. The `infra` root gains a `base_image` input
(the snapshot ID / name) it already declares; `infra` outputs it and the `box` root uses it for
`hcloud_server.image` (replacing the stock `ubuntu-26.04` default). Because image selection is
admin-declared config, updating to a new snapshot is an **admin `git push`** → `infra` apply →
next fresh `jaunt up` uses it. (Consistent with "engineers consume config, admins produce it.")

## Build & rebuild flow

- The Packer template lives in the jaunt repo (`packer/jauntbox/`), alongside `system-setup.sh`.
- An **admin** (laptop or CI) runs `packer build` — rarely, only when the system layer changes
  (new apt package, Neovim bump, etc.). It needs the Hetzner token; **no Tailscale/secret**.
- The resulting snapshot ID is recorded in the admin config and promoted via `git push` to
  jauntd. Engineers never build images.
- Pin the base image + package versions for reproducible builds; tag snapshots
  `jauntbox-YYYYMMDD` so rollback is a config edit.

## Shell / tooling baked (from the reference devbox + our additions)

zsh (login shell), fzf with **Ctrl-T** (fd + bat preview) / **Ctrl-R** (`--sort --exact`),
starship prompt, Neovim + ripgrep/fd/bat, Docker, gh, Tailscale, jq/unzip/build-essential.
(The reference devbox also bakes zellij + tmux; treat those as optional add-ons to
`system-setup.sh` — see open questions.)

## Non-goals / future

- **Pre-warmed "golden volume"** (a snapshot of `/home` with the toolchain already compiled, so
  even the *first* box for a new engineer skips the language compile) — a separate mechanism
  from the image; deferred. The image alone already removes the *recurring* cold cost.
- ARM/multi-arch images — x86 only (matches the toolchain pins + df stack's amd64 images).
- Baking any engineer identity, dotfiles, or secrets — never.

## Open questions (resolve during planning)

- **Multiplexer:** bake zellij (with the reference's Ctrl+p→Alt+b rebind) and/or tmux, or leave
  to engineer dotfiles? Lean: bake tmux (ubiquitous), offer zellij as an opt-in.
- **Snapshot reference plumbing:** snapshot **name** (human-stable, `jauntbox-current`) vs **ID**
  (immutable) in config. Lean: config holds a name → a `hcloud_image` data source resolves it in
  `infra`, so rebuilds don't require an ID edit — but pin for rollback.
- **CI build:** GitHub Actions `packer build` on changes to `packer/` + `system-setup.sh`, or
  admin-run only. Lean: CI, since it's the one heavy step and benefits from being reproducible.

## Build sequence (a later plan)

1. **Extract `system-setup.sh`** from `dev-setup.sh` (system vs per-box split); verify a fresh
   cloud-init box is unchanged in behavior (scripts still idempotent).
2. **Packer template** (`packer/jauntbox/`, hcloud builder) running `system-setup.sh`; build a
   snapshot; smoke-test a server from it.
3. **Wire `base_image`** through `infra` → `box`; boot a box on the snapshot; confirm cloud-init
   is now near-instant (system steps no-op) and the toolchain still lands on the volume.
4. **Rebuild flow / CI** + snapshot promotion via the admin git-push path.

This depends on the control-plane root split (Plan 01) for the `infra`/`box` `base_image`
plumbing, so it sequences **after** the walking skeleton.
