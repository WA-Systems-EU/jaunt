# jauntbox 04: your services, for agents — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The services you start yourself on the box (`docker compose up -d` on your rootless Docker, or anything native) are reachable from agent containers at `host.jauntbox:<port>`, but only on the TCP ports you list (box-wide in `agent.host_ports`, or for one run with `jauntctl agent --host-port`). Every other host port stays closed to agents, and your laptop keeps reaching the same services over the tailnet as today.

**Architecture:** Plan 03 drops everything an agent bridge sends to the host (`INPUT -i br-+ -j DROP`). This plan adds two iptables chains that `INPUT` jumps to from agent bridges, above that DROP: `JAUNTBOX-HOST` holds one ACCEPT per box-wide port (flushed and refilled by `EnsureNetGuard`, from `box.json`), `JAUNTBOX-RUNS` holds the per-run ACCEPTs, each scoped to that run's bridge, which now gets a fixed name (`br-jb<10 hex>`) at network create. `Start` adds the run's rules after the network exists and before the container starts; `Finish` removes them. Every container gets `--add-host host.jauntbox:host-gateway`. `Start` reads `/proc/net/tcp{,6}` and warns when a listed port has no listener or listens only on loopback. `jauntctl agent list` shows the ports each run may reach. This replaces, by the owner's decision, the spec's supervisor-started per-run accessory services (§6 "Accessory services"): you run the services, jauntbox only opens the ports.

**Tech Stack:** Go (cobra), iptables (user chains in `filter`), Docker CLI (`network create -o com.docker.network.bridge.name`, `--add-host … host-gateway`), `/proc/net/tcp` parsing.

**Spec:** `docs/specs/2026-11-09-jauntbox-single-machine-design.md` (§4 trust zones, §6 agents). Builds on Plan 03 (`internal/agentrun`: `start.go`, `netguard.go`, `docker.go`, `stop.go`, `maintain.go`) and 03a.

## Global Constraints

- Agents reach the host only on the TCP ports listed for them: box-wide `agent.host_ports`, plus that run's `--host-port`s. Everything else from an agent bridge to the host stays dropped by Plan 03's `-i br-+ -j DROP` (and the `docker0` pair, untouched).
- Fail closed: if a rule can't be added, `agent start` refuses and cleans up; `EnsureNetGuard` returns the error. A failure while removing a run's rule never re-opens anything (see Ruling 6).
- Your published ports stay reachable from your laptop over the tailnet; nothing in this plan touches traffic that does not enter through a root-daemon bridge.
- Every Docker and iptables call runs inside root `jauntctl` (03a). Tests never call real Docker, iptables, GitHub, Hetzner or Tailscale: fakes behind interfaces (`Runner`, `Docker`, file fixtures for `/proc/net`).
- Commits: plain conventional messages, no attribution trailers.
- Exact values used across tasks:
  - config key `agent.host_ports` (YAML list of ints, JSON `host_ports`, omitted when empty); each port 1–65535, no duplicates, not 22 (the box's sshd); no other restriction (rootless Docker can only publish ≥ 1024, but a native service can listen lower)
  - per-run flag `--host-port <n>` on `jauntctl agent` and `jauntctl agent start-run`, repeatable (cobra `IntSliceVar`, so `--host-port 5432,6379` also works); same validation, field name `--host-port` in errors
  - validation messages: `<field>: port <n> is out of range (want 1-65535)`, `<field>: port <n> is listed twice`, `<field>: port 22 is the box's SSH port; agents may not reach it`, where `<field>` is `agent.host_ports` or `--host-port`
  - chains (iptables only, table `filter`): `JAUNTBOX-HOST`, `JAUNTBOX-RUNS`
  - `INPUT` order for `br-+` in iptables, top to bottom: `-i br-+ -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT`, `-i br-+ -j JAUNTBOX-HOST`, `-i br-+ -j JAUNTBOX-RUNS`, `-i br-+ -j DROP`. ip6tables keeps Plan 03's pair (ACCEPT, DROP) and gets no chains.
  - box-wide rule (in `JAUNTBOX-HOST`): `-p tcp --dport <n> -j ACCEPT`
  - per-run rule (in `JAUNTBOX-RUNS`): `-i <bridge> -p tcp --dport <n> -j ACCEPT`
  - run bridge name: `br-jb` + the first 10 hex of sha256(run id) (15 characters, the Linux limit; still matched by `br-+`; cannot equal a Docker-generated `br-<12 hex>` name since `j` is not hex)
  - network create argv: `network create --driver bridge -o com.docker.network.bridge.name=<bridge> --label … <name>` (no `--ipv6`, as today)
  - container argv: `--add-host host.jauntbox:host-gateway` right after `--pids-limit 4096`, on every run
  - `run.json`: `host_ports` (sorted union of box-wide and per-run ports at start: what the run may reach), `run_ports` (the per-run ports not already box-wide: the rules this run owns); both omitted when empty
  - `agent list` table gains a last column `PORTS` (comma-joined `host_ports`, `-` when none); `--json` carries `host_ports` from the record (absent when the run may reach no host port)
  - `agent.started` event gains field `host_ports` (comma-joined, empty string when none)
  - listen warnings (to `StartOpts.Progress`, prefixed `warning: `, never fatal):
    - `port <n>: nothing listens on it yet; agents are refused until a service does`
    - `port <n>: listens only on <addrs>, which agents can't reach; publish it on all interfaces (in compose: "<n>:<n>", not "127.0.0.1:<n>:<n>")` — when every listener is loopback (`127.0.0.0/8`, `::1`)
    - `port <n>: listens only on <addrs>; agents connect to host.jauntbox (the Docker bridge gateway), so bind it to all interfaces` — when listeners exist, none is a wildcard (`0.0.0.0`, `::`) and not all are loopback
    - `<addrs>` = the distinct listener addresses, comma-joined, in `/proc` order

## Rulings (decisions the spec leaves to the builder)

1. **Two chains, not rules straight in INPUT.** `JAUNTBOX-HOST` is flushed and refilled on every `EnsureNetGuard` (a port removed from the config disappears without having to list rules, which the error-only `Runner` can't); `JAUNTBOX-RUNS` is never flushed by it (that would cut running runs off) and changes only by exact `-C`/`-A`/`-D`. During the refill new connections to box-wide ports are refused for a moment (fail closed); established ones keep going through the conntrack ACCEPT above. Two starts refilling at once can leave a duplicate ACCEPT until the next refill: harmless, same ports. Cost if wrong: nftables sets later replace two helpers.
2. **Per-run scope = a named bridge.** `NetworkCreate` passes `com.docker.network.bridge.name=br-jb<10 hex>`, derived from the run id, so `Start` and `Finish` know the interface without asking Docker, and the name stays under Plan 03's `br-+` DROP. Cost if wrong (Docker refuses the option or the name): match `-s <network subnet>` from a network inspect instead (agents can't spoof sources: `--cap-drop ALL`).
3. **Match by input interface and port, not by destination address.** A listed port is reachable at any host address (`host.jauntbox`, the run's gateway, the box's tailnet or public IP); an unlisted one at none. Cost if wrong: add `-d <gateway>` to both rule shapes.
4. **IPv4 only.** Agent networks stay without `--ipv6`, `host.jauntbox` is an IPv4 address, and ip6tables keeps dropping everything from bridges to the host. Cost if wrong: a service reachable only over IPv6 is refused (fail closed); mirroring the chains in ip6tables is one loop.
5. **`host.jauntbox` = Docker's `host-gateway`** (the root daemon's `docker0` address, `172.17.0.1` by default), added to every run whether or not ports are listed. Traffic to it from a run's bridge enters INPUT on that bridge, so Ruling 3's rules apply. Cost if wrong (smoke shows it unreachable, e.g. `docker0` disabled): `--add-host host.jauntbox:<run network gateway>` from a network inspect after create.
6. **Run rules live and die with the run.** `Start` adds them after `NetworkCreate` and before `docker run`; a start failure removes them with the other cleanups. `Finish` removes them after the container and network, also when both are already gone. A `-D` error is ignored: the rule names a bridge that no other run can get (the id is unique), so a leftover rule is inert until the next reboot clears INPUT. Cost if wrong: inert rules accumulate on a long-lived box; a periodic sweep in `Tick` removes them.
7. **Box-wide ports are fixed for the box's life** (confirmed by the owner, 2026-11-11). `box.json` is written by cloud-init at `up`, so a config change takes effect on the next box (`jauntbox down`, `jauntbox up`). The boot unit (`jauntctl agent-net`) reads `box.json`; a missing file means no box-wide ports (on first boot the unit may run before cloud-init wrote it; every `agent start` re-applies with the ports from `box.json`). An unreadable or invalid `box.json` still applies the guard with no ports, then returns the error (the unit shows failed). Cost if wrong: the owner wants live changes → a `jauntctl agent-net` re-run after editing `box.json` already works.
8. **Per-run ports already box-wide get no own rule** (`run_ports` excludes them; `host_ports` lists them), so finishing a run can never close a box-wide port. Cost if wrong: none beyond one rule.
9. **The listen check warns, never refuses.** The service may come up after the agent; the warning says why an agent would be refused (loopback bind, nothing listening). An unreadable `/proc/net/tcp` gives no warnings. Cost if wrong: a refusal later is one `if`.
10. **Port 22 is refused, every other port is allowed** (owner's decision, 2026-11-11), box-wide and per run. sshd is the box's front door; an agent has no credentials, but there is no reason to let it knock. A service that really needs 22 can publish on another port. Cost if wrong: one line in `CheckHostPorts`.
11. **`HostRunner` moves to `agentrun`** (from `cmd/jauntbox/agent.go`) so the supervisor's `Maintainer` can remove run rules in `Finish`. Cost if wrong: none.

## Review Focus

1. **A service bound to `127.0.0.1` only** (`ports: ["127.0.0.1:5432:5432"]`, rootlesskit listens on loopback) or **a listed port with nothing listening**: the agent would be refused with no clue. `Start` prints the exact warning naming the bind and the compose fix, and still starts (Task 3 tests `TestListenWarningsLoopbackOnly`, `TestListenWarningsNothingListening`, `TestStartPrintsListenWarnings`).
2. **An unlisted port, on the gateway IP or the box's tailnet IP**, and **another run's per-run port**: the rules match only `--dport <listed>` (no `-d`, so every host address behaves the same) and a per-run rule names only that run's bridge; a second run without `--host-port` gets no rule (Task 2 test `TestRunPortRulesScopedToBridge`; Task 3 test `TestStartTwoRunsPortsSeparate`; live: SMOKE Plan 04 rows 3–4).
3. **Overlapping box-wide and per-run ports** (`host_ports: [5432]`, `--host-port 5432 --host-port 6379`): the run owns only 6379; finishing it leaves 5432 open for the others (Task 3 test `TestStartOverlapOwnsOnlyExtraPorts`; Task 2 test `TestRevokeRunPortsLeavesHostChain`).
4. **Reboot or Docker restart, then re-apply**: after INPUT lost the DROP (or everything), `EnsureNetGuard` restores the exact order (conntrack ACCEPT, the two jumps, DROP), recreates the chains, refills `JAUNTBOX-HOST` with exactly the current ports (a port no longer listed is gone), and is idempotent on a second call (Task 2 tests `TestNetGuardHostPortsOrder`, `TestNetGuardRefillDropsStalePorts`, `TestNetGuardReappliesAfterFlush`, `TestNetGuardIdempotentWithPorts`; `TestAgentNetWithoutBoxInfo`).
5. **`Finish` when the bridge and container are already gone** (Docker restart, reboot, crash): the run's rules are still deleted (by interface name), a `-D` error doesn't fail the finish, the run is recorded finished; a start that fails after the rules were added removes them (Task 3 tests `TestFinishGoneRevokesRunPorts`, `TestFinishIgnoresRevokeErrors`, `TestStartFailureRemovesRunPorts`).

---

### Task 1: `agent.host_ports` in config and box info

**Files:**
- Modify: `internal/config/config.go`, `examples/jauntbox.yaml`
- Test: `internal/config/config_test.go`, `internal/boxinfo/boxinfo_test.go`

**Interfaces:**
- Produces:
  - `config.Agent.HostPorts []int \`yaml:"host_ports" json:"host_ports,omitempty"\`` (default empty). `BoxInfo.Agent` already carries `config.Agent`, and `up` already fills it from the client config (`internal/client/up.go`), so nothing else changes there.
  - `config.CheckHostPorts(field string, ports []int) error` — the messages from Global Constraints; `Validate` calls it with `"agent.host_ports"`.
- `examples/jauntbox.yaml`: under `agent:` add `# host_ports: [5432, 6379]   # TCP ports on the box your agents may reach (host.jauntbox:<port>)`.

- [ ] **Step 1: Failing tests:** `TestAgentHostPortsDefaultEmpty` (`Default().Agent.HostPorts` is empty); `TestAgentHostPortsValidation` (table: `[5432, 6379]` ok; `[1]` ok; `[22]` → `agent.host_ports: port 22 is the box's SSH port; agents may not reach it`; `[0]` and `[65536]` → `agent.host_ports: port … is out of range (want 1-65535)`; `[5432, 5432]` → `agent.host_ports: port 5432 is listed twice`); `TestCheckHostPortsFieldName` (`CheckHostPorts("--host-port", []int{70000})` and `[]int{22}` both name `--host-port`); `TestLoadHostPortsYAML` (a file with `agent: {host_ports: [5432]}` loads); `TestBoxInfoCarriesHostPorts` (write/read round trip keeps `[5432, 6379]`; an empty list leaves no `host_ports` key in the JSON).
- [ ] **Step 2:** `go test ./internal/config ./internal/boxinfo` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): agent.host_ports in config and box info`.

### Task 2: Firewall: host ports for agent bridges

**Files:**
- Modify: `internal/agentrun/netguard.go`, `internal/agentrun/netguard_test.go` (extend `fakeTables` with `-N`, `-F`, `-A`; `-N` of an existing chain and `-F`/`-A`/`-C` of a missing one fail, a jump `-j <chain>` to a missing user chain fails), `cmd/jauntbox/boxforge.go` (`agent-net`), `cmd/jauntbox/agent.go` (drop `hostRunner`, use `agentrun.HostRunner`)
- Create: `internal/agentrun/hostrunner.go`
- Test: `internal/agentrun/netguard_test.go`, `cmd/jauntbox/boxforge_test.go` (or the existing test file covering box commands)

**Interfaces:**
- Consumes: `config.CheckHostPorts` (Task 1), `boxinfo.Read`.
- Produces:
  - `agentrun.HostRunner(ctx context.Context, name string, args ...string) error` — the body of today's `cmd/jauntbox` `hostRunner`, moved.
  - `agentrun.EnsureNetGuard(ctx context.Context, run Runner, hostPorts []int) error` — as today for `DOCKER-USER` and the `docker0` / ip6tables INPUT pairs; for iptables: `-w -N JAUNTBOX-HOST` and `-w -N JAUNTBOX-RUNS` (errors ignored: the chain may exist), then `-w -F JAUNTBOX-HOST` (error returned: the chain neither existed nor could be made), then `-w -A JAUNTBOX-HOST -p tcp --dport <n> -j ACCEPT` per port in ascending order (error returned), then the `br-+` INPUT rules in the Global Constraints order via `ensureOrdered`. Invalid `hostPorts` → the `CheckHostPorts("agent.host_ports", …)` error before any call.
  - `ensureOrdered(ctx context.Context, run Runner, bin, chain string, rules [][]string) error` (unexported) — walks `rules` from last to first; a rule is deleted (if present) and inserted at position 1 when it is missing or when a rule below it was inserted in this pass. Replaces the hand-written ACCEPT/DROP dance; the `docker0` and ip6tables pairs use it too.
  - `agentrun.BridgeName(id string) string` — `br-jb` + first 10 hex of sha256(id).
  - `agentrun.AllowRunPorts(ctx context.Context, run Runner, id string, ports []int) error` — per port: `-w -C JAUNTBOX-RUNS -i <BridgeName(id)> -p tcp --dport <n> -j ACCEPT`, and when that fails `-w -A JAUNTBOX-RUNS …` (error returned, after removing the ones this call added).
  - `agentrun.RevokeRunPorts(ctx context.Context, run Runner, id string, ports []int)` — per port `-w -D JAUNTBOX-RUNS -i <bridge> -p tcp --dport <n> -j ACCEPT`, errors ignored (Ruling 6).
  - `jauntctl agent-net`: reads `boxinfo.Path`; missing → `EnsureNetGuard(ctx, HostRunner, nil)`; unreadable or invalid ports → `EnsureNetGuard(…, nil)` then the read/validation error (Ruling 7); else `EnsureNetGuard(…, info.Agent.HostPorts)`. The path comes from a variable the test can point at a temp file.

- [ ] **Step 1: Failing tests** (fake tables):
  - `TestNetGuardHostPortsOrder`: empty tables, ports `[6379, 5432]` → iptables `INPUT` is exactly `[acceptDocker0, dropDocker0]` and `[acceptBr, "-i br-+ -j JAUNTBOX-HOST", "-i br-+ -j JAUNTBOX-RUNS", dropBr]` in that relative order (docker0 and br-+ blocks may interleave by block); `JAUNTBOX-HOST` = `["-p tcp --dport 5432 -j ACCEPT", "-p tcp --dport 6379 -j ACCEPT"]`; `JAUNTBOX-RUNS` exists and is empty; ip6tables `INPUT` holds only the two pairs and no `JAUNTBOX-*` chain exists in ip6tables.
  - `TestNetGuardUpgradesPlan03Input`: tables pre-filled with Plan 03's INPUT (`acceptBr` above `dropBr`, no chains) → the Global Constraints order, `dropBr` not duplicated.
  - `TestNetGuardRefillDropsStalePorts`: call with `[5432, 6379]`, then with `[5432]` → `JAUNTBOX-HOST` = `[5432 rule]`; a rule pre-placed in `JAUNTBOX-RUNS` survives both calls.
  - `TestNetGuardReappliesAfterFlush`: after a first call, empty `INPUT` (reboot stand-in) but keep chains → the second call restores the full order; then delete only `dropBr` → restored with both jumps and the conntrack ACCEPT above it.
  - `TestNetGuardIdempotentWithPorts`: second call with the same ports → no `-I` and no `-D` on INPUT (refill `-F`/`-A` on `JAUNTBOX-HOST` is expected).
  - `TestNetGuardFailsClosedOnChain`: `-F JAUNTBOX-HOST` fails → error, and no jump was inserted into INPUT; an `-A JAUNTBOX-HOST` failure → error.
  - `TestNetGuardRejectsInvalidPorts`: `[0]` → validation error, fake saw no calls.
  - Review Focus 2: `TestRunPortRulesScopedToBridge`: `AllowRunPorts(id, [5432])` → `JAUNTBOX-RUNS` holds exactly `-i <BridgeName(id)> -p tcp --dport 5432 -j ACCEPT`, no `-d` in any rule of either chain; a second call adds nothing.
  - `TestAllowRunPortsFailsClosed`: second `-A` fails → error, and the first port's rule was removed.
  - Review Focus 3: `TestRevokeRunPortsLeavesHostChain`: box-wide `[5432]`, run `[6379]` → after `RevokeRunPorts`, `JAUNTBOX-RUNS` empty, `JAUNTBOX-HOST` still has 5432; revoking again (rules gone) does not panic or error.
  - `TestBridgeName`: 15 characters, prefix `br-jb`, stable per id, different for two ids, `strings.HasPrefix(name, "br-")`.
  - Review Focus 4: `TestAgentNetWithoutBoxInfo` (missing file → guard applied, no `JAUNTBOX-HOST` rules, no error); `TestAgentNetBadBoxInfo` (invalid JSON → guard applied, error returned); `TestAgentNetUsesBoxPorts`.
  - Update the existing `TestNetGuardInputOrder`, `TestNetGuardIdempotent`, `TestNetGuardFailsClosed`, `TestNetGuardInsertsMissingRules` for the new signature (`nil` ports) and the new jumps.
- [ ] **Step 2:** `go test ./internal/agentrun ./cmd/jauntbox` → FAIL.
- [ ] **Step 3:** Implement; update the two `EnsureNetGuard` callers (`agent-net`, `Starter.Start`: pass `s.HostPorts`, added in Task 3; until then `nil`).
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): open listed host ports to agent bridges, above the drop`.

### Task 3: Per-run ports, `host.jauntbox`, list

**Files:**
- Modify: `internal/agentrun/docker.go`, `internal/agentrun/start.go`, `internal/agentrun/run.go`, `internal/agentrun/stop.go`, `internal/agentrun/maintain.go`, `internal/supervisor/run.go` (wire `Net`), `cmd/jauntbox/agent.go`, the fake `Docker` in `internal/agentrun/*_test.go`
- Create: `internal/agentrun/listen.go`, `internal/agentrun/listen_test.go`
- Test: `internal/agentrun/start_test.go`, `internal/agentrun/stop_test.go`, `internal/agentrun/maintain_test.go`, `internal/agentrun/docker_test.go`, `cmd/jauntbox/agent_test.go`

**Interfaces:**
- Consumes: Task 1 (`CheckHostPorts`, `info.Agent.HostPorts`), Task 2 (`EnsureNetGuard`, `AllowRunPorts`, `RevokeRunPorts`, `BridgeName`, `HostRunner`).
- Produces:
  - `Docker.NetworkCreate(ctx context.Context, name, bridge string, labels map[string]string) error`; the CLI argv from Global Constraints.
  - `ContainerSpec.ExtraHosts []string` (`host:ip` values, each emitted as `--add-host <v>` right after `--pids-limit 4096`); `Start` sets `[]string{"host.jauntbox:host-gateway"}`.
  - `StartOpts.HostPorts []int` (per run); `Starter.HostPorts []int` (box-wide, from `info.Agent.HostPorts` in `agentDeps.starter`); `Starter.ProcNet string` (default `/proc/net`).
  - `Run.HostPorts []int \`json:"host_ports,omitempty"\`` and `Run.RunPorts []int \`json:"run_ports,omitempty"\`` (Global Constraints; both sorted).
  - `agentrun.ListenWarnings(tcp, tcp6 []byte, ports []int) []string` — parses `/proc/net/tcp` and `/proc/net/tcp6` content (state `0A` = LISTEN), returns the warnings from Global Constraints in port order (no `warning: ` prefix; `Start` adds it).
  - `Finish(ctx context.Context, d Docker, p Paths, net Runner, id string, ev events.Sink, now time.Time) error` — as today, plus `RevokeRunPorts(ctx, net, id, r.RunPorts)` after the network removal and before the tmpfs dir removal, whether or not the container existed; a nil `net` skips it.
  - `Maintainer.Net Runner`, passed to `Finish`; `supervisor.New` sets `Net: agentrun.HostRunner`.
  - `jauntctl agent --host-port <n>` (repeatable): validated with `CheckHostPorts("--host-port", …)` before sudo, forwarded to `start-run` as one `--host-port <n>` per port in the given order; `start-run --host-port` validates again and sets `StartOpts.HostPorts`.
  - `jauntctl agent list`: `PORTS` column (Global Constraints); `--json` unchanged apart from the new record fields.
- `Start` order changes: `CheckHostPorts("--host-port", o.HostPorts)` with the other flag checks (before any Docker call) → … → `EnsureNetGuard(ctx, s.Net, s.HostPorts)` → … → `Run.HostPorts` = sorted union, `Run.RunPorts` = sorted per-run ports not in `s.HostPorts` → `NetworkCreate(ctx, name, BridgeName(run.ID), labels)` → `AllowRunPorts(ctx, s.Net, run.ID, run.RunPorts)` (error → `cannot open the host ports for this run, refusing to start: <err>`; cleanup appended: `RevokeRunPorts`) → listen warnings for `run.HostPorts` to `o.Progress` (read `<ProcNet>/tcp` and `tcp6`; a read error skips the check) → container as today with `ExtraHosts` → … → `agent.started` with `host_ports`.

- [ ] **Step 1: Failing tests:**
  - `TestNetworkCreateArgs` (`docker_test.go`): exact argv including `-o com.docker.network.bridge.name=br-jb…` before the labels and no `--ipv6`; `TestContainerSpecAddHost`: `--add-host host.jauntbox:host-gateway` sits right after `--pids-limit 4096`.
  - Update `TestStartHappyPath`: the spec now has the `--add-host`, the network was created with `BridgeName(id)`, no `JAUNTBOX-RUNS` call when no ports are listed, `run.json` has no `host_ports`/`run_ports`, `agent.started` has `host_ports: ""`.
  - `TestStartHostPorts`: box-wide `[5432]`, `--host-port 6379` → recorded runner calls show the `JAUNTBOX-HOST` refill with 5432 and the `-A JAUNTBOX-RUNS -i <bridge> -p tcp --dport 6379 -j ACCEPT` after `NetworkCreate` and before `Run`; `run.json` `host_ports [5432 6379]`, `run_ports [6379]`; event `host_ports: "5432,6379"`.
  - Review Focus 3: `TestStartOverlapOwnsOnlyExtraPorts`: box-wide `[5432]`, `--host-port 5432 --host-port 6379` → `run_ports [6379]`, one `JAUNTBOX-RUNS` rule.
  - Review Focus 2: `TestStartTwoRunsPortsSeparate`: run A with `--host-port 6379`, run B (another checkout) without → B adds no `JAUNTBOX-RUNS` rule, A's rule names A's bridge only.
  - `TestStartInvalidHostPort`: `--host-port 0` → `--host-port: port 0 is out of range (want 1-65535)`, and `--host-port 22` → the SSH-port message; fake Docker saw no calls.
  - `TestStartRunPortsFailureRefuses`: the runner fails `-A JAUNTBOX-RUNS` → error starts with `cannot open the host ports for this run`, network removed, no container started.
  - Review Focus 5: `TestStartFailureRemovesRunPorts`: init fails after the rules were added (container stops before ready) → `-D JAUNTBOX-RUNS` for each run port among the cleanups.
  - Review Focus 1 (`listen_test.go`, fixtures of real `/proc/net/tcp` and `tcp6` lines): `TestListenWarningsLoopbackOnly` (`0100007F:1538` LISTEN → the loopback warning naming `127.0.0.1` and `"5432:5432"`); `TestListenWarningsNothingListening` (no line for the port, or only an ESTABLISHED one → the nothing-listens warning); `TestListenWarningsWildcardOK` (`00000000:1538` or tcp6 `::` → no warning); `TestListenWarningsSpecificAddress` (a tailnet `100.x` address only → the bind-to-all-interfaces warning). `TestStartPrintsListenWarnings`: `ProcNet` points at a temp dir with the loopback fixture → `Progress` has `warning: port 5432: listens only on 127.0.0.1…`, the start succeeds; a missing `ProcNet` dir → no warning, start succeeds.
  - Review Focus 5 (`stop_test.go`): `TestFinishGoneRevokesRunPorts` (no container, no network: `-D JAUNTBOX-RUNS` still issued for each of `run_ports`, run finished, no error); `TestFinishIgnoresRevokeErrors` (runner fails every `-D` → no error, run finished); update `TestFinishRunsFallbackBeforeStop` for the new order (fallback → stop → remove → network rm → revoke).
  - `TestTickGoneContainerFinishesWithoutFallback` (update): the `Maintainer`'s `Net` gets the revoke calls.
  - `cmd/jauntbox/agent_test.go`: `TestAgentForwardsHostPorts` (`--host-port 5432 --host-port 6379` → `start-run` argv carries both, in order); `TestAgentHostPortValidatedBeforeSudo` (`--host-port 70000` → error, sudo never called); update `TestAgentListTable` (header ends in `PORTS`, a run with `host_ports [5432 6379]` shows `5432,6379`, one without shows `-`).
- [ ] **Step 2:** `go test ./internal/agentrun ./internal/supervisor ./cmd/jauntbox` → FAIL.
- [ ] **Step 3:** Implement `listen.go`, the `docker.go` changes, then `start.go`, `stop.go`, `maintain.go`, the supervisor wiring and the commands.
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): per-run host ports, host.jauntbox and ports in agent list`.

### Task 4: Docs and smoke checks

**Files:**
- Modify: `README.md` ("Agents": new subsection "Your services, for agents"; "Root and sudo on the box": the "Two Dockers, on purpose" table and cost line; "Your editor and your services": one line pointing to the new subsection; the config example if it lists `agent:` keys), `site/docs.html` (agents part: new `<h3 id="services">Your services, for agents</h3>` after the `cannot-reach` section; the `docker` section's "Two Dockers" table and cost paragraph; the `config` section if it lists `agent:` keys), `docs/jauntbox/SMOKE.md` (new "Plan 04 checks (your services, for agents)" section after "Plan 03: agent containers", before "What \"good\" looks like")

- [ ] **Step 1: README and site "Your services, for agents"** (same content in both; the site uses the existing markup classes of its neighbouring sections):
  1. Start your services yourself with your rootless Docker: `docker compose up -d` in the project (or any native service). They keep running across agent runs and you manage them; agents can't start, stop or see them.
  2. Publish on all interfaces: `ports: ["5432:5432"]`. A `127.0.0.1:5432:5432` bind listens only on the box's loopback, and agent containers reach the box through their bridge, never through loopback, so they are refused; `jauntctl agent` warns when a listed port listens only on loopback or nothing listens on it. Ports must be 1024 and up (rootless). Your laptop reaches the same port over the tailnet as before.
  3. List the ports: `agent.host_ports: [5432, 6379]` in the config (takes effect on the next box: `jauntbox down`, `jauntbox up`), or for one run: `jauntctl agent --host-port 5432` (repeatable; the port closes when the run ends).
  4. In the app config inside the container use `host.jauntbox`: `postgres://app:app@host.jauntbox:5432/app`, `redis://host.jauntbox:6379`.
  5. What stays closed: every other port on the box, from any host address; IPv6 to the host. Port 22 (sshd) can't be listed at all. `jauntctl agent list` shows each run's ports.
  6. Data (warning kept by the owner's decision): the service's data lives in your Docker volumes, shared by every run that reaches it; an agent can change or drop it. Use a database per project or a throwaway one if that matters.
- [ ] **Step 2: "Two Dockers"**: table row "Ports": agents' column becomes `not published; they reach your services only on the ports you list (host.jauntbox)`; cost line (README and site) becomes: `The cost: agents reach your services only through the host ports you list (see Your services, for agents), not by container name, and they can't run Docker themselves.`
- [ ] **Step 3: SMOKE "Plan 04 checks"**: intro: needs a box made with `agent.host_ports: [5432]`, and in some repo a compose file publishing Postgres on `5432:5432` and a second service on `6379:6379`, started with `docker compose up -d` (rootless). Run from `~/src/<repo>` in a herdr pane unless a row says otherwise. Table `| Check | Result |` with rows:
  1. `sudo iptables -S INPUT` shows, for `br-+`: the ESTABLISHED,RELATED ACCEPT, `-j JAUNTBOX-HOST`, `-j JAUNTBOX-RUNS`, then the DROP, in that order; `sudo iptables -S JAUNTBOX-HOST` holds `--dport 5432`; `sudo ip6tables -S` has no `JAUNTBOX-*` chain
  2. From `jauntctl agent attach <id> --shell`: `getent hosts host.jauntbox` prints an address (record it and `ip route` of the container: is it `docker0`'s `172.17.0.1`?)
  3. `psql postgres://…@host.jauntbox:5432/…` (or `nc -zv host.jauntbox 5432`) connects; `nc -zv -w 5 host.jauntbox 6379` times out (unlisted); `nc -zv -w 5 host.jauntbox 22` and `nc -zv -w 5 <box tailscale IP> 22` time out; `jauntctl agent --host-port 22` is refused with the SSH-port message
  4. `nc -zv -w 5 <box tailscale IP> 5432` connects (rules match the port, not the address: record it)
  5. Per-run: `jauntctl agent --host-port 6379` in a second checkout: 6379 connects from that run; from the first run it still times out; `jauntctl agent list` shows `5432,6379` for the second run and `5432` for the first
  6. `jauntctl agent stop <second run> --now`: `sudo iptables -S JAUNTBOX-RUNS` no longer has the run's `br-jb…` rule; 5432 still works from the first run
  7. Republish Postgres as `127.0.0.1:5432:5432`, then `jauntctl agent` in another checkout: the loopback warning is printed and the agent can't connect; restore `5432:5432`. With the service stopped, the nothing-listens warning is printed
  8. From the laptop: `psql -h jauntbox-<user> -p 5432 …` still connects while agents run
  9. `sudo systemctl restart docker` with a run active: the run reads `gone` / finished in `jauntctl agent list`, its `JAUNTBOX-RUNS` rules are gone, a new `jauntctl agent` reaches 5432; after `sudo reboot`, `sudo iptables -S INPUT` again shows row 1's order before any agent start
- [ ] **Step 4: Commit** `docs(jauntbox): your services, for agents; plan 04 smoke checks`.
