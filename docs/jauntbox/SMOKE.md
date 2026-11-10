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

## What "good" looks like

- `up` returns quickly; the box joins the tailnet within a few minutes.
- The box deletes itself by every route (limit, `down`, safety net); the volume
  is never deleted.
- Nothing is left billing: after the test, the Hetzner project shows no server
  (the volume and firewall/key may remain, delete them when fully done).
