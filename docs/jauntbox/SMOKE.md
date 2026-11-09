# jauntbox live smoke test (opt-in, manual)

**Not run in CI.** This stands up a real, billed Hetzner server and uses a real
tailnet, so it only runs when you have real credentials and choose to run it.
Run it after any change that could affect provisioning, the supervisor or the
lifecycle limits. Never point it at a Hetzner project that holds other resources.

## Procedure

1. **Dedicated project and real server types.** Create a new Hetzner project and
   an API token for it; export `HCLOUD_TOKEN=...` and `TAILSCALE_API_KEY=...`.
   Run `hcloud server-type list` and **replace the placeholder profile types** in
   `config.Default()` and `examples/jauntbox.yaml` with real x86 (amd64) types for
   about 16/32/64 GB in `fsn1`. Commit that change. Create your config from
   `examples/jauntbox.yaml`.

2. **Tailnet policy.** Paste the output of `jauntbox acl-snippet` into the
   tailnet policy file and save it.

3. **Bring the box up.** `jauntbox up small --wait`. Then:
   - `jauntbox ssh` works.
   - `systemctl list-timers` on the box shows `jauntbox-supervise` and
     `jauntbox-safety`.

4. **Status.** `jauntbox status` shows the box, `on tailnet: true`, and the
   deadline and verdict from the supervisor.

5. **Self-deletion keeps the volume.** On the box set `max_uptime: 15m` by editing
   `/etc/jauntbox/box.json` (it is frozen there at `up`), disconnect, and wait.
   The box deletes itself; in the Hetzner console the volume `jauntbox-<user>-home`
   remains.

6. **Volume survives.** `jauntbox up` again: same hostname, and the `/home`
   content (put a marker file there in step 3) is intact.

7. **Night session.** `jauntbox night --until <HH:MM about 10 minutes from now>`,
   then on the box start a fake agent:
   `docker run -d --label jauntbox.agent=1 alpine sleep 3600`.
   The box survives past the evening cutoff / idle limit while the agent runs and
   deletes itself at the night deadline.

8. **Down.** `jauntbox down`: the box is gone within the grace period
   (`wrapup_grace` + 3 min). Also try `jauntbox resize big` once on a live box:
   old box deleted, new one created, `/home` intact.

9. **Safety net.** `jauntbox up` without `--wait`, kill the background push helper
   (`pkill -f internal-push`), and leave it. The box deletes itself roughly 45-55
   minutes later. **Confirm the volume is still there afterwards** (a safety-net
   deletion must delete only the server).

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
