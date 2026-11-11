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
jauntbox down                       # wrap up and delete the box (the volume stays; waits up to wrapup_grace + 14 min)
```

Live smoke checks and their results: [`docs/jauntbox/SMOKE.md`](docs/jauntbox/SMOKE.md).

### Your editor and your services

- **VS Code Remote-SSH** (or any ssh-based remote editor) works as is: connect to
  `<user>@jauntbox-<user>` over Tailscale SSH and open a folder under `~/src`. Agents still
  start with `jauntctl agent` from a terminal (the editor's terminal is fine).
- **Your services from the laptop:** what you publish with `docker compose` or
  `docker run -p` on the box is reachable at `http://jauntbox-<user>:<port>` over the
  tailnet (database clients, the app in your browser), never from the internet.
- **Agents' own servers** aren't published: reach them from the box by the container IP
  (`jauntctl agent list`), or from the laptop with `ssh -N -L 8080:<run IP>:8080 <user>@jauntbox-<user>`.

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
  `jauntbox-<user>`. It first asks which account owns the App: your personal account or
  one of your organizations (default `github.org`; `--owner <login>` skips the
  question). The App is private, so only its owner can install it. It needs an admin credential once: `gh auth token`, or `GITHUB_ADMIN_TOKEN`
  when `gh` is absent. That credential is used on the laptop only; it is never stored
  and never sent to the box. Install the App on the repos you want agents to touch.
  If your browser is on another device than the one running jauntbox, add
  `--listen tailnet`: the callback server binds to this machine's Tailscale IP (or pass
  `--listen <ip>[:port]`) and prints the URL to open instead of opening a browser.
  Wildcard addresses such as `0.0.0.0` are refused.
- The App has `contents: write`, `pull_requests: write` and `metadata: read`. Nothing
  else: no `workflows`, no `administration`, no `secrets`.
- Rulesets: on each repo the default branch (plus any `github.protected_patterns`
  entries in your config) can only be updated or merged by repo admins, which is you.
  The App never bypasses the ruleset, so agents push branches and open PRs; you merge.
  GitHub only runs rulesets on private repos for paid plans (Team for an organization,
  Pro for a personal account). On GitHub Free, `setup github` protects what it can,
  names the private repos it could not protect, and stops. `--allow-unprotected` (one
  run) or `github.allow_unprotected: true` accepts that agents can push to those
  repos' default branch; `setup github --check` keeps flagging them with ⚠.
- On the box, `git` uses a credential helper that mints a token for one repository,
  valid for an hour and cached until 10 minutes before it expires. `jaunt-repos`
  lists the repos the App is installed on and clones them; it warns if it finds an old
  `gh auth login`, which you should remove.
- **Adding repos later.** The App works for any repo you install it on, including ones
  you create after setup:
  1. On GitHub, open the settings of the account or organization that owns the App,
     then **GitHub Apps** → **Configure** next to `jauntbox-<user>`, and add the repo under
     *Repository access* (or pick *All repositories* to include future repos automatically).
  2. Run `jauntbox setup github` again. It skips what already exists and adds the
     default-branch ruleset to every installed repo; without it, agents can push to the new
     repo's default branch. `jauntbox setup github --check` lists installed repos whose
     ruleset is missing or has drifted. On GitHub Free a private repo can't be protected
     (see Rulesets above).
  3. Nothing to restart or unlock on the box: `jaunt-repos` shows and clones the new repo,
     and `jauntctl agent` works in its checkout. An agent start in a repo the App isn't
     installed on is refused with the install link.
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

## Agents

Agents run in containers on the box, never in your shell. You start them from the box
itself with `jauntctl agent`, in the folder of the repo they should work on.

```sh
jauntbox                                # attach the box's herdr session
cd ~/src/<repo>                         # in the repo's herdr pane
jauntctl agent                          # Claude opens right there, in a container
```

```sh
jauntctl agent [--branch b] [--max 2h] [--skip-permissions]   # start an interactive run in this checkout
jauntctl agent --unattended "one-line prompt"                 # start a run that works on its own
jauntctl agent attach [<run>] [--shell]   # reopen Claude (it continues) or open bash in the container
jauntctl agent list                       # runs with state, limit and container IP
jauntctl agent stop [<run>] [--now]       # ask for a wrap-up; --now also stops the container
jauntctl agent logs [<run>] [--follow]    # a run's Claude log
```

- **First start.** The first `jauntctl agent` builds the agent image, which takes
  minutes (it says so before it starts). `--max` takes a duration above 0 and at most
  24h; without it the box's `agent.max_duration` (default 8h) applies. Claude's credential is `CLAUDE_CODE_OAUTH_TOKEN`: run `claude setup-token`
  on your laptop, then `jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN` and
  `jauntbox unlock`. Agents commit as you, so the box needs `git config --global
  user.name` and `user.email`; `start` refuses with the fix if they are missing.
- **Where it works.** The container mounts the checkout you are in, with your uid, so
  you see its changes live and its files stay yours. The agent stays on your current
  branch. On the repo's default branch (or a detached HEAD) the run switches the
  checkout to `agent/<repo>-<YYYYMMDD>` and says so; `--branch b` switches to `b`
  (created from `origin/b` when only the remote has it, else from your current commit).
  Uncommitted changes go along; a switch git refuses fails the start.
- **Several repos at once, one run per checkout.** Each repo under `~/src` can have its
  own run, container and token. A second `jauntctl agent` in a checkout that already
  has a run refuses and names the run to attach to.
- **Closing the pane doesn't end the run.** Interactive containers stay up; reopen
  with `jauntctl agent attach` (in the same folder, or give a run id).
  `attach` and `stop` act only on your own running containers. `--skip-permissions`
  lets interactive Claude skip permission prompts; unattended runs always do (the
  container is the sandbox). herdr does not see Claude's working or idle status
  through `docker exec`.
- **Unattended.** The prompt can be any length up to 64 KiB, several lines
  included. `jauntctl agent logs --follow` watches the run. `logs` reads any of your
  own runs, finished or not; without an id it shows the newest run of the current
  checkout. Finished runs (record and log) are removed 30 days after they finish;
  the 20 most recent are always kept.
- **What a container holds.** Only the checkout (`/work`), a read-only per-run state
  directory (its token and wrap-up flag), the repo's agent home on the volume
  (`/home/agent`: Claude's config and transcripts, the `mise` cache) and the run's log
  directory (`/var/log/jauntbox`). No
  Docker socket, no host networking, not privileged, all capabilities dropped,
  `no-new-privileges`. It gets a token for that one repo (re-read from a file the
  supervisor refreshes) and only the agent credentials the adapter declares. It never
  holds the App private key, the Hetzner token or other repos' tokens. Toolchains come
  from `mise` inside the container (the repo's mise config is trusted there), cached
  in the agent home on the volume.
- **What it cannot reach.** On the root daemon, traffic a container sends through the
  box to the metadata service (`169.254.169.254`) or the tailnet (`100.64.0.0/10`,
  `fd7a:115c:a1e0::/48`) is dropped (`DOCKER-USER`, IPv4 and IPv6). Traffic to the box
  itself is dropped too: anything arriving from a Docker bridge (`docker0`, `br-*`) at
  the host (`INPUT`) is dropped, so a container cannot reach the bridge gateway, the
  box's tailnet address, sshd or anything you run on the box (dev servers, databases,
  your rootless Docker's published ports), on any of the box's addresses. Replies to
  connections the box opens are let through, so you still reach a container by its IP
  (previews). The internet, GitHub and package registries stay reachable. The unit is enabled by the install step (best effort) and every
  `jauntctl agent` start re-applies the rules and refuses to start if it cannot.
  Containers you start yourself on the root daemon (`sudo docker run`) are covered once
  the guard has been applied (by an agent start or after a reboot).
- **The checkout guard, and what it does not cover.** Inside the container
  `.git/config`, `.git/hooks`, `.git/commondir` and (if present) `.git/modules` are
  read-only, so the agent cannot plant hooks or config your own `git` would run.
  `.git` itself is mounted separately (writable, so the agent can commit), which makes
  it a mount point the agent cannot move away or remove to plant a fresh one. Your
  global git settings also stop your git descending into submodules. It does not cover
  code you run yourself after the agent changed it: tests, `make`, `mise` and `direnv`
  config. **Run agent-changed tests inside the container** (`jauntctl agent attach
  --shell`) and don't trust agent-changed `mise` or `direnv` files. Host `git` works
  normally in the checkout (status, log, commit, push); only running the agent's code
  on the host is what to avoid. Inside the container, because `.git/config` is
  read-only, `git push -u` reports a config write error but the push lands; branches
  are created without tracking.
- **Wrap-up and `CONTINUATION.md`.** On any forced stop (`jauntctl agent stop`, the
  run's `--max` duration, the box's night deadline or `jauntbox down`) the agent is
  told to write `CONTINUATION.md` (what is done, what is next), commit it and push.
  Claude's hooks (managed settings the repo and the agent cannot change) inject the
  instruction and block stopping until it is pushed. If nothing appears within
  `limits.wrapup_grace` (default 5 min), the box writes a mechanical `CONTINUATION.md`
  (git status, diff summary, last log lines) and pushes it itself, with `--no-verify` so your repo's hooks
  don't block it. It never pushes to the default branch: if the checkout is on it (or on a
  detached HEAD) by then, the work goes to a new `agent/<repo>-wrapup-<UTC stamp>` branch.
  If it cannot write the note it pushes nothing. Teardown order: wrap-up agents, revoke tokens, stop containers, sync,
  delete the box.
- **Box wrap-ups.** Wrap-ups the box starts carry its stop reason prefixed `box-`:
  `box-idle`, `box-evening-cutoff`, `box-max-uptime`, `box-night-deadline`,
  `box-agents-done` or `box-stop-requested`. When the box goes back to "keep" (for
  example you type in a pane during an idle wrap-up), those are cleared, but not
  wrap-ups from `jauntctl agent stop` or `run-max`. `jauntbox night` itself starts no
  wrap-up; the night deadline does. The supervisor's agent
  work is bounded: 2 minutes per normal pass, up to 5 minutes to finish agents before
  the box deletes itself.
- **Resume.** `jauntctl agent --branch <branch>` on a branch that already has
  `CONTINUATION.md` opens Claude with "Read CONTINUATION.md at the repository root and
  continue from it."
- **Trust.** The box user is trusted: they have the sudo password and can read
  everything an agent can. Agents never run on the host.

## Root and sudo on the box

- **Root comes from the laptop.** jauntbox's own privileged steps (install, units,
  secrets load, the unlock marker, stop, night, box status) run over Tailscale SSH as
  `root`. The tailnet policy allows that only for you. If it is not allowed, the
  command fails with `root ssh to <host> failed; your tailnet policy must allow ssh as
  root to tag:jauntbox-<user>`. Run `jauntbox acl-snippet` (it now includes `root`),
  paste it into your policy, and retry. The laptop never falls back to your sudo.
  `jauntbox down` on an online box stops with the same error and suggests
  `jauntbox down --force`, which deletes the box without the wrap-up. An offline box
  is still deleted through Hetzner.
- **`sudo` asks for a password.** Set it with `jauntbox secrets set BOX_SUDO_PASSWORD`.
  Every unlock applies it with `chpasswd`; it is never stored on the box. Without it,
  `unlock` and `status` print a note and you have no sudo password. Don't set it to an
  empty string.
- **Passwordless sudo covers four `jauntctl` commands only:** `mint`, `repos`,
  `box-status` and `agent`, in the forms the sudoers file lists. Nothing else:
  not `install`, `systemctl`, `secrets-load`, `stop`, `night-set` or `supervise`.
  Under sudo, `jauntctl` refuses `--config`, scrubs its environment, and `mint` takes
  only `owner/name`.
- **No `docker` group.** `docker` and `docker compose` work without sudo against your
  own rootless daemon (context `rootless`, socket `/run/user/<uid>/docker.sock`, data
  in `~/.local/share/docker` on `/home`, so images survive rebuilds). Published ports
  must be 1024 and up. `sudo docker ...` reaches the root daemon and asks for your
  password. Rootless setup is best-effort: if it fails, the provisioning log says
  `WARNING: rootless docker setup failed: <step>`.
- **Two Dockers, on purpose.** The box runs two Docker daemons that don't see each other:
  | | Your Docker | The agents' Docker |
  |---|---|---|
  | Command | `docker`, `docker compose` | none: only `jauntctl agent` drives it |
  | Daemon | rootless, runs as you | the root daemon |
  | What runs there | your services, your experiments | agent containers, one network per run |
  | Ports | published on the box, reachable from your laptop over the tailnet | not published |
  | `docker ps` shows | your containers | nothing; use `jauntctl agent list` (or `sudo docker ps`) |

  Keeping agents off your daemon means an agent can't start, stop or inspect your
  containers, and can't use a Docker socket to become root. The cost: agents don't reach
  your services yet (Plan 04 opens the ports you list).
- **Metadata endpoint.** The unit `jauntbox-metadata-guard.service` drops traffic to
  `169.254.169.254` from any non-root process, so the Hetzner user data (which holds
  boot-time secrets) isn't readable by you or your containers. If `iptables` fails the
  unit fails open and shows as failed. Containers on the root daemon are covered by the
  agent network guard (see Agents).
- **The accepted weakness.** You know the sudo password, so you can become root on your
  own box. A process running as you can't use `sudo` by itself, but it can plant a fake
  `sudo` (an alias or a PATH shim in your shell config) and capture the password the next
  time you type it. Type the password only in a shell you trust. The split narrows what a
  process running as you can do on its own; it does not protect against you or against a
  shell that has already been tampered with.
- **Upgrading.** Update the tailnet policy first. Boxes created before this change keep
  the old passwordless sudo for everything until they are recreated, which the daily
  teardown does. Such a box also keeps its old `jauntctl`, so `BOX_SUDO_PASSWORD` sits in
  that box's RAM vault (`agents.env`) until it is rebuilt. An empty password is refused
  by `jauntbox secrets set`.

## Status

**Beta.** Plans 01 (walking skeleton), 02 (GitHub and secrets), 03a (privilege split) and
03 (agent containers) are built and merged; Plan 03's live smoke is partly done. Plan 04
(agents reach the services you start with `docker compose`, and agent previews in
your laptop browser) is next.

- Design: [`docs/specs/2026-11-09-jauntbox-single-machine-design.md`](docs/specs/2026-11-09-jauntbox-single-machine-design.md)
- Plans: [`docs/plans/`](docs/plans/)

The earlier OpenTofu implementation (and the team/fleet design built on it) lives in the
git history; a fleet version comes later.

## Licence

MIT, see [LICENSE](LICENSE). Copyright (c) 2026 WA Systems. WA Systems builds an
enterprise edition and a hosted control plane on top of jauntbox, in a separate
repository. Contributing: see [CONTRIBUTING.md](CONTRIBUTING.md).

Built by [WA Systems](https://wa-systems.eu).
