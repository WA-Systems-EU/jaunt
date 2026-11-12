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

3. **Bring the box up.** `jauntbox up small` (it stays in the foreground until the box is ready). Then:
   - `jauntbox ssh` works.
   - `systemctl list-timers` on the box shows `jauntbox-supervise` and
     `jauntbox-safety`.

   - `ps -o args -C tailscaled` on the box shows `--state=/home/.jaunt/tailscaled.state`
     (the volume path, not `/var/lib/tailscale`).

4. **Status.** `jauntbox status` shows the box, the tailnet mark and the
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

8. **Down.** `jauntbox down`: it prints how long it waits (`wrapup_grace` + 14 min:
   one supervise timer interval, a whole supervisor pass, a margin) and the box is
   gone within that; with no agents running it goes much faster. Also try `jauntbox resize big` once on a live box:
   old box deleted, new one created, `/home` intact, and the box's MagicDNS name
   is still `jauntbox-<user>` (no `-1` suffix: node identity survived).

9. **Safety net.** `jauntbox up --detach`, kill the background push helper
   (`pkill -f internal-push`) immediately after `up` returns, and leave it. The box deletes itself roughly 45-55
   minutes later. **Confirm the volume is still there afterwards** (a safety-net
   deletion must delete only the server).

## Bare `jauntbox`: persistent herdr session

Run on a fresh box (no box existing) and on a second client. The same box must
have `herdr` installed by provisioning (`herdr --version` on the box).

1. `jauntbox` from the laptop: herdr opens with one workspace per repo in `~/src`.
2. Kill the Wi-Fi for 20 s: it reattaches by itself (you see "connection lost;
   reconnecting in 2s…").
3. Detach, then run `jauntbox` from a second machine: same session, same panes.
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

## Plan 03: agent containers

Needs a box with the repo's App installed, `CLAUDE_CODE_OAUTH_TOKEN` stored
(`claude setup-token`, then `jauntbox secrets set` and `jauntbox unlock`), and
`user.name` / `user.email` set on the box. Run from `~/src/<repo>` in a herdr pane
unless a row says otherwise. Fill in the result column.

| Check | Result |
|---|---|
| 1. First `jauntctl agent`: record the image build time; `docker image ls jauntbox/agent` shows `jauntbox/agent:<12 hex>` (use `sudo docker image ls`) | |
| 2. In the image, as the non-root uid: `claude --version` works (the Claude Code installer layout was assumed, not built) and `mise --version` works | |
| 3. From `jauntctl agent attach <id> --shell`: `id -u` is your uid | |
| 4. `echo x >> .git/config`, `touch .git/hooks/x` and `rm .git/commondir` all fail (read-only) | |
| 4b. `mv .git .git2` and `rmdir .git` both fail (`.git` is a mount point: busy) | |
| 5. `curl -m 5 http://169.254.169.254/hetzner/v1/metadata` fails | |
| 6. `curl -m 5 http://100.100.100.100` fails | |
| 6b. `curl -m 5 http://<box tailscale IP>:22` and `curl -m 5 http://<bridge gateway>:22` (`ip route` shows the gateway) both time out; from the box, `curl <container IP>:<port>` of a server started in the container still works | |
| 7. `ls /run/jauntbox/secrets` fails | |
| 8. `cat /run/jauntbox-run/token` shows only a token | |
| 9. `git push -u origin HEAD` on the agent branch reports the config write error (read-only `.git/config`) but the push lands | |
| 10. Pushing to another installed repo with that token fails (403) | |
| 11. `sudo ip6tables -S DOCKER-USER` on the box exists and holds the tailnet IPv6 rule (`fd7a:115c:a1e0::/48`); `sudo iptables -S DOCKER-USER` holds the two IPv4 rules; `sudo iptables -S INPUT` and `sudo ip6tables -S INPUT` start with the `docker0` and `br-+` ESTABLISHED,RELATED ACCEPT, each above its DROP | |
| 12. Interactive: `jauntbox`, then in the repo's herdr pane `jauntctl agent`: Claude opens in that pane. Record what Claude asked on the very first run (onboarding) | |
| 13. A second agent in another repo's pane at the same time: both run; `jauntctl agent` again in the first checkout refuses and names the run | |
| 14. Close the pane; in another pane `jauntctl agent attach <id>`: Claude continues (`--continue`). Record whether herdr shows the pane as an agent | |
| 15. Wrap-up: `jauntctl agent stop <id>` during an interactive task: Claude receives the instruction, writes, commits and pushes `CONTINUATION.md`. Record the hook output seen in the session | |
| 16. Fallback: `jauntctl agent stop <id>` with Claude idle: after the grace, a mechanical `CONTINUATION.md` is pushed | |
| 17. Unattended with `--max 10m`: `jauntctl agent --unattended "a prompt with spaces" --max 10m` works through the sudoers rule, `logs --follow` shows output, the run wraps up at 10 min; `agent.*` events in `events.jsonl` | |
| 18. `jauntctl agent logs <id>` shows a finished run; with no id it shows the newest run of the checkout | |
| 19. Resume: `jauntctl agent --branch <that branch>` in the same checkout opens with the resume prompt | |
| 20. On the host, `git status` and `git log` in the checkout work normally after the run, and `cat .git/commondir` shows `.` | |
| 21. Teardown with a running agent: `jauntbox down`: fallback pushed, tokens revoked, box deleted; event order in the next box's log | |
| 22. Cancel: with two runs, `jauntctl agent stop <id>` on one; leave the box idle until it starts a wrap-up (`wrapup.started`, and `agent.wrapup_started` with `box-idle` for the other run in `events.jsonl`), then type in a pane: the box keeps (`wrapup.cancelled`), the other run is no longer `wrapping-up` in `jauntctl agent list`, the stopped run still is | |

## Plan 04 checks (your services, for agents)

Needs a box made with `agent.host_ports: [5432]`, and in some repo a compose file that
publishes Postgres on `5432:5432` and a second service on `6379:6379`, started with
`docker compose up -d` (rootless). Run from `~/src/<repo>` in a herdr pane unless a row
says otherwise. Fill in the result column.

| Check | Result |
|---|---|
| 1. `sudo iptables -S INPUT` shows, for `br-+`: the ESTABLISHED,RELATED ACCEPT, `-j JAUNTBOX-HOST`, `-j JAUNTBOX-RUNS`, then the DROP, in that order; `sudo iptables -S JAUNTBOX-HOST` holds `--dport 5432`; `sudo ip6tables -S` has no `JAUNTBOX-*` chain | |
| 2. From `jauntctl agent attach <id> --shell`: `getent hosts host.jauntbox` prints an address (record it and the container's `ip route`: is it `docker0`'s `172.17.0.1`?) | |
| 3. `psql postgres://...@host.jauntbox:5432/...` (or `nc -zv host.jauntbox 5432`) connects; `nc -zv -w 5 host.jauntbox 6379` times out (unlisted); `nc -zv -w 5 host.jauntbox 22` and `nc -zv -w 5 <box tailscale IP> 22` time out; `jauntctl agent --host-port 22` is refused with the SSH-port message | |
| 4. `nc -zv -w 5 <box tailscale IP> 5432` connects (the rules match the port, not the address: record it) | |
| 5. Per run: `jauntctl agent --host-port 6379` in a second checkout: 6379 connects from that run; from the first run it still times out; `jauntctl agent list` shows `5432,6379` for the second run and `5432` for the first | |
| 6. `jauntctl agent stop <second run> --now`: `sudo iptables -S JAUNTBOX-RUNS` no longer has the run's `br-jb...` rule; 5432 still works from the first run | |
| 7. Republish Postgres as `127.0.0.1:5432:5432`, then `jauntctl agent` in another checkout: the loopback warning is printed and the agent can't connect (record whether the warning is visible for an interactive start too, where Claude's screen opens right after, as well as for `--unattended`); restore `5432:5432`. With the service stopped, the nothing-listens warning is printed | |
| 8. From the laptop: `psql -h jauntbox-<user> -p 5432 ...` still connects while agents run | |
| 9. `sudo systemctl restart docker` with a run active: the run reads `gone` / finished in `jauntctl agent list`, its `JAUNTBOX-RUNS` rules are gone, and a new `jauntctl agent` reaches 5432. After `sudo reboot`, `sudo iptables -S INPUT` again shows row 1's order before any agent start | |

## Plan 06 checks (terminal UI)

Run in a real terminal (stderr is a terminal) unless a row says otherwise. Fill in the
result column.

| Check | Result |
|---|---|
| 1. `jauntbox up small` on a new box shows each step (Policy and limits, SSH key, firewall, volume, Server created, Joined the tailnet, Box reachable, Install supervisor, Unlock secrets, Dev tools installed) with its time, and ends on `Ready in …` with the deletion time and the "Open it with jauntbox, or ssh jauntbox-<user>." line | |
| 2. `jauntbox up small \| cat` (box already up, then on a new box) prints plain lines only: no escape bytes, no spinner, one line per finished step | |
| 3. Ctrl-C during Joined the tailnet or Box reachable (after Server created) prints the note naming `push.log`; `jauntbox status` a few minutes later shows the box ready. A second Ctrl-C during the foreground wait quits | |
| 4. `jauntbox down` shows its steps and ends cleanly; `jauntbox down` again says "no box running" | |
| 5. `jauntbox resize big` shows its steps and returns only when the new box is ready | |
| 6. `jauntbox status` prints the short summary (box, state, server type, created, tailnet, secrets, verdict and deadline); `jauntbox status --json \| jq .` parses | |
| 7. `jauntbox init --config /tmp/jb-form/config.yaml` walks the form (arrow keys, Enter to go on, `/` to filter timezones, token checks, locations, server types with prices, review screen) and writes the config, env file and ssh block you chose; Ctrl-C in the form writes nothing | |
| 8. `jauntbox init --plain --config /tmp/jb-plain/config.yaml` in a terminal asks line by line; answer the same as row 7 (separate folders keep the two env files apart; the ssh block already exists on this second run, so skip it) and compare `config.yaml` and `env` only with the form's | |
| 9. Bare `jauntbox` with no box shows the same steps as `up`, then attaches the herdr session | |
| 10. `NO_COLOR=1 jauntbox status` and `jauntbox --plain up` print no colour or animation | |
| 11. `jauntbox up --detach` returns once the server exists; `jauntbox status` later shows the box ready | |

## What "good" looks like

- `up` shows each step and ends on Ready within a few minutes.
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

App owner `WA-Systems-EU` (GitHub Free), installed on one private repo.

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

### 2026-11-11: Plan 03 checks (agent containers, first pass)

Box created from `ddfd650`.

| Check | Result |
|---|---|
| 1, 2, 12. First `jauntctl agent`: image build, `claude` and `mise` as the non-root uid, Claude opens in the pane | Pass (build time and onboarding prompts not recorded). |
| 3–10, 4b, 6b. Isolation from `attach --shell`: uid, read-only git files, `.git` busy, metadata/tailnet/host blocked, no vault, token only, push lands | Pass. |
| 11. DOCKER-USER (IPv4 and IPv6) and INPUT rules, ACCEPT above DROP | Pass. Note: our INPUT rules sit above `-j ts-input`; a tailscaled restart puts its jump first again (harmless for containers). |
| 13–22. Run handling, wrap-up, fallback, unattended, resume, teardown, cancel | Not run yet. |
