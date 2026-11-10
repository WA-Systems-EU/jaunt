# jauntbox 03: agent containers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents (Claude Code first) run only inside per-run containers on the box, each holding one repo's checkout, its own agent credential and a token for that repo only; forced stops wrap them up into a pushed `CONTINUATION.md`.

**Architecture:** A root-run `jauntctl agent start-run` builds (once) an embedded agent image, prepares per-run directories, writes the repo's App token into a tmpfs file mounted read-only into the container, and starts the container as a dedicated unprivileged uid on its own Docker network. The container clones the repo itself, so the host never runs git in agent-writable directories. You start runs from a herdr pane on the box (`jauntctl agent <repo>`), and Claude runs in that pane. The supervisor refreshes run tokens each tick, enforces each run's max duration, raises a wrap-up flag that managed Claude Code hooks turn into an instruction, and runs a mechanical in-container fallback before stopping containers and revoking tokens.

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
- Tests never call real Docker, iptables, GitHub, Hetzner or Tailscale: fakes behind interfaces, shell scripts tested with stubs.
- Commits: plain conventional messages, no attribution trailers.
- Exact values used across tasks:
  - agent uid/gid **64000**, user `agent`, home `/home/agent`, workdir `/work`
  - workspace on volume: `/home/.jauntbox/agents/<owner>/<repo>/work` → `/work`; agent home: `/home/.jauntbox/agents/<owner>/<repo>/home` → `/home/agent` (both owned 64000:64000, mode 0750)
  - run state (tmpfs): `/run/jauntbox/runs/<id>/` (root:64000, 0750) → `/run/jauntbox-run` read-only; files `token` (root:64000 0640), `wrapup` (0644, content = reason)
  - run record and logs (volume): `/home/.jauntbox/runs/<id>/run.json` (root, 0600) and `/home/.jauntbox/runs/<id>/log/` (64000, 0750) → `/var/log/jauntbox`
  - run id: `<repo name lowercased>-<UTC YYYYMMDD-HHMMSS>`; container and network name `jauntbox-<id>`
  - labels: `jauntbox.agent=1`, `jauntbox.run=<id>`, `jauntbox.repo=<owner>/<repo>`
  - image tag `jauntbox/agent:<first 12 hex of sha256 over the embedded build context>`
  - default branch name `agent/<repo>-<YYYYMMDD>` (local date in the box timezone)

## Rulings (decisions the spec leaves to the builder)

1. **Token delivery: a file, not a broker.** The supervisor (every tick) and `agent start` write the current token to the run's `token` file; the in-image credential helper reads it. No socket daemon. Cost if wrong: a broker later replaces one writer.
2. **The container clones its own checkout as uid 64000.** The host never runs git in agent directories, and host git refuses them ("dubious ownership") because the owner differs — the owner works with agents inside the container (spec: herdr panes attach into containers). Do not add `safe.directory` for them. Cost if wrong: owner edits from the host need `docker exec`.
3. **One active run per repo.** A second `jauntctl agent` on the same repo refuses and names the run to attach to. Cost if wrong: parallel runs later need per-run workspaces.
4. **You start agents from herdr.** `jauntbox` gets you into the box's herdr session; in any pane, `jauntctl agent <repo>` starts the run and Claude runs right there in that pane. `jauntctl agent attach <id>` reopens it in any pane (Claude continues with `--continue`). Interactive runs keep the container alive (`sleep infinity` under `--init`) and Claude runs through `docker exec -it`, so closing the pane doesn't end the run. Unattended runs make `claude -p` the main process; `jauntctl agent logs --follow <id>` watches one in a pane. There is no laptop-side agent command. herdr can't see Claude's working/idle status through `docker exec`; reporting it to herdr is left for later.
5. **Hooks live in the image's managed settings** (`/etc/claude-code/managed-settings.json`), which the repo and the agent (non-root) cannot change: `PreToolUse` injects the wrap-up instruction once per session via `additionalContext`; `Stop` blocks stopping (`decision: block`) until `CONTINUATION.md` is committed and pushed, unless `stop_hook_active` is true.
6. **Resume** passes an opening prompt (`Read CONTINUATION.md at the repository root and continue from it.`) when the branch already has `CONTINUATION.md`; no SessionStart hook.
7. **Permissions:** unattended runs pass `--dangerously-skip-permissions` (the container is the sandbox; it is refused as root, and we run as 64000). Interactive runs use Claude's default mode unless `--skip-permissions` is given.
8. **Default image only in v1:** project Dockerfiles, dotfile mounts and accessory services (compose) move to Plan 04. Toolchains come from `mise` inside the container (`mise install` when the repo has `.tool-versions` / `mise.toml`), cached in the agent home on the volume.
9. **Network guard:** `DOCKER-USER` drops container traffic to `169.254.169.254/32` and `100.64.0.0/10`, and (ip6tables) to `fd7a:115c:a1e0::/48`. Applied by a boot unit and re-checked by every `agent start`, which refuses to start if it cannot apply them.
10. **Git identity** comes from the box user's `~/.gitconfig` (`user.name`, `user.email`); missing → `start` refuses with the fix (`git config --global user.name …` on the box).
11. **Previews:** the host reaches a container by its network IP; `agent list` prints it. No published ports.

## Review Focus

1. A locked box or a missing `CLAUDE_CODE_OAUTH_TOKEN` → `start` refuses with the unlock / `jauntbox secrets set` message and leaves nothing behind (Task 4 test).
2. The repo argument matches no installed repo, or several (`thomas` in two owners) → clear error listing the candidates, nothing created (Task 4 test).
3. A second `jauntctl agent` on a repo that already has a run → refused, names the existing run (Task 4 test).
4. The container dies during init (clone fails, bad branch name) → `start` reports the tail of the init log and removes container, network and run dirs (Task 4 test).
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
  - `jaunt-agent-init <cmd…>`: needs env `JAUNT_REPO` (owner/repo), `JAUNT_BRANCH`, `JAUNT_DEFAULT_BRANCH`, `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`. Configures git (`credential.helper=/usr/local/bin/jaunt-git-credential`, identity also as committer), clones `https://github.com/$JAUNT_REPO.git` into `/work` if empty, fetches, checks out `$JAUNT_BRANCH` (tracking the remote branch if it exists, else new from `origin/$JAUNT_DEFAULT_BRANCH`, pushed with `-u`), runs `mise install` when `/work/.tool-versions` or `/work/mise.toml` exists, touches `/tmp/jauntbox-ready`, then `exec "$@"`. Output goes to `/var/log/jauntbox/init.log`.
  - `jaunt-git-credential get`: prints `username=x-access-token` / `password=<contents of /run/jauntbox-run/token>`; missing or empty token → prints `quit=1`. Any other op → exit 0 silently.
  - `jaunt-hook pretool|stop`: reads hook JSON on stdin. No `/run/jauntbox-run/wrapup` → exit 0, no output. `pretool`: once per `session_id` (marker `/tmp/jauntbox-wrapup-told-<session_id>`) prints `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":<instruction>}}`. `stop`: unless done (CONTINUATION.md exists in `/work`, `git status --porcelain` empty, `git rev-list @{u}..HEAD` empty) and unless `stop_hook_active` is true, prints `{"decision":"block","reason":<instruction>}`.
  - Instruction text, verbatim, with `<reason>` from the flag file: `jauntbox is wrapping up this run (<reason>). Stop the current task now. Write CONTINUATION.md at the repository root with: what you were doing, the current state, next steps, and open questions for the owner. Commit it together with your work to the current branch and push. Do not start new work.`
  - `jaunt-unattended`: runs `claude -p "$JAUNT_PROMPT" --output-format stream-json --verbose --dangerously-skip-permissions` in `/work`, stdout to `/var/log/jauntbox/claude.jsonl`, stderr to `/var/log/jauntbox/claude.err`, exits with Claude's status.
  - `jaunt-wrapup-fallback <reason>`: in `/work`; if done (as above) prints `already-done`; else writes `CONTINUATION.md` if absent (template below), `git add -A`, commits `wip(jauntbox): mechanical wrap-up (<reason>)`, pushes `HEAD:$JAUNT_BRANCH`, prints `pushed`; on failure prints `failed: <message>` and exits 1.
  - Mechanical template: title `# CONTINUATION (written by jauntbox)`, a line `The agent did not leave a continuation note before the run ended (<reason>).`, then `- Branch: …`, `- Run: $JAUNT_RUN`, and sections `## git status` (porcelain), `## Changes against origin/$JAUNT_DEFAULT_BRANCH` (`git diff --stat`), `## Last log lines` (last 40 lines of `/var/log/jauntbox/claude.jsonl` or `init.log`, whichever exists), each in a fenced block.
- Dockerfile: `FROM ubuntu:24.04`; packages `git ca-certificates curl jq ripgrep less vim-tiny build-essential tini`; `mise` (official install script to `/usr/local/bin`); user `agent` 64000:64000 with home `/home/agent`; Claude Code via `curl -fsSL https://claude.ai/install.sh | bash` as `agent`, with `/home/agent/.local/bin` on `PATH`; `ENV DISABLE_AUTOUPDATER=1`; `managed-settings.json` copied to `/etc/claude-code/managed-settings.json` (root, 0644); scripts to `/usr/local/bin` (0755); `USER agent`, `WORKDIR /work`, `ENTRYPOINT ["/usr/local/bin/jaunt-agent-init"]`, `CMD ["sleep","infinity"]`. Because the home is a volume mount over `/home/agent`, install Claude to `/opt/claude` (symlink into `/usr/local/bin/claude`) rather than relying on the image's home.
- `managed-settings.json`: `PreToolUse` (matcher `*`) → `/usr/local/bin/jaunt-hook pretool`; `Stop` → `/usr/local/bin/jaunt-hook stop`; `env.DISABLE_AUTOUPDATER = "1"`.

- [ ] **Step 1: Failing shell tests** in `provision/agent/agent_test.sh` (bash, real `git`, `jq`; a bare repo in a temp dir stands in for GitHub; scripts take `JAUNT_RUN_DIR` (default `/run/jauntbox-run`), `JAUNT_WORK` (default `/work`) and `JAUNT_LOG_DIR` (default `/var/log/jauntbox`) overrides for testing):
  - credential: token present → exact two lines; empty/missing → `quit=1`; `store` → no output.
  - hook: no flag → no output for both; flag + pretool → JSON with the exact instruction including the reason, second call with the same session → no output; stop with dirty tree → block JSON; stop with `stop_hook_active: true` → no output; stop after CONTINUATION committed and pushed → no output.
  - fallback: dirty tree, no note → `pushed`, remote branch has the WIP commit and a CONTINUATION.md with the title line and the reason; second run → `already-done`; unreachable remote → `failed: …`, exit 1.
- [ ] **Step 2: Failing Go tests** `TestTagStableAndContentAddressed` (same bytes → same 12-hex tag; changing one script changes it), `TestContextHasDockerfileAndExecutableScripts` (tar entries, modes), `TestEnsureBuildsOnlyWhenMissing` (fake Docker: exists → no Build; missing → one Build with `Tag()`).
- [ ] **Step 3:** Run both → FAIL. **Step 4:** Implement scripts, Dockerfile, settings and `agentimage`. **Step 5:** `bash provision/agent/agent_test.sh` and `go test ./...` → PASS; `shellcheck` the scripts if available.
- [ ] **Step 6: Commit** `feat(jauntbox): agent image with wrap-up hooks and in-container git helpers`.

### Task 3: Network guard for agent containers

**Files:**
- Create: `internal/agentrun/netguard.go`, `internal/agentrun/netguard_test.go`, `internal/cloudinit/units/jauntbox-agent-net.service`
- Modify: `internal/cloudinit/render.go` (install the unit), `internal/cloudinit/render_test.go`, `cmd/jauntbox/main.go` (box command `agent-net`, hidden)

**Interfaces:**
- Produces: `type Runner func(ctx context.Context, name string, args ...string) error`; `agentrun.EnsureNetGuard(ctx context.Context, run Runner) error` — for each rule, `-C DOCKER-USER <rule>` and, if that fails, `-I DOCKER-USER 1 <rule>`; rules: `iptables -d 169.254.169.254/32 -j DROP`, `iptables -d 100.64.0.0/10 -j DROP`, `ip6tables -d fd7a:115c:a1e0::/48 -j DROP`. An `-I` failure is returned (callers fail closed). `jauntctl agent-net` calls it (root required).
- Unit: `Type=oneshot`, `After=docker.service`, `Requires=docker.service`, `ExecStart=/usr/local/bin/jauntctl agent-net`, `RemainAfterExit=yes`, `WantedBy=multi-user.target`; enabled by the same install command that enables the supervisor timer (Plan 02 `installCmd` in `internal/client/push.go`: append `&& sudo systemctl enable --now jauntbox-agent-net.service`).

- [ ] **Step 1: Failing tests:** `TestNetGuardInsertsMissingRules` (fake runner: all `-C` fail → three `-I` calls with exact args), `TestNetGuardIdempotent` (all `-C` succeed → no `-I`), `TestNetGuardFailsClosed` (an `-I` error is returned), `TestRenderInstallsAgentNetUnit`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): block metadata and tailnet from agent containers`.

### Task 4: Starting a run

**Files:**
- Create: `internal/agent/adapter.go`, `internal/agent/claude.go`, `internal/agent/claude_test.go` (agent adapter interface, spec §6.3 and §10)
- Create: `internal/agentrun/run.go` (Run record, paths, store), `internal/agentrun/docker.go` (CLI-backed `Docker`), `internal/agentrun/start.go`, tests `internal/agentrun/run_test.go`, `internal/agentrun/start_test.go`

**Interfaces:**
- Consumes: `agentimage.Ensure/Tag` (Task 2), `EnsureNetGuard` (Task 3), `minter.Minter.Mint/Repos` (Plan 02), `vault.Dir`, `vault.ErrLocked`, `config.Agent` via `boxinfo` (Task 1).
- Produces:
  - `agent.Adapter` interface: `Name() string`; `Credentials() []string` (vault file names to inject as env vars); `InteractiveCmd(resume, continueSession, skipPermissions bool) []string`; `UnattendedCmd() []string`. `agent.ClaudeCode{}` returns `["CLAUDE_CODE_OAUTH_TOKEN"]`, interactive `["claude"]` plus `--dangerously-skip-permissions` when asked, then `--continue` or the `ResumePrompt` (exclusive; continue wins), unattended `["jaunt-unattended"]`. `agent.ResumePrompt = "Read CONTINUATION.md at the repository root and continue from it."`. `Starter` and the attach command take the adapter; only `ClaudeCode` exists in v1.
  - `type Run struct { ID, Repo, Branch, DefaultBranch, Mode string; Prompt string; StartedAt time.Time; Max time.Duration; WrapUpAt time.Time; WrapUpReason string; FinishedAt time.Time; ExitCode int }` stored as `run.json`; `Mode` is `"interactive"` or `"unattended"`.
  - `type Paths struct { RunsTmp, RunsVol, Agents string }` with `DefaultPaths()` = `/run/jauntbox/runs`, `/home/.jauntbox/runs`, `/home/.jauntbox/agents`; `(Paths) Load(id) (Run, error)`, `(Paths) Save(Run) error`, `(Paths) All() ([]Run, error)`.
  - `type Docker interface` = Task 2's plus `NetworkCreate(ctx, name string, labels map[string]string) error`, `NetworkRemove(ctx, name string) error`, `Run(ctx, spec ContainerSpec) error`, `Exec(ctx context.Context, container string, user string, args ...string) ([]byte, error)`, `Inspect(ctx, name string) (State, error)` (`State{Exists, Running bool; ExitCode int; IP string}`), `Logs(ctx, name string, tail int) (string, error)`, `Remove(ctx, name string) error` (force), `Stop(ctx, name string, timeout time.Duration) error`, `ListRuns(ctx) ([]string, error)` (ids from `label=jauntbox.agent=1`, including exited).
  - `type StartOpts struct { Repo, Branch, Prompt string; Max time.Duration; SkipPermissions bool }`; `type Starter struct { Docker Docker; Mint func(ctx, repo string) (forge.Token, error); Repos func(ctx) ([]forge.Repo, error); Net Runner; Paths Paths; SecretsDir string; GitConfig func(key string) (string, error); Now func() time.Time; Loc *time.Location; Events events.Sink; ReadyWait time.Duration }`; `(*Starter) Start(ctx, StartOpts) (Run, error)`; `Starter` also has `Agent agent.Adapter`, and the credential checks and env come from `Agent.Credentials()`.
- `ContainerSpec` produces exactly: `docker run -d --name jauntbox-<id> --hostname <repo> --network jauntbox-<id> --user 64000:64000 --cap-drop ALL --security-opt no-new-privileges --init --pids-limit 4096 --label …(3) --env-file <tmp> -v <work>:/work -v <home>:/home/agent -v <runtmp>:/run/jauntbox-run:ro -v <log>:/var/log/jauntbox <image> [jaunt-unattended]`. The env file (`/run/jauntbox/runs/<id>.env`, 0600, beside — not inside — the mounted run dir, deleted right after `docker run` returns) holds `CLAUDE_CODE_OAUTH_TOKEN`, `JAUNT_REPO`, `JAUNT_BRANCH`, `JAUNT_DEFAULT_BRANCH`, `JAUNT_RUN`, `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, and for unattended `JAUNT_PROMPT`.
- `Start` order: vault unlocked (marker present, else `vault.ErrLocked`) and `CLAUDE_CODE_OAUTH_TOKEN` present (else `missing agent credential CLAUDE_CODE_OAUTH_TOKEN: run \`jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN\` then \`jauntbox unlock\` on your laptop`) → resolve repo (`owner/repo` exact, or bare name matching exactly one installed repo; none/several → error listing installed `owner/repo`) → no active run for the repo (container exists and `FinishedAt` zero → error `run <id> is already active on <repo>; attach with \`jauntctl agent attach <id>\``) → git identity → `EnsureNetGuard` → `agentimage.Ensure` → dirs with the owners/modes from Global Constraints → token file → network → container → wait until `Exec(test -f /tmp/jauntbox-ready)` succeeds or the container stops or `ReadyWait` (default 3 min) passes; on failure return an error containing the last 20 lines of `/var/log/jauntbox/init.log` and remove container, network and tmpfs run dir (keep the volume log) → `Save` → emit `agent.started` (`run`, `repo`, `branch`, `mode`).
- Branch: `--branch` or `agent/<repo>-<YYYYMMDD>`; `DefaultBranch` from `Repos` (`forge.Repo.DefaultBranch`); refuse `--branch` equal to the default branch.

- [ ] **Step 1: Failing tests** with a fake `Docker`, fake mint/repos, temp `Paths` and secrets dir (plus `TestClaudeCodeAdapter` in `internal/agent`: credentials, interactive argv for each flag combination, unattended argv):
  - `TestStartHappyPath`: exact `ContainerSpec` args (uid, caps, mounts, `:ro` on the run dir, labels, network), env file content and its removal, token file content `ghs_x` and modes, `run.json` saved, `agent.started` emitted.
  - `TestStartUnattendedPassesPrompt`: spec ends with `jaunt-unattended`, env has `JAUNT_PROMPT`.
  - Review Focus 1: `TestStartLocked`, `TestStartMissingClaudeToken` — errors as above, fake Docker saw no calls.
  - Review Focus 2: `TestStartRepoResolution` — bare name unique → resolved; none → error lists installed repos; two owners → error lists both; nothing created.
  - Review Focus 3: `TestStartRefusesSecondRunOnRepo`.
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
  - `jauntctl agent <repo> [--branch b] [--unattended "<prompt>"] [--max 8h] [--skip-permissions]` (the parent command takes the repo; a repo whose name equals a subcommand is given as `owner/repo`): runs as the box user. It runs the root part as `sudo -n /usr/local/bin/jauntctl agent start-run <same args> --json` (hidden subcommand: `Start`, prints the `Run` as JSON; same sudo pattern as `sudoMint`). Interactive: then `syscall.Exec` of the attach below in the same terminal, so Claude opens in the pane you typed it in. Unattended: prints `run <id> started; watch it with: jauntctl agent logs --follow <id>`.
  - `jauntctl agent attach <id> [--shell]`: no root needed. `docker exec -it -u 64000:64000 -w /work jauntbox-<id>` + the adapter's `InteractiveCmd(resume = /work/CONTINUATION.md exists, continueSession, skipPermissions from run.json)`: the first attach of a run has `continueSession = false`, later ones `true` (a marker `first-attach-done` in the volume run dir records it); `--shell` runs `bash`.
  - `jauntctl agent list [--json]`: `agentrun.List(ctx, d Docker, p Paths) ([]Status, error)` with `Status{Run; State string; IP string}`; `State` ∈ `running`, `wrapping-up`, `finished`, `gone` (record without container). Table columns: `RUN REPO BRANCH MODE STATE STARTED MAX IP`.
  - `jauntctl agent stop <id> [--now]`: root; `agentrun.RequestWrapUp(p Paths, id, reason string, now time.Time) error` writes the run's `wrapup` flag (reason `"stop-requested"`) and sets `WrapUpAt`; `--now` additionally calls `agentrun.Finish(ctx, d, p, id, ev) error` (fallback via `docker exec` only while the container is running — an exited or missing container gets none —, then stop/remove container and network, remove tmpfs dir, set `FinishedAt`, emit `agent.stopped` with `fallback` = script output).
  - `jauntctl agent logs <id> [--follow]`: shows `/home/.jauntbox/runs/<id>/log/claude.jsonl` (root; re-execs with `sudo -n`), following with `--follow`.
- `jauntctl agent` typed on the laptop prints the existing wrong-side message (agents start on the box).

- [ ] **Step 1: Failing tests:** `TestListStates` (each of the four states from fake inspect + records; Review Focus 5's `gone`), `TestRequestWrapUpWritesFlag`, `TestFinishRunsFallbackBeforeStop` (call order `exec jaunt-wrapup-fallback` → `stop` → `remove` → `network rm`; event fields), `TestFinishWhenContainerGone` (no fallback, record finished, no error), `TestAgentRunsRootPartWithSudo` (argv of `start-run`), `TestAgentInteractiveExecsAttach` (after `start-run`, the exec'd argv is the attach), `TestAttachArgvFirstAndLater` (resume prompt on first attach when CONTINUATION.md exists, `--continue` afterwards), `TestAgentRepoNamedLikeSubcommand` (`jauntctl agent owner/list` starts a run).
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
  - `(*Maintainer) Tick(ctx, now time.Time) error` — per run record without `FinishedAt`: container gone or exited → finish (Review Focus 5: no fallback for a gone container; exited unattended → `agent.finished` with `exit_code`); running → refresh the token file when `Mint` returns a different token (write via temp + rename, mode 0640 root:64000; a mint error is reported as `agent.token_refresh_failed` and does not stop the tick); `now >= StartedAt+Max` and no `WrapUpAt` → `RequestWrapUp` with reason `run-max` + `agent.wrapup_started`; `WrapUpAt` set and `now >= WrapUpAt+Grace` → `Finish`.
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

- [ ] **Step 1: README "Agents"**: the commands from Task 5 with one-line descriptions; the flow `jauntbox` → herdr → `jauntctl agent <repo>` in a pane (ruling 4); what a container holds and cannot reach (Global Constraints, ruling 9); wrap-up and `CONTINUATION.md` (ruling 5, grace, mechanical note); resume (ruling 6); one run per repo; host git refuses agent workspaces by design — use `jauntctl agent attach --shell`; first `start` builds the image (minutes); `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`; the full-sudo host user is trusted, agents never run on the host.
- [ ] **Step 2: SMOKE "Plan 03"**, each with what to record:
  1. Image build time on first `start`; `docker image ls jauntbox/agent`.
  2. Isolation, from `jauntctl agent attach <id> --shell`: `id` is 64000; `curl -m 5 http://169.254.169.254/hetzner/v1/metadata` fails; `curl -m 5 http://100.100.100.100` fails; `ls /run/jauntbox/secrets` fails; `cat /run/jauntbox-run/token` shows only a token; `git push` works on the agent branch; pushing to another installed repo with that token fails (403).
  3. Interactive: `jauntbox`, then in a herdr pane `jauntctl agent <repo>`: Claude opens in that pane. Record what Claude asked on the very first run (onboarding). Close the pane; in another pane `jauntctl agent attach <id>`: Claude continues (`--continue`). Record whether herdr shows the pane as an agent.
  4. Wrap-up: `jauntctl agent stop <id>` during an interactive task → Claude receives the instruction, writes, commits and pushes `CONTINUATION.md`; record the hook output seen in the session.
  5. Fallback: `jauntctl agent stop <id>` with Claude idle → after the grace, a mechanical `CONTINUATION.md` is pushed.
  6. Unattended with `--max 10m`: the run wraps up at 10 min; `agent.*` events in `events.jsonl`.
  7. Resume: `jauntctl agent <repo> --branch <that branch>` opens with the resume prompt.
  8. Teardown with a running agent: `jauntbox down` → fallback pushed, tokens revoked, box deleted; event order in the next box's log.
- [ ] **Step 3: Commit** `docs(jauntbox): agent containers usage and smoke checks`.
