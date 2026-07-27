# jaunt control plane — `jauntd` + `jaunt` CLI

**Date:** 2026-07-24
**Status:** Design — approved for planning
**Builds on:** [2026-07-21 jaunt tofu site design](./2026-07-21-jaunt-tofu-site-design.md)

## Vision

An engineer sits down, types one command, and is dropped into a shell on a ready dev box.
At end of day they close the laptop and walk away — the box tears **itself** down. A team
adopts the whole thing by forking one MIT repo, bootstrapping once, and pushing it out.

```
jaunt          # up (if needed) + auto-SSH in — the entire daily ritual
jaunt down     # immediate teardown (otherwise idle-watch does it for you)
```

## Problem

Today `tofu` is the only interface: `cd tofu/hetzner && tofu apply -var box_on=true`. That
is correct but unshippable to a team — every laptop would need the repo checked out clean,
`tofu` installed, and the **Hetzner token + Tailscale key** on disk, plus its own drifting
copy of state. We need a smooth, secure, team-scalable front end **without** reimplementing
infra against the cloud API (that would be a second source of truth and guaranteed drift).

## The core idea: two lifecycles

The insight that makes this tractable is that infra changes at two very different rates, and
we split them:

| | Runs where | Frequency | Owns | Auth |
|---|---|---|---|---|
| **Bootstrap tofu** | admin laptop / CI | rare | the `jauntd` box, tailnet ACLs, engineer registry | admin creds |
| **`jauntd`** | tiny always-on box, on the tailnet | constant | flips `box_on` per engineer via `tofu` | Tailscale identity |
| **`jaunt` CLI** | each laptop | daily | thin client → `jauntd` | Tailscale identity |
| **idle-watch** | each engineer's box | continuous | auto-**off** at EOD (already built) | n/a |

The **frequent** path (daily up/down) is instant because it runs on `jauntd` on the tailnet.
The **heavy** path (`tofu` for scale/new-resources) only runs on the **rare** admin path.

## Non-goals

- Reimplementing infra against the Hetzner API. `jauntd` **drives `tofu`**; tofu stays the
  single source of truth.
- A database. **Tofu state is the datastore** — the list of boxes, IPs, volume IDs. Nothing
  else to operate.
- Managing the *auto-off* lifecycle. `idle-watch` → `self-delete.sh` already deletes an idle
  box (server gone, volume kept). `jauntd` never needs a scheduler.
- Multi-cloud abstraction. Hetzner is the target (the UpCloud root remains, but the control
  plane targets the Hetzner roots).

## Architecture

### The repo has two roles

- **Public `jaunt` repo (GitHub, MIT):** the *product*. Fork it. Contains the base config for
  the tailnet, the `jauntd` box, the `jauntd` service, the `jauntbox` module, and the first
  jauntbox. This is distribution/upstream.
- **Live config remote = your own `jauntd` box.** Once deployed, the authoritative config
  lives on the jauntd box. An admin forks upstream, customizes, bootstraps, and from then on
  `git push jauntd` is how config changes.

### Bootstrap resolves the chicken-and-egg

You cannot push to `jauntd` before it exists, so the **first** apply runs from the admin's
fork (laptop/CI). It: creates the `jauntd` box, deploys the service, stands `jauntd` up as a
git remote, pushes the config to it, and (optionally) creates the first jauntbox. That is the
"push the repo out and voila" moment. The bootstrap's own state (the jauntd box + tailnet) is
the one piece of state that lives with the admin — a deliberate chicken-and-egg exception.

### The tofu roots are split — this is what makes `jauntd` minimal *by construction*

| Root | Applied by | State | Contains |
|---|---|---|---|
| **`infra`** | admin push → post-receive hook on jauntd | `infra/<user>.tfstate` (local on jauntd) | volume, Tailscale key, firewall, tag — the durable per-engineer resources + outputs |
| **`box`** | `jauntd` (jaunt API) | `box/<user>.tfstate` (local on jauntd) | **only** `hcloud_server` (gated on `box_on`) + volume attachment, reading the `infra` root's outputs via `terraform_remote_state` |

Because the `box` root's config contains *only the server*, `jauntd`'s daily-path apply
**cannot** scale, resize, or add resources even in principle — there is no resource for
anything durable in its config. It reads volume ID / firewall / cloud-init / Tailscale key as
read-only remote-state outputs from `infra`. Admin owns `infra`; `jauntd`'s API owns `box`.

> **`jauntd` is the sole applier of both per-engineer states** (infra via the git hook, box
> via the API — both execute *on the jauntd box*). One writer per state file means state can
> live **locally on jauntd**; no shared remote backend is required. Back it up by snapshotting
> the jauntd data volume.

### Config delivery: `jauntd` *is* the git remote

`jauntd` hosts a bare git repo, served over **Tailscale SSH** as the `git` user. Admins change
config by `git push jauntd`. A **`post-receive` hook** checks out the promoted ref and runs
`tofu apply` on the `infra` root for the declared engineers (idempotent — a no-op where
nothing changed). No GitHub-at-runtime dependency, no deploy key, no object storage.

- **Promotion, not floating HEAD.** `jauntd` tracks a promoted ref (an `infra-live` branch or
  a tag). A work-in-progress or broken commit on `main` can never break someone's morning
  `jaunt up`, because jauntd only ever runs config an admin explicitly pushed to the live ref.

### Two control channels, both gated by Tailscale identity

| Channel | Who | Auth | Capability |
|---|---|---|---|
| `jaunt` API (HTTP on the tailnet) | engineer | Tailscale **WhoIs** on the connection → caller's `user` | **only** `box_on` toggle for the caller's own box |
| `git push jauntd` (Tailscale SSH, `git` user) | admin | Tailscale SSH ACL → `group:admins` | change config → `infra` applies |

The boundary the user wanted ("only admins change scale/resources; jauntd only brings up one
instance per call") is enforced by *which channel you arrived on*, not by policy checks in
application code. An engineer's `jaunt up` can only ever start their declared box.

### `jaunt` CLI

Thin Go client. Talks to the `jauntd` API over the tailnet.

- `jaunt` (bare) — ensure the caller's box is up (`jauntd` runs `box` apply, `box_on=true`),
  poll Tailscale until `jauntbox-<user>` answers, then **auto-SSH in**. Idempotent: if already
  up, it just reconnects; if `idle-watch` deleted it overnight, tofu recreates it — same
  command, no special case.
- `jaunt down` — immediate teardown (`box_on=false`; server gone, volume kept).
- `jaunt status` — is my box up? show MagicDNS name / IP.

### `jauntd` service

Small Go service on the tiny always-on box. Responsibilities:

1. **jaunt API** — HTTP over the tailnet. Authenticate the caller via the Tailscale LocalAPI
   `WhoIs`; map identity → `user`. For `up`: if the box is already running, **skip the apply**
   and return connect info (idempotent, never disturbs a running box); otherwise run the `box`
   root apply (via `opentofu/tofu-exec`) with `box_on=true`. For `down`: apply
   `box_on=false`. Never pulls or writes config — it applies whatever the admin last promoted.
2. **git remote + `post-receive` hook** — bare repo + hook that checks out the promoted ref
   and applies the `infra` root for declared engineers. (Served by Tailscale SSH `git` user;
   the hook is a script, not part of the Go binary.)
3. **Holds the creds** — Hetzner token + Tailscale key, root-readable only, on the jauntd box.
   Never on any laptop.
4. **Holds per-engineer local tofu state** (`infra/<user>.tfstate`, `box/<user>.tfstate`).

Tech: Go (single binary, matches the CLI), **`opentofu/tofu-exec`** to drive `tofu`
(OpenTofu's own fork of terraform-exec — avoids the Terraform-version feature-gating
mismatch you hit if you point HashiCorp's `terraform-exec` at the `tofu` binary; a thin
`os/exec` wrapper over our small fixed command set is a dependency-free fallback), and
Tailscale `LocalClient` for WhoIs.

## Tailscale ACL

The policy gains a `group:admins`, a `tag:jauntd`, and SSH rules that make git-push
admin-only while leaving the engineer paths working. `git` is served **over Tailscale SSH**
(not a raw sshd), so Tailscale SSH's default-deny is the gate: an engineer has no rule
permitting SSH to `tag:jauntd`, so the push is refused before `git-receive-pack` runs.

```jsonc
{
  "groups": { "group:admins": ["tsultrim@wa-systems.eu"] },   // admin logins — bootstrap-owned
  "acls": [ {"action": "accept", "src": ["*"], "dst": ["*:*"]} ],
  "ssh": [
    { "action": "check", "src": ["autogroup:member"], "dst": ["autogroup:self"],
      "users": ["autogroup:nonroot", "root"] },
    { "action": "check", "src": ["autogroup:member"], "dst": ["tag:jaunt-tsultrim"],
      "users": ["autogroup:nonroot", "root"] },
    // admin-only git push (accept, not check, so non-interactive push isn't re-auth'd):
    { "action": "accept", "src": ["group:admins"], "dst": ["tag:jauntd"], "users": ["git"] },
    // admin maintenance shell on jauntd:
    { "action": "check", "src": ["group:admins"], "dst": ["tag:jauntd"],
      "users": ["autogroup:nonroot", "root"] }
  ],
  "nodeAttrs": [ {"target": ["autogroup:member"], "attr": ["funnel"]} ],
  "tagOwners": {
    "tag:jaunt-tsultrim": ["autogroup:admin"],
    "tag:jauntd":         ["autogroup:admin"]
  }
}
```

The `jaunt` API needs no ACL change (open `acls` already permit reachability; identity comes
from WhoIs). `group:admins` membership is admin-owned in this policy (bootstrap path), so no
engineer can self-promote.

## Security & invariants

- **No secrets on laptops.** Engineers need only tailnet membership; admins additionally need
  git-push rights (a `group:admins` entry). All real creds live on the jauntd box.
- **Engineer channel is capability-minimal by construction** — the `box` root has no durable
  resources, so `jaunt up/down` can only toggle a server. No cost-bomb via scaling, no rogue
  resources, even from a *tricked* (non-compromised) jauntd.
- **Structural changes are admin-only by channel** — only a `git push` (gated to
  `group:admins`) can change scale, add engineers, or add resource types.
- **Defense in depth on push** — the `post-receive` hook additionally calls Tailscale WhoIs
  and rejects any pusher not in the admin set, so an ACL misconfig still fails safe.
- **Engineers consume config; they never produce it.** A `jaunt up/down` reads the currently
  promoted config and toggles a server — it never writes jauntd's git checkout or triggers an
  `infra` apply. Only the admin `post-receive` hook mutates the checkout. So an engineer call
  cannot change config, only server existence.
- **`jaunt up` is idempotent by box state — it never modifies a *running* box.** If the box is
  already up, jauntd does **not** re-apply; it just reconnects. It runs `tofu apply` (box root,
  `box_on=true`) only when the box is down/absent. Consequently an admin config change (e.g. a
  plan bump) never disturbs a running session — it lands on the **next fresh start**, which,
  because boxes idle-off at EOD, is naturally the next morning's `up`-from-down. No surprise
  mid-day recreates.

## Risks / tradeoffs

- **jauntd is a single point of truth** (config + state + creds). Mitigation: it's tiny and
  cheap; snapshot its data volume; worst case, re-bootstrap from the fork and `git push` the
  config back.
- **jauntd holds full creds.** If the *box itself* is compromised, the blast radius is full
  (as with any runner holding creds — GH Actions secrets, a CI box, a laptop). Harden the
  jauntd box; keep its attack surface to the tailnet only.
- **`post-receive` applies all declared engineers' `infra` on push.** Idempotent but O(n) in
  engineers; optimize to changed-engineers-only if it becomes slow.

## Relationship to existing pieces

- **idle-watch / self-delete** (built) — unchanged; provides auto-off. `jauntd` provides on.
- **Tailscale identity persistence** (built this session) — the box reclaims its node key from
  the volume on recreate, so `jaunt up` after an idle-off keeps the stable `jauntbox-<user>`
  hostname (no `-1`).
- **Packer image (future, complementary):** baking the toolchain into the base image makes a
  cold `jaunt up` ~a minute instead of a multi-minute compile. Not required for this control
  plane, but it's what makes the daily up *feel* instant. Tracked separately.
- **Box sizing** is per-deployment config (default `cpx41` — sized for the df 11-service
  stack), set in the `infra`/`box` config, not a control-plane concern.

## Build sequence

Walking skeleton end-to-end first, then breadth:

1. **Root split** — refactor the current single Hetzner root into `infra` (durable) + `box`
   (server-only, reads `infra` outputs via `terraform_remote_state`). Prove `box_on` toggle
   works against pre-existing durable infra.
2. **jauntd box + service (single engineer)** — bootstrap tofu for the jauntd box; a minimal
   `jauntd` that authenticates via WhoIs and runs the `box` apply for the caller.
3. **`jaunt` CLI** — `jaunt` (up + auto-SSH), `jaunt down`, `jaunt status` against jauntd.
4. **git-remote config delivery** — bare repo on jauntd, Tailscale SSH `git` user, promoted
   ref, `post-receive` → `infra` apply. Apply the new ACL.
5. **Multi-engineer + admin ergonomics** — engineer registry, per-user state keys, per-engineer
   isolation ACL refinement (`group:eng-<user> → tag:jaunt-<user>`), `jaunt status` for admins.

Steps 1–3 are the walking skeleton (one engineer, up/down over the tailnet). The
implementation plan will decompose these into tasks.

## Open questions (resolve during planning)

- **`infra` apply trigger granularity** — all-engineers-on-push vs changed-only. Start with
  all (idempotent); optimize if needed.
- **jauntd API shape** — REST vs a tiny RPC; streaming apply progress vs poll. Lean REST +
  poll for the skeleton.
- **State backup cadence** — volume snapshot schedule for the jauntd box.
