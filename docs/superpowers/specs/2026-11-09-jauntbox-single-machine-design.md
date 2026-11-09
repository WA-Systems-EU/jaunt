# jauntbox — single-machine remote workspace — Design

**Date:** 2026-11-09
**Status:** Design — awaiting approval
**Branch:** `feat/remote-workspace`
**Related:** [tofu site design](./2026-07-21-jaunt-tofu-site-design.md),
[control plane (fleet) design](./2026-07-24-jaunt-control-plane-design.md),
[Tailscale stable identity](./2026-07-24-tailscale-stable-node-identity-design.md),
[Packer image](./2026-07-27-jaunt-packer-image-design.md)

> Convention in this document: **(Thomas)** marks a decision the owner made;
> **(builder's call)** marks a decision taken during design that the owner has not
> explicitly confirmed and may revisit; unmarked text is description.

---

## 1. Intent — why this exists

AI coding agents (Claude Code and its peers) now run for hours with broad access to
whatever machine they're on. Running them on a personal laptop or home workstation puts
them in the same place as personal data, browser sessions, password stores and the SSH
keys that reach production servers. The owner wants **the code workspace — and every
agent — to live remotely, as simple as that**, with the laptop (or a small Lenovo thin
client on the desk) reduced to a terminal that starts the box, connects to it, and holds
the things agents must never touch. **(Thomas)**

Secondary driver: the owner's home workstation is RAM-limited (48 GB, routinely deep in
swap) and buying more RAM is not on the table. A remote box sized per day (16 / 32 / 64 GB)
solves both problems.

The owner believes many developers share this need, so jauntbox is **open source**:

- **jauntbox (this design):** a single-machine tool for one person and one box. No tofu,
  no always-on control server.
- **jaunt fleet (later, separate project):** the existing tofu + `jauntd` design for teams.
  It will reuse this design's Go packages (provider, forge and agent adapters).

### What success looks like

A day goes like this **(Thomas)**:

1. Morning: on the laptop or Lenovo, `jauntbox up small` while planning the day. It does
   not block; a few minutes later the box is ready.
2. `ssh` in (Tailscale), start herdr, resume work on a project with Claude Code or a
   similar tool — interactively, or as an agent running unattended.
3. Through the day the owner and agents commit and push; agents open PRs. The owner
   SSHes into servers **from the laptop/Lenovo, never from the box**, to check logs.
4. Agents keep working while the owner moves between work spots.
5. Evening: work is pushed, the box tears itself down, credentials are revoked, the
   tailnet has no stale devices. Optionally the owner authorises a *robot night session*
   first.

## 2. Goals, non-goals, anti-goals

### Goals

- One command to bring a right-sized box up; one to bring it down; the box also brings
  **itself** down.
- Work survives every teardown (persistent volume), and switching box size is a
  matter of minutes.
- **Three trust zones** (§4) enforced by mechanism, not by convention.
- Agents run unattended, including while the owner is away, and can push branches and
  open PRs — but **merging and deploying are human-only**, enforced on the forge.
- Pluggable **forge** (GitHub first, Gitea/Forgejo second) and **agent** (Claude Code
  first) adapters, and a pluggable **cloud provider** (Hetzner first).
- Open-source quality: someone other than the owner can set it up from the README.

### Non-goals (v1)

- Teams / multiple engineers / central control server — that's the fleet project.
- tofu in the single-machine path.
- An AI gateway/proxy for model API keys (considered and deferred — doesn't fit the
  owner's current Claude usage; may return for specific work cases). **(Thomas)**
- Local models (Ministral etc.), GPU dispatch (RunPod/UpCloud), and a custom jaunt agent —
  a later, separate project. **(Thomas)**
- Deploying from the box. Projects deploy via CI/CD on merge; migrating each project's
  current deploy flow (odysseus from the workstation) to CI/CD is a per-project
  prerequisite outside jauntbox. **(Thomas)**
- Providers other than Hetzner (the interface exists; implementations come later).
- The Packer image (separate spec; jauntbox should keep working with or without it).

### Anti-goals — it fails even if it "works" when…

- an agent can reach production servers, or the laptop;
- a long-lived, account-wide forge credential (e.g. a `gh auth login` token) sits on
  the box or its volume;
- an agent can merge to the default branch, change CI workflows, or touch repo
  secrets/settings;
- a box can bill unattended past the agreed limits without an explicit night
  authorisation;
- an agent container can reach the Docker socket (that is root on the host).

## 3. Constraints

- **Language: Go.** **(Thomas)** — chosen over Rust for the ecosystem
  (`hcloud-go`, `go-github`, `code.gitea.io/sdk`, Tailscale Go libraries) and code sharing
  with the fleet's `jauntd`.
- **Cloud: Hetzner only in v1**, behind a provider interface. **(Thomas)**
- **amd64 only in v1.** **(Thomas)** This isn't a Hetzner limitation (it sells ARM `cax`
  types); it comes from projects such as df that need amd64-only images and x86-pinned
  toolchains. **Expected soon:** choosing a *flavour* besides size (arm64 first, and
  possibly other providers such as Scaleway, or GPU later). So the size-profile config must
  be a structured record that can take an `arch` (or `flavour`) field later without a
  breaking change, and the agent image build shouldn't assume amd64 where it costs nothing
  to stay neutral.
- **Network: Tailscale in v1.** Box reachable only via Tailscale (cloud firewall
  default-deny inbound, as today). Stable MagicDNS name `jauntbox-<user>`. Tailscale sits
  behind a **mesh adapter** interface (the fourth seam, next to cloud, forge and agent):
  join with a one-shot key, stable hostname, identity-based SSH, access-rule
  check/snippet (box can't reach servers or clients), caller identity (WhoIs, for the
  fleet), persisted node identity. **(Thomas:** alternatives are phase 2.**)**
  - **Headscale** (phase 2): self-hosted coordination server using the same Tailscale
    clients — mostly a `--login-server` setting; verify Tailscale SSH / SSH-policy and
    WhoIs/API differences.
  - **NetBird** (phase 2 / enterprise): German, open-source including the management
    server, WireGuard-based, own SSH and policies — the EU-sovereign, self-hostable option
    for the enterprise pitch; needs a full adapter.
  - Considered and not pursued: Teleport (access/audit platform, not a mesh; possible
    enterprise audit layer later), Nebula / Netmaker / Firezone / plain WireGuard (would
    mean rebuilding identity, SSH and ACLs), ZeroTier (no advantage).
- **Default branch is never assumed to be `main`** — read it from the forge
  (`main`, `master`, `trunk`, …). **(Thomas)**
- Reuse the existing provisioning (`tofu/modules/provision/*.sh`, cloud-init template,
  Tailscale state-on-volume) rather than rewriting it; it moves to a location both the
  Go binary and the tofu fleet can consume.

## 4. Trust zones (the security model)

| Zone | Holds | Can reach | Never holds |
|---|---|---|---|
| **Client** (laptop, Lenovo) | personal data, server SSH keys, master copies of agent credentials, cloud + Tailscale API creds | the box (Tailscale SSH), the servers | agents |
| **Box host** (`jauntbox-<user>`, the owner's shell) | code on the volume, forge App private key (root-only), agent credentials in RAM | forge, internet egress, agent containers | server SSH keys, `gh auth login` tokens |
| **Agent container** (one per project run) | one project's checkout, its agent credential, a credential helper that yields a token for *that repo only* | internet egress, the forge (that repo) | Docker socket, other projects, host credentials |

Enforcement:

- **Box → servers blocked by the Tailscale ACL** (the box's tag gets no SSH/network
  grant to server tags). jauntbox setup generates/validates the needed ACL snippet;
  the owner applies it (§9). Box → client is likewise not granted.
- **Containers** run without the Docker socket, without host networking, without
  privileged mode, with only the project directory (and a per-run state dir) mounted.
- **Agents always run in containers — including interactive Claude Code sessions in
  herdr.** **(Thomas)** No agent CLI is run on the host shell; herdr panes attach *into*
  agent containers. The host shell is for the owner only.
- **Known accepted exposure:** the owner's user on the box has passwordless sudo (existing
  provisioning) and is in the `docker` group, so the *human* zone on the box includes the
  App key and the box's Hetzner token. Acceptable precisely because agents never run on the
  host. Tools the owner runs by hand at the host shell are trusted like the owner.

## 5. Forge access

### GitHub (first adapter)

- The owner creates a **private GitHub App** once ("jauntbox-<user>"), installed only on
  the repos agents may work on. That installation list *is* the whitelist. `jauntbox setup
  github` guides this (manifest flow if practical — **(builder's call)**).
- **App permissions:** contents read/write, pull requests read/write, metadata read.
  **No `workflows`** (cannot modify `.github/workflows` → cannot exfiltrate CI deploy
  secrets), **no administration, no secrets, no environments**.
- **Repo rulesets** applied by `jauntbox setup` per whitelisted repo: target
  `~DEFAULT_BRANCH` (plus optional configured release patterns); restrict updates/
  deletions so only humans can merge; the App is not a bypass actor. **(Thomas:** merge
  and deploy are human territory.**)** Applying rulesets needs an owner/admin credential
  used from the client at setup time — never stored on the box.
- **Key custody (unattended mode, the default):** the App private key lives on the **box
  host**, root-only, used by the supervisor's token minter (§7). It mints
  **installation tokens scoped to a single repository** with the permissions above;
  GitHub caps them at 1 h. Rationale: agents must push while the owner is away
  **(Thomas)**, so an always-present minter is required, and the App's permissions +
  rulesets make a leaked key worth "push branches / open PRs on whitelisted repos" only.
  The key is delivered to the box at `up` from the client and kept in RAM (tmpfs), not on
  the volume. **(builder's call: RAM over volume)**
- **Attended mode (optional, per project):** the key stays on the client, and while a
  client session is active the client pushes freshly minted tokens to the box every
  ~45 min. When the client sleeps, pushes stop within the hour (dead-man's switch).
- **Delivery into containers:** a git credential helper inside the container asks the
  host minter over a per-container Unix socket; the minter only answers for the repo bound
  to that container. Tokens are never written to disk in the container.
- **Revocation:** on container stop and on box teardown, live tokens are revoked via
  the API (they'd expire in ≤ 1 h regardless).
- **The owner at the host shell** uses the same mechanism (git credential helper → minter)
  for manual pushes; `jaunt-repos` (existing script) is adapted to list the App's
  installed repos and clone through App tokens instead of `gh auth login`.

### Gitea / Forgejo (second adapter) — weaker guarantees, documented as such

- No App/installation-token equivalent. Per-repo credentials via **deploy keys with write
  access** (created/deleted through the API) or a **bot user** collaborating only on
  chosen repos. **(builder's call which; lean deploy keys)**
- **No automatic expiry** (to verify): credentials are created at `up`/container start
  and deleted at stop/teardown; each `up` and `jauntbox down` also sweeps orphans
  (labelled by name prefix).
- Branch protection on the default branch via API, as for GitHub.
- **Open risk to verify before claiming parity:** whether a branch push with a modified
  Gitea Actions workflow can access repo secrets. If yes, the adapter must refuse to run
  (or loudly warn) unless deploy secrets are restricted to protected-branch runs or live
  outside Gitea Actions.

### Forge adapter interface (shape, not signature)

List repos available to agents · default branch of a repo · mint repo-scoped credential ·
revoke credential · sweep orphaned credentials · apply/verify protection on default branch
(+ patterns) · open PR (optional convenience). Each adapter declares which guarantees it
provides (auto-expiry, workflow isolation) so the CLI can show them.

## 6. Agents

### Running an agent

`jauntbox agent <project> [--branch b] [--unattended "<task prompt>"] [--max 8h]`
**(builder's call on exact flags)**

- One container per agent run, built from a jauntbox **agent image** (base: the project's
  toolchain needs + the agent CLIs). The project can supply its own image/Dockerfile
  (e.g. `.jauntbox/agent.Dockerfile`); otherwise a default image is used. **(builder's
  call)**
- **Interactive container UX matters** (herdr attaches into the container): the owner's
  shell config/editor available read-only inside, project ports reachable from the host
  for previews. **(builder's call** on mechanism**)**
- **Accessory services (databases etc.) run beside the agent, not inside it.**
  **(Thomas + builder's call)** Each run gets its own private Docker network; the
  project's declared services (selected services from its existing `compose.yml`, or a
  `.jauntbox/services.yml`) are started by the supervisor on that network at run start and
  removed at run end. The agent reaches them by name (`db:5432`) but has **no Docker
  access at all** — it cannot start, stop or inspect containers. The owner can still start
  extra containers by hand from herdr. Automatic start matters for unattended/night runs.
  Service data volumes persist on the box volume per project. **Limitation:** agents can't
  run container-spawning tests (testcontainers) or rebuild the stack; a per-project opt-in
  **nested Docker via sysbox** (`docker: nested`) is a later phase, not v1.
- Agents work on a branch, never the default branch (the ruleset enforces this anyway).
  Default branch name: `agent/<project>-<date>` or user-provided. **(builder's call)**
- **Interactive** use: the owner attaches inside herdr (the container's terminal).
  **Unattended** use: the agent runs headless (`claude -p …` / Agent SDK for Claude Code)
  with a task prompt, logging to a per-run log on the volume.
- Every run has a **max duration** (default 8 h, overridable per run) after which it is
  wrapped up (§6.3).

### Agent credentials

- Subscriptions (Claude, OpenAI) and API keys (Mistral, …) **(Thomas)**. For
  subscriptions, use **long-lived non-refreshing tokens** (Claude: `claude setup-token`,
  as provisioning already expects) — no refreshing OAuth logins on the box, since shared
  read-only copies can't refresh and a box-side refresh could invalidate the client's copy.
  OpenAI/Codex equivalent to verify; if none exists, use an API key. **(builder's call)** Master copies
  live on the client. At `up`, the client pushes them over Tailscale SSH into a
  **RAM-only directory** on the host (tmpfs, root-only). The supervisor injects only the
  credential(s) an agent adapter declares it needs into that container (env or a
  read-only mounted file). Never written to the volume; gone at teardown. Available all
  night for robot sessions.
- Spending limits: provider-side for now (no gateway in v1).

### Agent adapter interface and wrap-up

Each adapter (Claude Code first) knows: how to start interactive and unattended runs ·
which credentials it needs · how to **deliver a wrap-up instruction** · how to resume
with context.

**Wrap-up** happens on any forced stop (19:00 / 10 h / night deadline / run max /
`jauntbox down`):

1. The supervisor sets a wrap-up flag for the run.
2. Claude Code adapter: a jauntbox-provided Claude Code **hook** (on tool use) sees the
   flag and injects the instruction: stop, write `CONTINUATION.md` at the repo root
   (what was being done, current state, next steps, open questions for the owner), commit
   it with the work to the agent's branch, push. For headless runs the supervisor may
   instead stop the process and **resume the same session** with the wrap-up prompt. Exact
   hook mechanics to be verified against current Claude Code docs during planning.
3. **Grace period** (default 5 min — **builder's call**). If no `CONTINUATION.md` appears,
   the supervisor writes a mechanical one (branch, `git status`, diff stat, last log
   lines), commits and pushes it as WIP. The owner always finds *something*.
4. **Resume:** when `jauntbox agent` starts on a branch containing `CONTINUATION.md`, it
   is given to the agent as opening context. Whoever merges deletes it; it never lands on
   the default branch (the PR description should say so; **builder's call** whether a
   check enforces it).

## 7. Box lifecycle and the on-box supervisor

### Commands (client)

| Command | Does |
|---|---|
| `jauntbox up [small\|big\|huge]` | create server (size profile) + attach volume + Tailscale join; returns immediately (`--wait` to block). A detached client-side helper waits for the box to join the tailnet, then pushes secrets (§7.1) |
| `jauntbox down` | wrap-up agents, revoke tokens, delete server (volume kept) |
| `jauntbox status` | box up?, size, uptime, limits remaining, running agents, night session |
| `jauntbox night --until 07:00 [--runs …]` | authorise a robot night session (§7.3) |
| `jauntbox resize <profile>` | down + up with another size (volume reattaches) |
| `jauntbox setup` | one-time: cloud project/token, Tailscale, forge App + rulesets, ACL snippet, config file |
| `jauntbox ssh` / `connect` | convenience SSH into the box |

`jauntbox agent …` runs **on the box** (same binary, box-side subcommands).
**(builder's call: one binary, two roles)**

### Secret delivery and the locked state **(builder's call)**

Secrets that must not sit in user-data (the forge App key, agent credentials) are pushed
by the client over Tailscale SSH into tmpfs. Because `up` doesn't block, it spawns a
short-lived detached helper that waits (minutes) for the box to answer on the tailnet,
then pushes. Hetzner user-data stays readable from the metadata endpoint for the server's
lifetime, so it carries only what it carries today (Tailscale auth key — shredded after
join — and the self-delete token) and nothing more.

If tmpfs is empty (helper never ran, or the box rebooted), the supervisor is **locked**:
no agent starts, running agents pause at their next forge/credential request, and
`jauntbox status` says so. Any client command (`status`, `up`, `ssh`, `unlock`) re-pushes
and unlocks. A reboot during a night session therefore pauses work until morning — the
safe failure.

### Stateless client

No local state file. The server, volume, firewall and SSH key are found on Hetzner by
**labels** (`jauntbox=<user>`), so the laptop and the Lenovo drive the same box with no
sync and either can run `down`. **(builder's call)**

jauntbox uses its **own dedicated Hetzner project**, separate from the one the tofu roots
(and the gbrain box) use. It never adopts or touches tofu-managed resources; existing
work moves over through git. **(builder's call)** Config (size profiles, timezone,
limits, forge, credentials paths) is a YAML file on each client.

### Size profiles

Named profiles in config mapping to Hetzner x86 server types, defaults roughly
`small` ≈ 16 GB, `big` ≈ 32 GB, `huge` ≈ 64 GB. Exact type names and prices are checked
against Hetzner's current catalogue at build time (they change — `cpx41` was retired
recently). **(Thomas** sizes; **builder's call** names/types.**)** The volume is in one
Hetzner location; all profiles must be offered there.

### Activity and limits **(Thomas)**

- **Human activity**: an SSH/herdr session with input within the last ~30 min
  **(builder's call** on the threshold and detection method — an open-but-abandoned
  session must not count).
- **Agent activity**: a running jauntbox agent container.
- Rules, evaluated every ~2 min by the supervisor:
  1. Human activity → box stays up (limits below don't apply while the owner is actively
     working).
  2. Otherwise, the box goes down at the **earlier of 10 h after `up` and 19:00 local**
     (owner's configured timezone), after wrap-up — **unless a night session is active**.
  3. Otherwise, idle (no human, no agent) for the idle window (keep today's 2 h day /
     1 h night defaults) → down.
  4. Every agent run also has its own max duration.
- **Robot night session**: `jauntbox night --until <time>` authorises agents to continue
  past the evening limit until the deadline; the box tears down when all authorised runs
  finish or at the deadline (with wrap-up), whichever comes first. Valid for that one
  night only. **(Thomas)**
- The current shell `idle-watch` + `decide()` logic is ported into the Go supervisor
  (keep the pure decide-function + table tests pattern).

### Teardown

Wrap-up agents → revoke forge tokens → stop containers → sync/unmount volume → the box
deletes **itself** via the Hetzner API (as today). The cloud token on the box must
therefore be able to delete its own server; scope it to a dedicated Hetzner project
containing only jauntbox resources (as today). Tailscale: keep the existing persisted
node identity so the device entry is **reused** each day — one stable device, no
accumulating dead entries. **(builder's call** interpreting "tailscale cleaned up"; the
owner may prefer an ephemeral node removed each night.**)**

## 8. What exists today and how it is reused

- `tofu/modules/jauntbox/cloud-init/user-data.tftpl` — volume mount at `/home`, Tailscale
  state on the volume, `tailscale up --ssh`, auth-key shredding. Re-expressed as a Go
  template rendered by the CLI.
- `tofu/modules/provision/{dev-setup.sh,dev-setup-user.sh,jaunt-repos.sh}` — kept;
  `jaunt-repos.sh` adapted to App tokens (§5); `dev-setup-user.sh` no longer relies on
  `gh auth login`.
- `tofu/modules/jauntbox/agent/idle-watch.sh` — superseded by the Go supervisor for the
  single-machine path (tofu path untouched until the fleet project).
- Location of shared assets so both Go (`go:embed`) and tofu can use them: **builder's
  call** (e.g. a top-level `provision/` directory).
- `cmd/` and `internal/` are already reserved for Go code.

## 9. Setup (one-time) — what the owner does

1. Hetzner: a dedicated project + API token.
2. Tailscale: API key/OAuth client; tag for the box; ACL changes so the box tag cannot
   reach server tags or client devices. jauntbox prints the snippet and verifies the live
   policy; it does not overwrite a hand-managed policy. (The repo's `tailnet` root is
   currently out of sync with the live policy — don't apply it blindly.)
3. Forge: create the App, install on chosen repos, apply rulesets (`jauntbox setup github`).
4. Agent credentials stored on the client (location/format: **builder's call**; e.g. a
   file readable only by the user, or the OS keyring).
5. Config file: profiles, timezone, limits.

## 10. Phasing **(builder's call)**

- **v1:** Hetzner provider · GitHub forge adapter (unattended key custody) · Claude Code
  agent adapter · containers + accessory services · lifecycle rules, night sessions,
  wrap-up/CONTINUATION · setup command · ported provisioning. Provider/forge/agent
  interfaces in place.
- **Phase 2:** Gitea/Forgejo adapter (after the Actions-secrets question is answered) ·
  attended mode · nested Docker (sysbox) opt-in · more agent adapters · Headscale and
  NetBird mesh adapters · arm64 / other flavours.
- **Later / separate projects:** fleet (tofu + jauntd), local models/GPU/own agent,
  AI gateway, Packer image integration.

### Open core and extension points

**Direction (Thomas), past v1:** jauntbox single-machine is open source (MIT) and fully
usable on its own. **Fleet management is the enterprise product**, with a cheap,
feature-complete tier for small teams (2–5 engineers, priced per engineer, cap in the
licence terms per organisation). Licence for enterprise code (closed vs source-available,
e.g. FSL) is undecided. This reverses the earlier "not a SaaS / the public repo is the
product" stance in the site and control-plane specs; those get updated when the enterprise
work starts, **not in v1**.

**What v1 must provide anyway** (good design regardless; makes enterprise features
add-ons rather than forks):

- **Event stream / audit log** from the supervisor: box up/down, limit reached, night
  session authorised, agent run start/stop, token minted/revoked, wrap-up outcome.
  Structured (JSON lines) on the volume in v1, with a sink interface.
- **Policy hook:** limits, allowed size profiles, allowed repos and night-session rules
  come from a policy source interface. v1 implements it with the local config file; a
  central service can implement it later.
- **Adapters (provider, forge, agent, mesh) as stable Go interfaces** in their own packages.

## 11. Left to the builder

Package layout; CLI framework; exact flag names; config schema; how the supervisor
detects input-recency on SSH/herdr sessions; container runtime details (rootless or not,
user namespaces); the default agent image contents; the mechanical CONTINUATION
template; test strategy (keep decide()-style pure-function table tests; integration
smoke test documented like `tofu/SMOKE.md`).

## 12. Housekeeping found during design (not part of this spec, but blockers to note)

- `lenovo.txt` in the repo root contains a Tailscale auth key — revoke, delete, gitignore.
- Uncommitted gbrain work on this branch (shared box inside the per-engineer root) and a
  debug `allow_ssh` rule — resolve before merging anything.

## 13. Open questions to verify during planning

- Claude Code hook mechanics for injecting the wrap-up instruction (event, output format).
- Gitea Actions: secret exposure on branch pushes with modified workflows.
- Hetzner current x86 types per size profile in the chosen location.
- GitHub App manifest flow feasibility for `jauntbox setup github`.
