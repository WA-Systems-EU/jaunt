# jauntbox

> **Your code workspace, remote by reflex.** A right-sized cloud box one command away,
> where your AI agents work in containers — far from your laptop, your personal data and
> your server keys.

AI coding agents run for hours with broad access to whatever machine they're on. jauntbox
moves that machine somewhere else: a box you start in the morning while the coffee
brews, work on over Tailscale SSH, and that tears **itself** down in the evening. Your
work lives on a persistent volume, so switching to a bigger box takes minutes and nothing
is lost.

- **One command up, one down — and it goes down by itself:** idle, 10 hours, or 19:00,
  unless you're actively working or authorised a robot night session.
- **Agents in containers, always:** each project runs in its own container with only its
  own repo and a GitHub token scoped to that one repo, valid for an hour.
- **Humans merge and deploy:** agents push branches and open PRs; your default branch —
  whatever it's called — is protected on the forge.
- **Your laptop keeps the keys to production.** The box can't reach your servers.
- **Cheap and EU by default:** Hetzner Cloud + Tailscale. Open source, MIT.

## The goal: one coffee

From installing jauntbox to typing in a shell on your first box should take about as
long as a cup of coffee — no files to edit, no infrastructure to learn.

```sh
jauntbox init            # detects your user, SSH key and timezone; asks for two API tokens
jauntbox                 # brings the box up if needed and attaches your persistent session
```

## Quick start

Needs a Hetzner Cloud project token (`HCLOUD_TOKEN`) and a Tailscale API key
(`TAILSCALE_API_KEY`). `jauntbox init` detects your settings, checks both tokens, picks
server types, writes `~/.config/jauntbox/config.yaml` (never the tokens) and prints the
Tailscale policy snippet. It can save the tokens to `~/.config/jauntbox/env` (mode 0600),
which jauntbox reads automatically when the variables are unset; or it prints the two
`export` lines for your shell profile. Prefer editing by hand? Copy
`examples/jauntbox.yaml` to `~/.config/jauntbox/config.yaml`.

```sh
jauntbox init                       # once: config, token checks, prints the ACL snippet
jauntbox acl-snippet                # reprint the snippet for your tailnet policy
jauntbox up small                   # returns at once; the box is ready a few minutes later
jauntbox status                     # box, tailnet presence, deadline and verdict
jauntbox                            # up + wait + attach (reconnects after network drops)
jauntbox ssh                        # a plain shell on the box
jauntbox night --until 07:00        # let agents keep the box overnight
jauntbox resize big                 # swap to another profile; /home persists
jauntbox down                       # wrap up and delete the box (the volume stays)
```

Not yet live-tested end to end: see [`docs/jauntbox/SMOKE.md`](docs/jauntbox/SMOKE.md).

## Status

**Under construction** on the `feat/remote-workspace` branch.

- Design: [`docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md`](docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md)
- Plan 01 (walking skeleton): [`docs/superpowers/plans/2026-11-09-jauntbox-01-walking-skeleton.md`](docs/superpowers/plans/2026-11-09-jauntbox-01-walking-skeleton.md)

The earlier OpenTofu implementation (and the team/fleet design built on it) lives in the
`trunk` branch history; a fleet version comes later.

Built by [WA Systems](https://wa-systems.eu).
