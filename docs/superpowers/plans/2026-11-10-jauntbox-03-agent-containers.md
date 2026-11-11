# jauntbox 03: agent containers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From the folder of any repo you cloned under `~/src` on the box, `jauntctl agent` runs Claude Code in a container that sees only that checkout, its own agent credential and a token for that repo only; several repos can have agents at once, and forced stops wrap them up into a pushed `CONTINUATION.md`.

**Architecture:** In a herdr pane, inside `~/src/<repo>`, you type `jauntctl agent`. It reads the repo from the checkout's `.git/config` (parsed, never by running git as root), then a root part (`jauntctl agent start-run`, via `sudo -n`) builds the embedded agent image once, writes the repo's App token into a tmpfs file mounted read-only into the container, and starts the container with your uid on its own Docker network, with your checkout mounted at `/work` and git's command-carrying files (`.git/config`, `.git/hooks`, `.git/modules`, `.git/commondir`) mounted read-only so the agent can't make your own git run its commands. Claude then opens in the same pane. The supervisor refreshes run tokens each tick, enforces each run's max duration, raises a wrap-up flag that managed Claude Code hooks turn into an instruction, and runs a mechanical in-container fallback before stopping containers and revoking tokens.

**Tech Stack:** Go 1.x (cobra), Docker CLI via `os/exec`, iptables/ip6tables `DOCKER-USER`, POSIX sh + jq scripts in the image, Claude Code managed settings hooks.

**Spec:** `docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md` (§4 trust zones, §6 agents, §7 activity/teardown).

## Global Constraints

- Agents always run in containers; no agent CLI on the host shell (spec §4).
- Containers: no Docker socket, no host networking, not privileged, only the project directory and a per-run state dir mounted (spec §4). Add: `--cap-drop ALL`, `--security-opt no-new-privileges`, `--init`, `--pids-limit 4096`.
- The agent container never holds the App private key, the Hetzner token or other repos' tokens (spec §4 table).
- Agent credentials: only those the adapter declares, from the RAM vault `/run/jauntbox/secrets`, never written to the volume (spec §6 "Agent credentials").
- Every run has a max duration, default **8 h**, overridable per run (spec §6).
- Wrap-up on any forced stop; grace = `limits.wrapup_grace` (default 5 min); mechanical `CONTINUATION.md` if none appears (spec §6.3).
- Teardown order: wrap-up agents → revoke forge tokens → stop containers → sync → self-delete (spec §7 Teardown). The mechanical fallback pushes, so it runs before revoke.
- Box-side root commands are `jauntctl` subcommands; laptop commands are `jauntbox` (Plan 02 Task 0).
- **03a has landed.** Every Docker call runs inside root `jauntctl`. `jauntctl agent attach` re-execs through `sudo -n jauntctl agent attach …` (allowed by the sudoers line) and runs `docker exec` as root with `--user <uid>:<gid>`. The four host git guard settings go into `installCmd` through `runuser -u <user> --`. Agent containers never mount `/run/user/<uid>` or the box user's rootless Docker socket (the box user's own rootless daemon is separate from the root daemon the agents use). The next bullet is the original statement of what 03a does.
- **Plan 03a lands first** (`docs/superpowers/plans/2026-11-10-jauntbox-03a-privilege-split.md`): the box user is not in the `docker` group and has passwordless sudo only for `jauntctl mint|repos|box-status|agent`; every Docker call happens inside root `jauntctl`; `jauntctl agent attach` re-execs through `sudo -n` and runs `docker exec` as root; `installCmd` runs as root over root ssh, with user git settings through `runuser -u <user> --`.
- Tests never call real Docker, iptables, GitHub, Hetzner or Tailscale: fakes behind interfaces, shell scripts tested with stubs.
- Commits: plain conventional messages, no attribution trailers.
- Exact values used across tasks:
  - the container runs as the box user's uid/gid (`--user <uid>:<gid>`), with `HOME=/home/agent`; workdir `/work` = the checkout (`/home/<user>/src/<repo>` or any git checkout under `/home/<user>`)
  - agent home (Claude's config, transcripts, mise cache) on the volume: `/home/.jauntbox/agents/<owner>/<repo>/home` → `/home/agent` (owned by the user, 0700)
  - run state (tmpfs): `/run/jauntbox/runs/<id>/` (root:<user gid>, 0750) → `/run/jauntbox-run` read-only; files `token` (root:<user gid> 0640), `wrapup` (0644, content = reason)
  - run record and logs (volume): `/home/.jauntbox/runs/<id>/run.json` (root, 0600) and `/home/.jauntbox/runs/<id>/log/` (user, 0750) → `/var/log/jauntbox`
  - checkout guard (read-only bind mounts over the writable checkout): `<checkout>/.git/config` → `/work/.git/config:ro`; `<checkout>/.git/hooks` (created empty if missing) → `/work/.git/hooks:ro`; `<checkout>/.git/modules` (only if it exists) → `/work/.git/modules:ro`; `<checkout>/.git/commondir` (created with the content `.` if missing; `.` keeps git's common dir where it is) → `/work/.git/commondir:ro`. A read-only mount can't be edited, deleted or renamed from inside the container.
  - box user's global git settings, added by the install command: `diff.ignoreSubmodules=all`, `status.submoduleSummary=false`, `submodule.recurse=false`, `core.fsmonitor=false` (your git never descends into submodules an agent could have planted)
  - run id: `<repo name lowercased>-<UTC YYYYMMDD-HHMMSS>`; container and network name `jauntbox-<id>`
  - labels: `jauntbox.agent=1`, `jauntbox.run=<id>`, `jauntbox.repo=<owner>/<repo>`
  - image tag `jauntbox/agent:<first 12 hex of sha256 over the embedded build context>`
  - default branch name `agent/<repo>-<YYYYMMDD>` (local date in the box timezone)

## Rulings (decisions the spec leaves to the builder)

1. **Token delivery: a file, not a broker.** The supervisor (every tick) and `agent start` write the current token to the run's `token` file; the in-image credential helper reads it. No socket daemon. Cost if wrong: a broker later replaces one writer.
2. **The agent works in your checkout, as you.** No second clone: the container mounts the checkout you are in, with your uid, so its files stay yours and you see its changes live. The checkout guard (Global Constraints) keeps git's command-carrying files read-only inside the container; the agent still commits and pushes (image git config: `push.default=current`; branches are created with `--no-track`, since writing tracking info to `.git/config` is refused). What the guard can't cover is code you run yourself after the agent changed it (tests, `make`, `mise`/`direnv` config): the docs say to run those inside the container (`jauntctl agent attach --shell`) and not to trust agent-changed `mise`/`direnv` files. Cost if wrong: a later plan narrows the host user's access (03a already limits their sudo).
3. **One active run per checkout, any number of checkouts.** Each repo under `~/src` can have its own run at the same time, each with its own container and token. A second `jauntctl agent` in a checkout that already has a run refuses and names the run to attach to (two agents in one working tree would edit the same files). One agent spanning two repos (`--with ../other`) is left for later. Cost if wrong: parallel runs in one repo later need per-run worktrees.
4. **You start agents from herdr, in the repo's folder.** `jauntbox` gets you into the box's herdr session; in a pane inside `~/src/<repo>` (its herdr workspace opens there), `jauntctl agent` starts the run and Claude runs right there in that pane. `jauntctl agent attach` (in the same folder, or with a run id anywhere) reopens it (Claude continues with `--continue`). Interactive runs keep the container alive (`sleep infinity` under `--init`) and Claude runs through `docker exec -it`, so closing the pane doesn't end the run. Unattended runs make `claude -p` the main process; `jauntctl agent logs --follow` watches one. There is no laptop-side agent command. herdr can't see Claude's working/idle status through `docker exec`; reporting it to herdr is left for later.
4b. **Branch.** The agent stays on your current branch, unless it is the repo's default branch (or HEAD is detached): then the container switches the checkout to `agent/<repo>-<YYYYMMDD>` (creating it from your current commit with `--no-track`), and `jauntctl agent` says so, since it is your working tree too. `--branch b` switches to `b` (created the same way if missing). Uncommitted changes go along with the switch; a switch git refuses fails the start with git's message.
5. **Hooks live in the image's managed settings** (`/etc/claude-code/managed-settings.json`), which the repo and the agent (non-root) cannot change: `PreToolUse` injects the wrap-up instruction once per session via `additionalContext`; `Stop` blocks stopping (`decision: block`) until `CONTINUATION.md` is committed and pushed, unless `stop_hook_active` is true.
6. **Resume** passes an opening prompt (`Read CONTINUATION.md at the repository root and continue from it.`) when the branch already has `CONTINUATION.md`; no SessionStart hook.
7. **Permissions:** unattended runs pass `--dangerously-skip-permissions` (the container is the sandbox; it is refused as root, and we run as your uid). Interactive runs use Claude's default mode unless `--skip-permissions` is given.
8. **Default image only in v1:** project Dockerfiles, dotfile mounts and accessory services (compose) move to Plan 04. Toolchains come from `mise` inside the container (`mise install` when the repo has `.tool-versions` / `mise.toml`), cached in the agent home on the volume.
9. **Network guard:** `DOCKER-USER` drops container traffic to `169.254.169.254/32` and `100.64.0.0/10`, and (ip6tables) to `fd7a:115c:a1e0::/48`. Applied by a boot unit and re-checked by every `agent start`, which refuses to start if it cannot apply them.
10. **Git identity** comes from the box user's `~/.gitconfig` (`user.name`, `user.email`); missing → `start` refuses with the fix (`git config --global user.name …` on the box).
11. **Previews:** the host reaches a container by its network IP; `agent list` prints it. No published ports.

## Review Focus

1. A locked box or a missing `CLAUDE_CODE_OAUTH_TOKEN` → `start` refuses with the unlock / `jauntbox secrets set` message and leaves nothing behind (Task 4 test).
2. `jauntctl agent` outside a git checkout under your home, in a linked worktree or submodule (`.git` is a file), or in a checkout whose `origin` is not an installed GitHub repo → clear error saying which and how to fix it (install URL for the last), nothing created (Task 4 test).
3. A second `jauntctl agent` in a checkout that already has a run → refused, names the existing run; a run in another checkout is unaffected (Task 4 test).
4. The container dies during init (branch switch refused, bad `--branch` name, `mise install` fails) → `start` reports the tail of the init log and removes container, network and run dirs (Task 4 test).
5. After a reboot or a Docker restart, `run.json` exists but the container and tmpfs run dir do not → the supervisor marks the run finished without crashing, `agent list` shows it as `gone` (Task 6 test).

---

### Task 0: Unknown agent count never skips wrap-up (carry-forward)

**Files:**
- Modify: `internal/lifecycle/decide.go`, `internal/supervisor/run.go:77-120`
- Test: `internal/lifecycle/decide_test.go`, `internal/supervisor/run_test.go`

**Interfaces:**
- Produces: `lifecycle.Inputs.AgentsUnknown bool`.

- [ ] **Step 1: Failing tests.** In `decide_test.go`, `TestStopWithUnknownAgentsWrapsUp`: `Inputs{StopRequested: true, AgentsRunning: 0, AgentsUnknown: true, …}` → `Verdict{StartWrapUp, "stop-requested"}`; with `WrapUpStartedAt` set and grace not elapsed → `Keep "wrapup-in-progress"`; elapsed → `Delete "wrapup-grace-elapsed"` (never `"no-agents"` while unknown). In `run_test.go`, `TestDockerErrorWithActiveHumanAndStopStartsWrapUp`: counter returns an error, human active, stop file present → wrap-up flag written, no delete.
- [ ] **Step 2:** `go test ./internal/lifecycle ./internal/supervisor` → FAIL.
- [ ] **Step 3:** In `stop()`, treat `AgentsUnknown` as "agents may be running" for both the start and the `no-agents` branch. In `evaluate`, set `AgentsUnknown: true` where today the count error is downgraded to a warning.
- [ ] **Step 4:** Tests pass; full `go test ./...` passes.
- [ ] **Step 5: Commit** `fix(jauntbox): an unknown agent count never skips wrap-up`.

### Task 1: Agent config and box info

**Files:**
- Modify: `internal/config/config.go`, `internal/boxinfo/boxinfo.go` (the file defining `BoxInfo`), `examples/jauntbox.yaml`, wherever `BoxInfo` is filled at `up` (grep `boxinfo.BoxInfo{`)
- Test: `internal/config/config_test.go`, `internal/boxinfo/*_test.go`

**Interfaces:**
- Produces: `config.Agent{MaxDuration Duration \`yaml:"max_duration" json:"max_duration"\`}` as `Config.Agent` (`yaml:"agent"`), default `8h` in `config.Default()`, validated `> 0` and `<= 24h`; `BoxInfo.Agent config.Agent` (`json:"agent"`), filled from the client config at `up`.

- [ ] **Step 1: Failing tests:** `TestDefaultAgentMaxDuration` (8h), `TestAgentMaxDurationValidation` (`0s` and `25h` rejected with a message naming `agent.max_duration`), `TestBoxInfoCarriesAgent` (round trip keeps `max_duration`).
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run → PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): agent max duration in config and box info`.

### Task 2: The agent image (embedded build context and scripts)

**Files:**
- Create: `provision/agent/Dockerfile`, `provision/agent/jaunt-agent-init`, `provision/agent/jaunt-git-credential`, `provision/agent/jaunt-hook`, `provision/agent/jaunt-unattended`, `provision/agent/jaunt-wrapup-fallback`, `provision/agent/managed-settings.json`, `provision/agent/agent_test.sh`
- Create: `internal/agentimage/image.go`, `internal/agentimage/image_test.go`
- Modify: `provision/embed.go` (embed `agent/*`)

**Interfaces:**
- Produces: `agentimage.Tag() string`; `agentimage.Context() ([]byte, error)` (a tar of `provision/agent/*` with `Dockerfile` at the root, scripts mode 0755); `agentimage.Ensure(ctx context.Context, d Docker) error` where `type Docker interface { ImageExists(ctx, tag string) (bool, error); Build(ctx context.Context, tag string, contextTar []byte) error }` (Task 4 implements it with the CLI).
- Script contract (Tasks 4 and 6 rely on these exact names, all in `/usr/local/bin`):
  - `jaunt-agent-init <cmd…>`: needs env `JAUNT_REPO` (owner/repo), `JAUNT_RUN`, `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, optional `JAUNT_SWITCH_TO` (branch to switch to). Checks that `/work` is a git checkout; when `JAUNT_SWITCH_TO` is set, `git switch "$JAUNT_SWITCH_TO"` if it exists locally, else `git switch --no-track -c "$JAUNT_SWITCH_TO"`; runs `mise install` when `/work/.tool-versions` or `/work/mise.toml` exists; touches `/tmp/jauntbox-ready`; then `exec "$@"`. Output goes to `/var/log/jauntbox/init.log`. It never clones and never writes git config.
  - `jaunt-git-credential get`: prints `username=x-access-token` / `password=<contents of /run/jauntbox-run/token>`; missing or empty token → prints `quit=1`. Any other op → exit 0 silently.
  - `jaunt-hook pretool|stop`: reads hook JSON on stdin. No `/run/jauntbox-run/wrapup` → exit 0, no output. `pretool`: once per `session_id` (marker `/tmp/jauntbox-wrapup-told-<session_id>`) prints `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":<instruction>}}`. `stop`: unless done (CONTINUATION.md exists in `/work`, `git status --porcelain` empty, `git fetch -q origin <current branch>` then `git rev-list origin/<current branch>..HEAD` empty; a missing remote branch counts as not pushed) and unless `stop_hook_active` is true, prints `{"decision":"block","reason":<instruction>}`.
  - Instruction text, verbatim, with `<reason>` from the flag file: `jauntbox is wrapping up this run (<reason>). Stop the current task now. Write CONTINUATION.md at the repository root with: what you were doing, the current state, next steps, and open questions for the owner. Commit it together with your work to the current branch and push. Do not start new work.`
  - `jaunt-unattended`: runs `claude -p "$JAUNT_PROMPT" --output-format stream-json --verbose --dangerously-skip-permissions` in `/work`, stdout to `/var/log/jauntbox/claude.jsonl`, stderr to `/var/log/jauntbox/claude.err`, exits with Claude's status.
  - `jaunt-wrapup-fallback <reason>`: in `/work`; if done (as above) prints `already-done`; else writes `CONTINUATION.md` if absent (template below), `git add -A`, commits `wip(jauntbox): mechanical wrap-up (<reason>)`, pushes `HEAD` to the same-named branch (`git push origin HEAD`), prints `pushed`; on failure prints `failed: <message>` and exits 1.
  - Mechanical template: title `# CONTINUATION (written by jauntbox)`, a line `The agent did not leave a continuation note before the run ended (<reason>).`, then `- Branch: …`, `- Run: $JAUNT_RUN`, and sections `## git status` (porcelain), `## Changes against origin/$JAUNT_DEFAULT_BRANCH` (`git diff --stat`; env `JAUNT_DEFAULT_BRANCH`), `## Last log lines` (last 40 lines of `/var/log/jauntbox/claude.jsonl` or `init.log`, whichever exists), each in a fenced block.
- Dockerfile: `FROM ubuntu:24.04`; packages `git ca-certificates curl jq ripgrep less vim-tiny build-essential tini`; `mise` (official install script to `/usr/local/bin`); no fixed user (containers run with `--user <uid>:<gid>` and `HOME=/home/agent`); Claude Code installed by the native installer into `/opt/claude` (world-readable, symlinked as `/usr/local/bin/claude`); `ENV DISABLE_AUTOUPDATER=1`; `/etc/gitconfig` with `credential.https://github.com.helper=/usr/local/bin/jaunt-git-credential`, `credential.https://github.com.useHttpPath=true`, `push.default=current`, `safe.directory=/work`; `managed-settings.json` copied to `/etc/claude-code/managed-settings.json` (root, 0644); scripts to `/usr/local/bin` (0755); `WORKDIR /work`, `ENTRYPOINT ["/usr/local/bin/jaunt-agent-init"]`, `CMD ["sleep","infinity"]`.
- `managed-settings.json`: `PreToolUse` (matcher `*`) → `/usr/local/bin/jaunt-hook pretool`; `Stop` → `/usr/local/bin/jaunt-hook stop`; `env.DISABLE_AUTOUPDATER = "1"`.

- [ ] **Step 1: Failing shell tests** in `provision/agent/agent_test.sh` (bash, real `git`, `jq`; a bare repo in a temp dir stands in for GitHub; scripts take `JAUNT_RUN_DIR` (default `/run/jauntbox-run`), `JAUNT_WORK` (default `/work`) and `JAUNT_LOG_DIR` (default `/var/log/jauntbox`) overrides for testing):
  - credential: token present → exact two lines; empty/missing → `quit=1`; `store` → no output.
  - hook: no flag → no output for both; flag + pretool → JSON with the exact instruction including the reason, second call with the same session → no output; stop with dirty tree → block JSON; stop with `stop_hook_active: true` → no output; stop after CONTINUATION committed and pushed → no output.
  - fallback: dirty tree, no note → `pushed`, remote branch has the WIP commit and a CONTINUATION.md with the title line and the reason; second run → `already-done`; unreachable remote → `failed: …`, exit 1.
  - init: on a feature branch without `JAUNT_SWITCH_TO` → stays; with `JAUNT_SWITCH_TO=agent/x-20261110` → switched, `.git/config` unchanged (compare bytes before/after), uncommitted file still present; a refused switch → non-zero exit and git's message in `init.log`.
  - checkout guard: with `.git/commondir` containing `.` and `chmod a-w .git/config` (stand-in for the read-only mount), `git status`, `git commit`, `git switch --no-track -c`, `git push origin HEAD` all work; `git push -u` reports the config error but the push lands.
- [ ] **Step 2: Failing Go tests** `TestTagStableAndContentAddressed` (same bytes → same 12-hex tag; changing one script changes it), `TestContextHasDockerfileAndExecutableScripts` (tar entries, modes), `TestEnsureBuildsOnlyWhenMissing` (fake Docker: exists → no Build; missing → one Build with `Tag()`).
- [ ] **Step 3:** Run both → FAIL. **Step 4:** Implement scripts, Dockerfile, settings and `agentimage`. **Step 5:** `bash provision/agent/agent_test.sh` and `go test ./...` → PASS; `shellcheck` the scripts if available.
- [ ] **Step 6: Commit** `feat(jauntbox): agent image with wrap-up hooks and in-container git helpers`.

### Task 3: Network guard and host git settings

**Files:**
- Create: `internal/agentrun/netguard.go`, `internal/agentrun/netguard_test.go`, `internal/cloudinit/units/jauntbox-agent-net.service`
- Modify: `internal/cloudinit/render.go` (install the unit), `internal/cloudinit/render_test.go`, `cmd/jauntbox/main.go` (box command `agent-net`, hidden), `internal/client/push.go` (`installCmd`), `internal/client/*_test.go`

**Interfaces:**
- Produces: `type Runner func(ctx context.Context, name string, args ...string) error`; `agentrun.EnsureNetGuard(ctx context.Context, run Runner) error` — for each rule, `-C DOCKER-USER <rule>` and, if that fails, `-I DOCKER-USER 1 <rule>`; rules: `iptables -d 169.254.169.254/32 -j DROP`, `iptables -d 100.64.0.0/10 -j DROP`, `ip6tables -d fd7a:115c:a1e0::/48 -j DROP`. An `-I` failure is returned (callers fail closed). `jauntctl agent-net` calls it (root required).
- Unit: `Type=oneshot`, `After=docker.service`, `Requires=docker.service`, `ExecStart=/usr/local/bin/jauntctl agent-net`, `RemainAfterExit=yes`, `WantedBy=multi-user.target`; enabled by the same install command that enables the supervisor timer (Plan 02 `installCmd` in `internal/client/push.go`: append `&& systemctl enable --now jauntbox-agent-net.service`; `installCmd` runs as root since 03a, so no `sudo`). The same `installCmd` sets the box user's global git settings from Global Constraints (`git config --global diff.ignoreSubmodules all`, `status.submoduleSummary false`, `submodule.recurse false`, `core.fsmonitor false`).

- [ ] **Step 1: Failing tests:** `TestNetGuardInsertsMissingRules` (fake runner: all `-C` fail → three `-I` calls with exact args), `TestNetGuardIdempotent` (all `-C` succeed → no `-I`), `TestNetGuardFailsClosed` (an `-I` error is returned), `TestRenderInstallsAgentNetUnit`, `TestInstallCmdSetsHostGitGuards` (the four `git config --global` settings and the unit enable are in `installCmd`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): block metadata and tailnet from agent containers; guard host git`.

### Task 4: Starting a run

**Files:**
- Create: `internal/agent/adapter.go`, `internal/agent/claude.go`, `internal/agent/claude_test.go` (agent adapter interface, spec §6.3 and §10)
- Create: `internal/agentrun/run.go` (Run record, paths, store), `internal/agentrun/checkout.go` (find and validate the checkout, parse its origin), `internal/agentrun/docker.go` (CLI-backed `Docker`), `internal/agentrun/start.go`, tests `internal/agentrun/run_test.go`, `internal/agentrun/checkout_test.go`, `internal/agentrun/start_test.go`

**Interfaces:**
- Consumes: `agentimage.Ensure/Tag` (Task 2), `EnsureNetGuard` (Task 3), `minter.Minter.Mint/Repos` (Plan 02), `vault.Dir`, `vault.ErrLocked`, `config.Agent` via `boxinfo` (Task 1).
- Produces:
  - `agent.Adapter` interface: `Name() string`; `Credentials() []string` (vault file names to inject as env vars); `InteractiveCmd(resume, continueSession, skipPermissions bool) []string`; `UnattendedCmd() []string`. `agent.ClaudeCode{}` returns `["CLAUDE_CODE_OAUTH_TOKEN"]`, interactive `["claude"]` plus `--dangerously-skip-permissions` when asked, then `--continue` or the `ResumePrompt` (exclusive; continue wins), unattended `["jaunt-unattended"]`. `agent.ResumePrompt = "Read CONTINUATION.md at the repository root and continue from it."`. `Starter` and the attach command take the adapter; only `ClaudeCode` exists in v1.
  - `agentrun.FindCheckout(dir, home string) (Checkout, error)` with `Checkout{Root, Repo, Branch string; Detached bool}`: walks up from `dir` to the first directory holding `.git`; `.git` must be a directory (a file means a linked worktree or submodule: refuse); after `filepath.EvalSymlinks`, `Root` must be inside `home`; `Repo` = `owner/repo` parsed from `[remote "origin"] url` in `.git/config` (accepts `https://github.com/o/r(.git)` and `git@github.com:o/r(.git)`, else an error naming the URL); `Branch`/`Detached` from `.git/HEAD`. Pure file parsing, never runs git.
  - `type Run struct { ID, Repo, Checkout, Branch, DefaultBranch, Mode string; Prompt string; StartedAt time.Time; Max time.Duration; WrapUpAt time.Time; WrapUpReason string; FinishedAt time.Time; ExitCode int }` stored as `run.json`; `Mode` is `"interactive"` or `"unattended"`.
  - `type Paths struct { RunsTmp, RunsVol, Agents string }` with `DefaultPaths()` = `/run/jauntbox/runs`, `/home/.jauntbox/runs`, `/home/.jauntbox/agents`; `(Paths) Load(id) (Run, error)`, `(Paths) Save(Run) error`, `(Paths) All() ([]Run, error)`.
  - `type Docker interface` = Task 2's plus `NetworkCreate(ctx, name string, labels map[string]string) error`, `NetworkRemove(ctx, name string) error`, `Run(ctx, spec ContainerSpec) error`, `Exec(ctx context.Context, container string, user string, args ...string) ([]byte, error)`, `Inspect(ctx, name string) (State, error)` (`State{Exists, Running bool; ExitCode int; IP string}`), `Logs(ctx, name string, tail int) (string, error)`, `Remove(ctx, name string) error` (force), `Stop(ctx, name string, timeout time.Duration) error`, `ListRuns(ctx) ([]string, error)` (ids from `label=jauntbox.agent=1`, including exited).
  - `type StartOpts struct { Dir, Branch, Prompt string; Max time.Duration; SkipPermissions bool }` (`Dir` = the caller's working directory); `type Starter struct { Docker Docker; Mint func(ctx, repo string) (forge.Token, error); Repos func(ctx) ([]forge.Repo, error); Net Runner; Paths Paths; SecretsDir string; User string; UID, GID int; GitConfig func(key string) (string, error); Now func() time.Time; Loc *time.Location; Events events.Sink; ReadyWait time.Duration }`; `(*Starter) Start(ctx, StartOpts) (Run, error)`; `Starter` also has `Agent agent.Adapter`, and the credential checks and env come from `Agent.Credentials()`.
- `ContainerSpec` produces exactly: `docker run -d --name jauntbox-<id> --hostname <repo> --network jauntbox-<id> --user <uid>:<gid> --cap-drop ALL --security-opt no-new-privileges --init --pids-limit 4096 --label …(3) --env-file <tmp> -e HOME=/home/agent -v <checkout>:/work -v <checkout>/.git/config:/work/.git/config:ro -v <checkout>/.git/hooks:/work/.git/hooks:ro [-v <checkout>/.git/modules:/work/.git/modules:ro] -v <checkout>/.git/commondir:/work/.git/commondir:ro -v <home>:/home/agent -v <runtmp>:/run/jauntbox-run:ro -v <log>:/var/log/jauntbox <image> [jaunt-unattended]`. The env file (`/run/jauntbox/runs/<id>.env`, 0600, beside — not inside — the mounted run dir, deleted right after `docker run` returns) holds `CLAUDE_CODE_OAUTH_TOKEN`, `JAUNT_REPO`, `JAUNT_RUN`, `JAUNT_DEFAULT_BRANCH`, `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`, `JAUNT_SWITCH_TO` when switching, and for unattended `JAUNT_PROMPT`. Before `docker run`, as root: create `<checkout>/.git/hooks` if missing and `<checkout>/.git/commondir` with content `.` if missing (both owned by the user), and refuse a checkout whose `.git/commondir` exists with other content (name the file and say to remove it).
- `Start` order: vault unlocked (marker present, else `vault.ErrLocked`) and `CLAUDE_CODE_OAUTH_TOKEN` present (else `missing agent credential CLAUDE_CODE_OAUTH_TOKEN: run \`jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN\` then \`jauntbox unlock\` on your laptop`) → `FindCheckout(Dir, /home/<user>)` → the repo must be in `Repos` (else `the App jauntbox-<user> is not installed on <owner/repo>; add it at <install URL>`) → no active run for the checkout (a run with the same `Checkout`, container present, `FinishedAt` zero → error `run <id> is already active in <checkout>; reopen it with \`jauntctl agent attach\``) → git identity → `EnsureNetGuard` → `agentimage.Ensure` → dirs with the owners/modes from Global Constraints → token file → network → container → wait until `Exec(test -f /tmp/jauntbox-ready)` succeeds or the container stops or `ReadyWait` (default 3 min) passes; on failure return an error containing the last 20 lines of `/var/log/jauntbox/init.log` and remove container, network and tmpfs run dir (keep the volume log) → `Save` → emit `agent.started` (`run`, `repo`, `branch`, `mode`).
- Branch (ruling 4b): `DefaultBranch` from `Repos`; `--branch` equal to the default branch is refused; `JAUNT_SWITCH_TO` = `--branch`, else `agent/<repo>-<YYYYMMDD>` when the checkout is on the default branch or detached, else unset; `Run.Branch` is the branch the run ends up on.

- [ ] **Step 1: Failing tests** with a fake `Docker`, fake mint/repos, temp `Paths` and secrets dir (plus `TestClaudeCodeAdapter` in `internal/agent`: credentials, interactive argv for each flag combination, unattended argv):
  - `TestStartHappyPath`: exact `ContainerSpec` args (your uid, caps, the checkout mount and all four read-only guard mounts, `:ro` on the run dir, labels, network), `.git/commondir` created with `.`, env file content and its removal, token file content `ghs_x` and modes, `run.json` saved, `agent.started` emitted.
  - `TestFindCheckout` (in `checkout_test.go`): from a subdirectory finds the root; https and ssh origin URLs; non-GitHub origin; `.git` file refused; outside home (including via a symlink) refused; detached HEAD.
  - `TestStartBranchChoice`: feature branch → no switch; default branch → `agent/<repo>-<date>`; detached → same; `--branch` → that branch; `--branch <default>` refused.
  - `TestStartRefusesForeignCommondir`; `TestStartModulesMountOnlyWhenPresent`.
  - `TestStartUnattendedPassesPrompt`: spec ends with `jaunt-unattended`, env has `JAUNT_PROMPT`.
  - Review Focus 1: `TestStartLocked`, `TestStartMissingClaudeToken` — errors as above, fake Docker saw no calls.
  - Review Focus 2: `TestStartNotACheckout`, `TestStartRepoNotInstalled` (error has the install URL), nothing created.
  - Review Focus 3: `TestStartRefusesSecondRunInCheckout`, `TestStartAllowsRunsInTwoCheckouts`.
  - Review Focus 4: `TestStartInitFailureCleansUp` — container stops before ready → error contains log tail, `Remove`/`NetworkRemove` called, tmpfs dir gone.
  - `TestStartNetGuardFailureRefuses`, `TestStartRefusesDefaultBranch`, `TestRunIDFormat`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement `run.go`, `start.go`, then `docker.go` (thin `exec.CommandContext("docker", …)` wrappers; `Build` pipes the tar to `docker build -t <tag> -`; unit-test only argument building with a recorded command runner).
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): start agent runs in isolated containers`.

### Task 5: `jauntctl agent` commands on the box

**Files:**
- Create: `cmd/jauntbox/agent.go`, `internal/agentrun/list.go`, `internal/agentrun/stop.go`, tests `cmd/jauntbox/agent_test.go`, `internal/agentrun/list_test.go`, `internal/agentrun/stop_test.go`
- Modify: `cmd/jauntbox/main.go` (register under the `jauntctl` root)

**Interfaces:**
- Consumes: Task 4.
- Produces:
  - `jauntctl agent [--branch b] [--unattended "<prompt>"] [--max 8h] [--skip-permissions]`, run inside a checkout: runs as the box user. It runs the root part as `sudo -n /usr/local/bin/jauntctl agent start-run --dir <cwd> <same flags> --json` (hidden subcommand: `Start`, prints the `Run` as JSON; same sudo pattern as `sudoMint`); when the run switched branches it prints `switched <checkout> to <branch>`. Interactive: then `syscall.Exec` of the attach below in the same terminal, so Claude opens in the pane you typed it in. Unattended: prints `run <id> started; watch it with: jauntctl agent logs --follow`.
  - `jauntctl agent attach [<id>] [--shell]`: re-execs through `sudo -n` (Plan 03a: no docker group); without an id it uses the active run of the checkout you are in. `docker exec -it -w /work jauntbox-<id>` (the container already runs as your uid) + the adapter's `InteractiveCmd(resume = /work/CONTINUATION.md exists, continueSession, skipPermissions from run.json)`: the first attach of a run has `continueSession = false`, later ones `true` (a marker `first-attach-done` in the volume run dir records it); `--shell` runs `bash`.
  - `jauntctl agent list [--json]`: `agentrun.List(ctx, d Docker, p Paths) ([]Status, error)` with `Status{Run; State string; IP string}`; `State` ∈ `running`, `wrapping-up`, `finished`, `gone` (record without container). Table columns: `RUN REPO BRANCH MODE STATE STARTED MAX IP`.
  - `jauntctl agent stop [<id>] [--now]` (no id: the run of the current checkout): root; `agentrun.RequestWrapUp(p Paths, id, reason string, now time.Time) error` writes the run's `wrapup` flag (reason `"stop-requested"`) and sets `WrapUpAt`; `--now` additionally calls `agentrun.Finish(ctx, d, p, id, ev) error` (fallback via `docker exec` only while the container is running — an exited or missing container gets none —, then stop/remove container and network, remove tmpfs dir, set `FinishedAt`, emit `agent.stopped` with `fallback` = script output).
  - `jauntctl agent logs [<id>] [--follow]` (no id: the run of the current checkout): shows `/home/.jauntbox/runs/<id>/log/claude.jsonl` (root; re-execs with `sudo -n`), following with `--follow`.
- `jauntctl agent` typed on the laptop prints the existing wrong-side message (agents start on the box).

- [ ] **Step 1: Failing tests:** `TestListStates` (each of the four states from fake inspect + records; Review Focus 5's `gone`), `TestRequestWrapUpWritesFlag`, `TestFinishRunsFallbackBeforeStop` (call order `exec jaunt-wrapup-fallback` → `stop` → `remove` → `network rm`; event fields), `TestFinishWhenContainerGone` (no fallback, record finished, no error), `TestAgentRunsRootPartWithSudo` (argv of `start-run` carries `--dir <cwd>`), `TestAgentInteractiveExecsAttach` (after `start-run`, the exec'd argv is the attach), `TestAttachArgvFirstAndLater` (resume prompt on first attach when CONTINUATION.md exists, `--continue` afterwards), `TestAttachWithoutIDUsesCheckoutRun`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): jauntctl agent, attach, list, stop and logs`.

### Task 6: Supervisor drives the runs

**Files:**
- Create: `internal/agentrun/maintain.go`, `internal/agentrun/maintain_test.go`
- Modify: `internal/supervisor/run.go` (new `Runs` hook), `internal/supervisor/run_test.go`

**Interfaces:**
- Consumes: Tasks 4–5, `config.Limits.WrapUpGrace`.
- Produces:
  - `type Maintainer struct { Docker Docker; Paths Paths; Mint func(ctx, repo string) (forge.Token, error); Events events.Sink; Grace time.Duration }`.
  - `(*Maintainer) Tick(ctx, now time.Time) error` — per run record without `FinishedAt`: container gone or exited → finish (Review Focus 5: no fallback for a gone container; exited unattended → `agent.finished` with `exit_code`); running → refresh the token file when `Mint` returns a different token (write via temp + rename, mode 0640 root:<user gid>; a mint error is reported as `agent.token_refresh_failed` and does not stop the tick); `now >= StartedAt+Max` and no `WrapUpAt` → `RequestWrapUp` with reason `run-max` + `agent.wrapup_started`; `WrapUpAt` set and `now >= WrapUpAt+Grace` → `Finish`.
  - `(*Maintainer) WrapUpAll(now time.Time, reason string) error` — `RequestWrapUp` for every active run that has none.
  - `(*Maintainer) FinishAll(ctx) error` — `Finish` for every active run.
  - `Supervisor.Runs` (nil = no-op) with these three methods behind an interface `RunManager`; `RunOnce` calls `Tick` every pass, `WrapUpAll(now, v.Reason)` on `StartWrapUp`, and on `Delete` the order **FinishAll → Revoke → emit box.deleted → Sync → DeleteSelf**. A `FinishAll` error is emitted (`agent.finish_failed`) and does not block the delete.
- `New(info)` wires a real `Maintainer` (`Grace` from `info.Limits.WrapUpGrace`).

- [ ] **Step 1: Failing tests:** `TestTickRefreshesTokenOnlyWhenChanged`, `TestTickRunMaxStartsWrapUp`, `TestTickGraceElapsedFinishes`, `TestTickGoneContainerFinishesWithoutFallback`, `TestTickExitedUnattendedRecordsExitCode`, `TestTickMintFailureContinues`; in `run_test.go`: `TestDeleteOrderFinishRevokeSyncDelete` (exact call order), `TestStartWrapUpFlagsAllRuns`, `TestFinishAllFailureDoesNotBlockDelete`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`; `sh internal/cloudinit/units/safety-net_test.sh`.
- [ ] **Step 5: Commit** `feat(jauntbox): supervisor refreshes, wraps up and finishes agent runs`.

### Task 7: Docs and smoke checks

**Files:**
- Modify: `README.md` (new "Agents" section after "GitHub and secrets"), `docs/jauntbox/SMOKE.md` (new "Plan 03: agent containers" section before "What good looks like")

- [ ] **Step 1: README "Agents"**: the commands from Task 5 with one-line descriptions; the flow `jauntbox` → herdr → `cd ~/src/<repo>` → `jauntctl agent` (ruling 4); several repos at once, one run per checkout (ruling 3); the checkout guard and what it does not cover (ruling 2), with "run agent-changed tests inside the container"; what a container holds and cannot reach (Global Constraints, ruling 9); wrap-up and `CONTINUATION.md` (ruling 5, grace, mechanical note); resume (ruling 6); one run per repo; host git refuses agent workspaces by design — use `jauntctl agent attach --shell`; first `start` builds the image (minutes); `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`; the host user has password sudo and is trusted, agents never run on the host.
- [ ] **Step 2: SMOKE "Plan 03"**, each with what to record:
  1. Image build time on first `start`; `docker image ls jauntbox/agent`.
  2. Isolation, from `jauntctl agent attach <id> --shell`: `id -u` is your uid; `echo x >> .git/config`, `touch .git/hooks/x` and `rm .git/commondir` fail (read-only); `curl -m 5 http://169.254.169.254/hetzner/v1/metadata` fails; `curl -m 5 http://100.100.100.100` fails; `ls /run/jauntbox/secrets` fails; `cat /run/jauntbox-run/token` shows only a token; `git push` works on the agent branch; pushing to another installed repo with that token fails (403).
  3. Interactive: `jauntbox`, then in the repo's herdr pane (`~/src/<repo>`) `jauntctl agent`: Claude opens in that pane. Start a second agent in another repo's pane at the same time; both run. Record what Claude asked on the very first run (onboarding). Close the pane; in another pane `jauntctl agent attach <id>`: Claude continues (`--continue`). Record whether herdr shows the pane as an agent.
  4. Wrap-up: `jauntctl agent stop <id>` during an interactive task → Claude receives the instruction, writes, commits and pushes `CONTINUATION.md`; record the hook output seen in the session.
  5. Fallback: `jauntctl agent stop <id>` with Claude idle → after the grace, a mechanical `CONTINUATION.md` is pushed.
  6. Unattended with `--max 10m`: the run wraps up at 10 min; `agent.*` events in `events.jsonl`.
  7. Resume: `jauntctl agent --branch <that branch>` in the same checkout opens with the resume prompt. On the host, `git status` and `git log` in the checkout work normally after the run, and `cat .git/commondir` shows `.`.
  8. Teardown with a running agent: `jauntbox down` → fallback pushed, tokens revoked, box deleted; event order in the next box's log.
- [ ] **Step 3: Commit** `docs(jauntbox): agent containers usage and smoke checks`.
