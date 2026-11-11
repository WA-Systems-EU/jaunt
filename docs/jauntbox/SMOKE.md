# jauntbox live smoke test (opt-in, manual)

**Not run in CI.** This stands up a real, billed Hetzner server and uses a real
tailnet, so it only runs when you have real credentials and choose to run it.
Run it after any change that could affect provisioning, the supervisor or the
lifecycle limits. Never point it at a Hetzner project that holds other resources.

## Procedure

1. **Dedicated project and `jauntbox init`.** Create a new Hetzner project and an
   API token for it, and a Tailscale API key. Run `jauntbox init` (time it: target is
   one coffee, about 15 minutes including box boot) and accept the detected values.
   Then check that init picked sensible types: compare `jauntbox`'s config profiles
   against `hcloud server-type list` (x86/amd64, not deprecated, about 16/32/64 GB,
   available in `fsn1`). If they look wrong, fix `PickProfiles` and add a test.
   Record the init time here: _not yet timed_.

2. **Tailnet policy.** Paste the snippet that `jauntbox init` printed (or
   `jauntbox acl-snippet`) into Access controls in the Tailscale admin console, remove or
   narrow the default allow-all rule, and save.

3. **Bring the box up.** `jauntbox up small --wait`. Then:
   - `jauntbox ssh` works.
   - `systemctl list-timers` on the box shows `jauntbox-supervise` and
     `jauntbox-safety`.

   - `ps -o args -C tailscaled` on the box shows `--state=/home/.jaunt/tailscaled.state`
     (the volume path, not `/var/lib/tailscale`).

4. **Status.** `jauntbox status` shows the box, `on tailnet: true`, and the
   deadline and verdict from the supervisor.

5. **Self-deletion keeps the volume.** On the box set `max_uptime: 15m` by editing
   `/etc/jauntbox/box.json` (it is frozen there at `up`), disconnect, and wait.
   Timing: the supervisor keeps the box while a human was active within
   `human_active_window` (30m by default), and checks that before the night or
   deadline rules. So disconnect at least 30 minutes before the expected
   deletion, or lower `human_active_window` in `box.json` too. The box deletes
   itself; in the Hetzner console the volume `jauntbox-<user>-home`
   remains.

6. **Volume survives.** `jauntbox up` again: same hostname, and the `/home`
   content (put a marker file there in step 3) is intact.

7. **Night session.** `jauntbox night --until <HH:MM about 10 minutes from now>`,
   then on the box start a fake agent:
   `docker run -d --label jauntbox.agent=1 alpine sleep 3600`.
   Run this step after `evening_cutoff` (19:00), or lower `evening_cutoff` in
   `box.json`, and keep your sessions closed for `human_active_window` (or lower
   it), otherwise the human-active rule masks what you are testing.
   The box survives past the evening cutoff / idle limit while the agent runs and
   deletes itself at the night deadline. In plan 01 nothing launches agents for you,
   so `night` without a running agent means "delete as soon as I leave" (the
   `agents-done` verdict).

8. **Down.** `jauntbox down`: the box is gone within the grace period
   (`wrapup_grace` + 3 min). Also try `jauntbox resize big` once on a live box:
   old box deleted, new one created, `/home` intact, and the box's MagicDNS name
   is still `jauntbox-<user>` (no `-1` suffix: node identity survived).

9. **Safety net.** `jauntbox up` without `--wait`, kill the background push helper
   (`pkill -f internal-push`) immediately after `up` returns, and leave it. The box deletes itself roughly 45-55
   minutes later. **Confirm the volume is still there afterwards** (a safety-net
   deletion must delete only the server).

## Bare `jauntbox`: persistent herdr session

Run on a fresh box (no box existing) and on a second client. The same box must
have `herdr` installed by provisioning (`herdr --version` on the box).

1. `jauntbox` from the laptop: herdr opens with one workspace per repo in `~/src`.
2. Kill the Wi-Fi for 20 s: it reattaches by itself (you see "connection lost;
   reconnecting in 2s…").
3. Detach, then run `jauntbox` from the Lenovo: same session, same panes.
4. Type in a pane, then go idle: `jauntbox status` shows human activity, then
   none after 30 min. **If typing in herdr panes does not register through
   `/dev/pts` atime, stop and report:** `HumanIdle` needs a herdr-aware signal
   (for example herdr's API reporting attached clients and last input).
5. Unverified herdr facts to confirm on the box: `HERDR_SESSION=jauntbox herdr
   server` starts a headless server for the named session (the start command in
   `internal/session/herdr.go`); `herdr --remote` exits with a status other than
   255 when the connection drops (if so, add it to the reconnect rule). Note
   the exit status you actually see when the link drops (record it here).
6. From inside a local herdr pane (so `HERDR_SESSION`, `HERDR_SOCKET_PATH` and
   `HERDR_ENV` are set): bare `jauntbox` must refuse with a hint to use a plain
   terminal or `--nested`, without touching the box. `jauntbox --nested` must
   attach to the box's `jauntbox` session (not the local one), printing the
   "inside a local herdr pane" notice.
7. Fresh box: bare `jauntbox` waits through first-boot provisioning ("provisioning
   jauntbox-<user>…") and then attaches without manual steps.

## Unverified API facts: check explicitly

These two were written from documentation and have never been run live. Record
what you see; if either is false, fix the code and add a test.

- **Tailscale `connectedToControl`.** Does `GET /api/v2/tailnet/-/devices`
  include `connectedToControl` by default (no `fields=all`)? Check with
  `curl -s -u "$TAILSCALE_API_KEY:" https://api.tailscale.com/api/v2/tailnet/-/devices | jq '.devices[0]'`.
  If the field is missing, `DeviceOnline` silently falls back to `lastSeen`;
  decide whether that is accurate enough or request `?fields=all`.
- **Hetzner firewall attachment.** The firewall is attached both at server create
  (by id) and through the label selector `jauntbox=<user>,role=box`. Does Hetzner
  accept both without error, and does the server show the firewall exactly once
  (`hcloud server describe jauntbox-<user>`, or the console's Firewalls tab)?

## Plan 02: forge and secrets

Needs a scratch GitHub repo (with a few commits) in the org from `github.org`
(`WA-Systems-EU` for the maintainer). Replace `o/r` with that repo and
`<default branch>` with its real default branch name.

After upgrading jauntbox, run `jauntbox` or `jauntbox up` once against an existing
box before `jauntbox down`, so the box gets `jauntctl`.

1. **App setup.** `jauntbox setup github` creates `jauntbox-<user>` under
   `WA-Systems-EU`. Install it on the scratch repo only. The ruleset
   `jauntbox: humans merge the default branch` is visible in the repo's settings.
   Record: whether GitHub accepted the manifest as written (`hook_attributes`), or
   what it rejected.
2. **Push and merge rules.** On the box, `jaunt-repos` lists the scratch repo with its
   real default branch and clones it. `git push origin HEAD:agent/smoke` works;
   `git push origin HEAD:<default branch>` is **rejected** by the ruleset. Open a PR
   from `agent/smoke` and merge it in the web UI as the human: it **works** (admin
   bypass). Record the rejection message and anything unexpected in the listing.
3. **Locked after reboot.** `sudo reboot` the box. Reconnect with plain
   `ssh <user>@jauntbox-<user>`, not `jauntbox ssh`, bare `jauntbox` or `jauntbox
   status`: those unlock the box automatically first. Then `git push` prints the
   locked message. Run `jauntbox unlock` on the laptop and push again: it works.
   Record both outputs.
4. **Permission drift.** Add the `workflows` permission by hand in the App settings.
   `jauntbox setup github --check` reports the drift. Minted tokens still lack
   `workflows`: `curl -H "Authorization: token $(sudo jauntctl mint o/r | jq -r .token)" https://api.github.com/installation/repositories`
   works, and editing `.github/workflows/x.yml` and pushing is rejected. Record the
   check output and the push rejection. Remove the permission afterwards.
5. **Revocation on down.** Note a minted token (step 4), then `jauntbox down`. The
   events in `/home/.jauntbox/events.jsonl` on the volume (root-only: `sudo grep`; read them from the next
   box, or by attaching the volume) show `token.revoked` for the tokens minted since
   the step-3 reboot, and the old token returns 401 from the GitHub API. Tokens
   minted before the reboot lived in the tmpfs cache, which the reboot wiped; they
   are not revoked and simply expire. Cache hits emit no `token.minted`, so compare
   the `token.revoked` count against the `token.minted` events since the last
   reboot. Record both counts and the HTTP status.
6. **Nothing on the volume.** `ls /home` and `grep -r ghs_ /home` find no token or
   key. Record the output (expected: nothing found).

## Plan 03a checks (privilege split)

Run on a box created after Plan 03a. Update the tailnet policy first, and set
`BOX_SUDO_PASSWORD` (`jauntbox secrets set BOX_SUDO_PASSWORD`). Fill in the result
column.

| Check | Result |
|---|---|
| 1. Policy updated with `jauntbox acl-snippet` (includes `root`) | |
| 2. `jauntbox up` works over root ssh | |
| 3. As the box user, `sudo -n true` fails | |
| 4. `sudo -n jauntctl repos` works | |
| 5. `sudo -n jauntctl stop` is refused | |
| 6. `sudo true` asks for a password and accepts `BOX_SUDO_PASSWORD` | |
| 7. `docker info` shows `rootless` | |
| 8. `docker run --rm hello-world` works | |
| 9. A small `docker compose up` works | |
| 10. `docker run --rm -v /:/host alpine cat /host/etc/shadow` is denied | |
| 11. `docker run --rm alpine wget -T 5 -qO- http://169.254.169.254/hetzner/v1/userdata` fails (metadata from a rootless container) | |
| 12. `sudo docker ps` reaches the root daemon | |
| 13. `curl -m 5 http://169.254.169.254/hetzner/v1/userdata` fails as the box user and works with `sudo` | |
| 14. The supervisor still deletes the box at `down` | |
| 15. After a reboot the metadata rule is back (`sudo iptables -S OUTPUT`) | |
| 16. With root ssh refused by the policy, `jauntbox down` stops with the fix and suggests `--force` | |
| 17. Provisioning log has no `WARNING: rootless docker setup failed` | |
| 18. `id` as the box user shows no `docker` group | |
| 19. `docker -H unix:///var/run/docker.sock ps` is denied | |
| 20. With root refused in the policy, `jauntbox` on a new box fails within a minute and names `jauntbox acl-snippet` | |

## What "good" looks like

- `up` returns quickly; the box joins the tailnet within a few minutes.
- The box deletes itself by every route (limit, `down`, safety net); the volume
  is never deleted.
- Nothing is left billing: after the test, the Hetzner project shows no server
  (the volume and firewall/key may remain, delete them when fully done).

## Results log

### 2026-11-10: first live run (branch `feat/remote-workspace`)

Ubuntu 26.04 stock image, `fsn1`, dedicated Hetzner project.

| Check | Result |
|---|---|
| 1. `init` | Pass: fast, well within the ~15 min target (exact time not measured). |
| 2. Tailnet policy | Not recorded. |
| 3. `up --wait`, `ssh`, timers, tailscaled state on the volume | Pass. |
| 4. `status` | Pass. |
| 5. Self-delete at `max_uptime`, volume kept | Pass. |
| 6. Volume survives `up` again (same hostname, marker intact, `box.json` back to defaults) | Pass. |
| 7. Night session: kept while the agent ran, wrap-up at the deadline (`night-deadline`), deleted after `wrapup_grace` (`wrapup-grace-elapsed`), volume kept | Pass. |
| 8. `resize` both ways | **Failed first** with Hetzner `volume already attached`: `Down` returned once the server was gone, before Hetzner reported the volume detached. Fixed in `67d08f1` (`Up` waits for the detach). After the fix: pass, about 2 minutes per resize, marker intact, no `-1` hostname. |
| 8. `down` | Pass. |
| 9. Safety net | **Not run live.** Covered offline by `safety-net_test.sh` only. |
| herdr 1. bare `jauntbox` attaches | Pass (from a plain ssh session on the Linux box). |
| herdr 2. Wi-Fi drop reconnect, `herdr --remote` exit status | Not run yet (needs the binary on the laptop). |
| herdr 3. second client, same session | Not run yet. |
| herdr 4. typing registers via `/dev/pts` atime | Not checked yet. |
| herdr 6. inside a local herdr pane | **Failed first** with herdr's "nested herdr is disabled". Fixed in `b787e4b`: refuses by default with a hint; `--nested` attaches. Pass after the fix. |
| herdr 7. fresh-box provisioning wait | Not run yet. |
| Tailscale `connectedToControl` in default `/devices` | Present (`true`); no `fields=all` needed. |
| Hetzner firewall attached by id and by label selector | Accepted; listed exactly once on the server. |

Shell history (`~/.zsh_history`) survives server changes.

### 2026-11-10: Plan 02 checks (forge and secrets)

App owner `WA-Systems-EU` (GitHub Free), installed on the private repo `WA-Systems-EU/thomas`.

| Check | Result |
|---|---|
| Unlock on `up` | **Failed first**: "could not unlock the box", because no secrets were stored on the laptop yet (setup not run). Expected; the warning now names the cause. |
| 1. App setup | **Three issues, all fixed.** The manifest flow only listened on loopback, unreachable from a browser on another device: `--listen tailnet` added (`5db48db`). With `github.org` unset the App was created under the personal account and could not be installed in the org: setup now asks for the owner (`3e659ca`). Rulesets returned 403 "Upgrade to GitHub Pro" on a private repo on GitHub Free: setup now names unprotectable repos and stops unless `--allow-unprotected` / `github.allow_unprotected` (`d0c2902`). After the fixes: App created under the org and installed; **no ruleset** on `thomas` (Free plan). Manifest accepted as written. |
| 2. Push and merge rules | Not recorded yet. The default-branch rejection cannot be tested on `thomas` (unprotected); needs a public scratch repo or a paid plan. |
| 3. Locked after reboot | Pass. |
| 4. Permission drift | Pass: `setup github --check` reported the added `workflows` permission. Workflow-file push rejection not recorded. |
| 5. Revocation on down | Pass: 2 `token.minted` since the step-3 reboot, 2 `token.revoked` just before the delete; the pre-reboot token was not revoked (cache wiped, expires on its own), as expected. Old token: HTTP 401. Found along the way: one NUL-filled line in `events.jsonl` and no `box.deleted` events, because `box.deleted` was written after the self-delete call; fixed in `26996b0` (written and synced first, every event fsynced). Read the log with `grep -a`. |
| 6. Nothing on the volume | Pass: no `ghs_` token and no private key under `/home`. |
| `box.deleted` recorded by a fixed box (`26996b0`) | Not recorded yet. |

### 2026-11-11: Plan 03a checks (privilege split)

Box created from `06b5592`, `small` profile.

| Check | Result |
|---|---|
| `up` before the fix | **Failed first** with Hetzner "invalid input in field 'user_data'": the rendered user-data was 35 KB, over Hetzner's 32 KiB limit. Fixed in `06b5592` (cloud-init files gzipped, about 18 KB now, size test added). |
| 20. New box with root refused by the policy | Pass: the background push stopped after about a minute and named `jauntbox acl-snippet`. |
| 1. Policy updated with `acl-snippet` (includes `root`) | Pass. |
| 2. Install over root ssh | Pass after the policy update (`jauntbox internal-push` on the running box). |
| 3–6. Sudo split | Pass. |
| 7–12. Rootless docker, `docker run`, `docker compose up`, host mount and metadata denied, root daemon via `sudo` | Pass. |
| 13. Metadata blocked for the user, reachable with `sudo` | Pass. |
| 15. Metadata rule back after a reboot | Pass. |
| 17. No rootless docker warning in the provisioning log | Pass. |
| 18. No `docker` group | Pass. |
| 19. Root daemon socket denied | Pass. |
| 14. Supervisor deletes the box at `down` | Not run (unchanged by 03a apart from root ssh; skipped). |
| 16. `down` with root refused | Not run (skipped). |
