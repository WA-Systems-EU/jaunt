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

## GitHub and secrets

The box never holds a long-lived credential on disk. Your secrets stay on the laptop
in `~/.config/jauntbox/secrets/` (files mode 0600) and are pushed into the box's RAM
(`/run/jauntbox/secrets`) when the box doesn't hold them (a new or rebooted box). After
`jauntbox secrets set`, run `jauntbox unlock` to push the new value.

```sh
jauntbox setup github                          # once: create and install the GitHub App
jauntbox setup github --check                  # later: report drift from the expected permissions
jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN   # an agent credential (prompted, never echoed)
jauntbox secrets list                          # names only, never values
jauntbox unlock                                # push secrets to the box: after a reboot or after changing secrets
```

- `setup github` runs the GitHub App manifest flow in your browser and creates
  `jauntbox-<user>` under the account in `github.org` (empty means your personal
  account). It needs an admin credential once: `gh auth token`, or `GITHUB_ADMIN_TOKEN`
  when `gh` is absent. That credential is used on the laptop only; it is never stored
  and never sent to the box. Install the App on the repos you want agents to touch.
- The App has `contents: write`, `pull_requests: write` and `metadata: read`. Nothing
  else: no `workflows`, no `administration`, no `secrets`.
- Rulesets: on each repo the default branch (plus any `github.protected_patterns`
  entries in your config) can only be updated or merged by repo admins, which is you.
  The App never bypasses the ruleset, so agents push branches and open PRs; you merge.
- On the box, `git` uses a credential helper that mints a token for one repository,
  valid for an hour and cached until 10 minutes before it expires. `jaunt-repos`
  lists the repos the App is installed on and clones them; it warns if it finds an old
  `gh auth login`, which you should remove.
- **Locked state.** After a reboot (or before the first unlock) the box has no secrets
  in memory. `git push` then prints the locked message. Laptop commands that reach the
  box (`jauntbox`, `ssh`, `status`) unlock it automatically; a locked box shows the
  locked message in `jauntbox status` if that automatic unlock failed or this laptop has
  no secrets stored. `jauntbox unlock` does it explicitly (and refuses to push an empty
  set, so a secret-less client never wipes a working box).
- Tokens are revoked before the box deletes itself; `token.revoked` events are written
  to `/home/.jauntbox/events.jsonl` on the volume.
- Box-side commands answer to `jauntctl` (`supervise`, `stop`, `night-set`,
  `box-status`, `secrets-load`, `mint`, `repos`, `git-credential`). A `jauntbox`
  symlink to it remains on the box for old units.
- **Upgrading.** After upgrading jauntbox, run `jauntbox` or `jauntbox up` once against
  an existing box before `jauntbox down`, so the box gets `jauntctl`.

## Status

**Under construction** on the `feat/remote-workspace` branch.

- Design: [`docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md`](docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md)
- Plan 01 (walking skeleton): [`docs/superpowers/plans/2026-11-09-jauntbox-01-walking-skeleton.md`](docs/superpowers/plans/2026-11-09-jauntbox-01-walking-skeleton.md)

The earlier OpenTofu implementation (and the team/fleet design built on it) lives in the
`trunk` branch history; a fleet version comes later.

Built by [WA Systems](https://wa-systems.eu).
