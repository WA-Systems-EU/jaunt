# Stable Tailscale node identity across box replacement

**Date:** 2026-07-24
**Status:** Design — approved for planning
**Scope:** `tofu/modules/jauntbox` cloud-init only. No root or variable changes.

## Problem

Each jauntbox joins Tailscale at boot with `tailscale up --hostname=jauntbox-<user>`
using an **ephemeral** auth key. The node's identity (its node key) lives in
`/var/lib/tailscale/tailscaled.state`, which sits on the **ephemeral root disk** and is
destroyed whenever the server is recreated. The persistent volume only carries `/home`.

So on replacement the new box registers as a **brand-new node** requesting a hostname that
the old (not-yet-garbage-collected) node still holds. Tailscale resolves the collision by
appending a numeric suffix — the MagicDNS name becomes `jauntbox-<user>-1` — which breaks
the project's promise of a *stable* hostname.

Ephemeral nodes self-clean, but only ~30 min after going offline. The two replacement paths
we hit are both fast and tofu-driven:

- **`box_on` off → on toggle** — server destroyed + recreated, volume kept.
- **`tofu apply` recreate** — image / plan / user-data change forces destroy + recreate.

In both, the old server is **hard-deleted** (no graceful shutdown), so any teardown hook on
the old box cannot run. The fix must happen on the way **up**, or avoid changing identity at
all.

## Approach

**Relocate `tailscaled`'s state onto the persistent volume** so a replacement box comes up as
the *same* Tailscale node, reclaims the same MagicDNS name, and never triggers dedup.

The volume is mounted at `/home` earlier in cloud-init (before Tailscale is installed), so a
volume-backed path is available when `tailscaled` first starts.

### Mechanism

Relocate only the **state file**, via a systemd drop-in — **not** by symlinking
`/var/lib/tailscale`.

> **Why not a symlink.** The packaged `tailscaled.service` declares `StateDirectory=tailscale`.
> systemd refuses to set up a `StateDirectory` that is a symlink and fails the unit with
> `Failed to set up special execution directory in /var/lib: Too many levels of symbolic links`,
> so `tailscaled` never starts and the box never joins. (Confirmed on a real box.)

Instead, leave `/var/lib/tailscale` as the real directory systemd owns, and point
`tailscaled`'s `--state` at a file on the volume. The packaged `ExecStart` ends with `$FLAGS`,
and `tailscaled`'s flag parser takes the **last** `--state`, so appending
`--state=/home/.jaunt/tailscaled.state` via `$FLAGS` overrides the packaged
`--state=/var/lib/tailscale/tailscaled.state` without hardcoding (and thus without drifting
from) the packaged command line.

**1. `write_files` drop-in** (config stage → on disk before the package auto-starts the daemon):

```
- path: /etc/systemd/system/tailscaled.service.d/10-jaunt-state.conf
  permissions: '0644'
  content: |
    [Unit]
    RequiresMountsFor=/home
    [Service]
    Environment=FLAGS=--state=/home/.jaunt/tailscaled.state
    ProtectHome=no
```

- `RequiresMountsFor=/home` — on a later reboot, order `tailscaled` after the volume mount.
- `ProtectHome=no` — defensive; guarantees the service can write under `/home` even if a
  future packaged unit hardens it (a no-op against today's unit).

**2. `runcmd` bring-up:**

```
- curl -fsSL https://tailscale.com/install.sh | sh
- mkdir -p /home/.jaunt && chmod 700 /home/.jaunt
- systemctl daemon-reload
- systemctl enable tailscaled
- systemctl restart tailscaled   # restart so the drop-in applies even if install auto-started it
- for i in $(seq 1 30); do [ -S /var/run/tailscale/tailscaled.sock ] && break; sleep 1; done
- tailscale up --auth-key="$(cat /etc/jaunt/ts.key)" --hostname=${hostname_prefix}-${user} --advertise-tags=${tailscale_tag} --ssh ${tailscale_extra_args} && shred -u /etc/jaunt/ts.key
```

The shred is chained with `&&` so it runs **only on a successful join** — if `tailscale up`
fails, the key is left in place for a manual retry. cloud-init re-writes `/etc/jaunt/ts.key`
fresh on the next *instance's* first boot, so the slow-replace fallback (re-auth via the key
when the old node was already GC'd) still works; the key is simply absent during the box's
*running* life.

> **Reboot vs. replacement.** cloud-init `runcmd`/`write_files` run once per instance (first
> boot), so the shred is safe across a plain **reboot**: the state file persists on the volume
> and `tailscaled` re-reads it (no auth key needed). A **replacement** is a new instance, so
> cloud-init re-runs fully and re-supplies the key.

### Why this covers both timing cases

- **Fast replace (old node still present):** the persisted node key reconnects as the *same*
  node — the "old" entry simply comes back online with its existing name. No collision, no
  suffix.
- **Slow replace (old node already GC'd):** the persisted node key is invalid server-side, so
  `tailscaled` falls back to the ephemeral auth key and registers fresh — but the old node is
  already gone, so there is still no name collision.

Either way, no `-1`.

## What stays the same

- **Ephemeral auth key type is unchanged** (reusable, preauthorized, ephemeral,
  `tag:jaunt-<user>`). It remains the safety net for genuine teardown: when a box is destroyed
  *and its volume deleted*, the state is gone and the offline ephemeral node is GC'd normally.
  We are not making nodes non-ephemeral. What changes is only *how long it sits on the box* —
  see the shred above.
- No changes to `tofu/hetzner` roots, variables, or the ephemeral `tailscale_tailnet_key`
  resource. This is contained entirely in the shared module's cloud-init.
- No new credentials on the box (the whole point of choosing this over an API-reap approach).

## Security notes

This change is **credential-neutral, then net-positive**:

- **No new secret.** `tailscaled.state` already exists on every box today (on the root disk);
  we only relocate it to the volume at the same `root:root 0700` protection. The engineer user
  (uid 2000, home under `/home/<user>`) cannot read it even though it shares the volume.
- **Two secrets, very different blast radii.** The **reusable auth key** (`/etc/jaunt/ts.key`)
  is the "mint arbitrary new nodes" credential — the real *add-their-own-box* risk. The
  **node state** (`tailscaled.state`) is this one node's identity: stealing it impersonates
  *this* node (ACL-scoped to the user's own box, and re-adds are visible in the admin console),
  it does **not** let an attacker register new nodes.
- **Net improvement over today.** Persisting identity lets us shred the dangerous one:
  `/etc/jaunt/ts.key` is destroyed immediately after a successful join, so during the box's
  running life — when a compromise would occur — there is **no mint-new-nodes credential on
  disk**, only the single-node identity.

### Considered and deferred

- **Single-use auth key** (instead of reusable) would further cut blast radius, but it is
  consumed on first join, so the slow-replace fallback (GC'd node re-authing via the key)
  would fail unless the key is regenerated per apply. Out of scope here.
- **Scoped OAuth client** — more moving parts than warranted for this fix.

## Edge cases

- **Volume recreate** (e.g. `volume_size` change) discards the state — but `/home` is discarded
  too, so it is a genuinely fresh box; acceptable.
- **Bonus:** the idle self-delete → later re-apply recovery path is also fixed for free, since
  `self-delete.sh` deletes only the server and leaves the volume (and thus the state) intact.

## Verification

Manual, against a real box (no unit test is meaningful for cloud-init identity):

0. **Fresh box first:** apply with `box_on=true` on a clean slate and confirm the box actually
   joins — `systemctl is-active tailscaled` is `active`, and `jauntbox-<user>` appears in
   `tailscale status`. (This is the regression the symlink approach caused.)
1. Record MagicDNS name and `tailscale status --json | jq -r .Self.ID`.
2. Toggle `box_on=false` then `box_on=true` (and separately: force a recreate via a user-data
   change) and re-apply.
3. Confirm the box rejoins as **`jauntbox-<user>`** with **no `-1`**, and that `.Self.ID` matches
   the value from step 1 (identity reclaimed) for the fast-replace case.
4. Confirm `/var/lib/tailscale` is a real directory (not a symlink), the drop-in
   `systemctl cat tailscaled` shows `Environment=FLAGS=--state=/home/.jaunt/tailscaled.state`,
   and `/home/.jaunt/tailscaled.state` exists on the volume.
5. Confirm the Tailscale admin console shows a **single** node for the user, not a stale pair.
6. Confirm `/etc/jaunt/ts.key` is **absent** on a running box (shredded post-join), and present
   again only transiently at boot.
