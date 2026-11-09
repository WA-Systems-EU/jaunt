# jauntbox 01 — Walking Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go `jauntbox` binary that brings a right-sized Hetzner box up and down from the laptop, and an on-box supervisor that tears the box down by itself according to the spec's limits (idle, 10 h, 19:00, night session).

**Architecture:** One binary, two roles. Client subcommands (`up`, `down`, `status`, `night`, `resize`, `ssh`, `acl-snippet`) talk to the Hetzner API and Tailscale API and reach the box with `ssh` over Tailscale. Box subcommands (`supervise`, `stop`, `night-set`, `box-status`) run on the box. The supervisor is a one-shot run every 2 min by a systemd timer. Its brain is a pure `lifecycle.Decide` function with table tests, as the shell `decide()` has today. No local state: everything is found by Hetzner labels.

**Tech Stack:** Go 1.27 · `github.com/spf13/cobra` · `gopkg.in/yaml.v3` · `github.com/hetznercloud/hcloud-go/v2/hcloud` · Tailscale REST API v2 through a small `net/http` client (no SDK) · `ssh`/`scp` binaries through `os/exec` · cloud-init.

**Spec:** `docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md`. Plans 02 (secrets + GitHub forge) and 03 (agents + wrap-up) follow this one. Anything they own is out of scope here.

## Starting point

The branch starts from a clean slate (commit `chore: clean slate for jauntbox v1`). The tofu implementation was removed from the branch and lives on `trunk`. Read old files with `git show trunk:<path>`, for example `git show trunk:tofu/modules/jauntbox/cloud-init/user-data.tftpl`. The shared provisioning scripts are already at `provision/`. A local, git-ignored `tofu/` folder with live state may exist on disk: **never touch it**. Implementers must stop and ask if `git status` shows changes they didn't make.

**v1 north star:** going from install to a shell on the first box takes about one coffee, with no file editing. `jauntbox init` (Task 11) is how; every error message names the fix.

## Global Constraints

- Go module path: `github.com/WA-Systems-EU/jaunt`. Binary: `cmd/jauntbox`.
- amd64 only in v1. `Profile.Arch` exists, defaults to `"amd64"`, and any other value fails validation with `arch "<x>" not supported in v1 (amd64 only)`.
- Hetzner only. Uses its **own Hetzner project** (its own token). It never reads or changes tofu-managed resources.
- Labels on every Hetzner resource: `jauntbox=<user>`, plus `role=box|home|fw|key`.
- Names: server and hostname `jauntbox-<user>`; volume `jauntbox-<user>-home`; firewall `jauntbox-<user>`; SSH key `jauntbox-<user>`. Tailscale tag `tag:jauntbox-<user>`.
- Limit defaults (spec §7):
  - `max_uptime: 10h`
  - `evening_cutoff: "19:00"`
  - `idle_day: 2h`
  - `idle_night: 1h`
  - `night_hours: "22:00-07:00"`
  - `human_active_window: 30m`
  - `wrapup_grace: 5m`
- All times are evaluated in the configured **IANA timezone** (`timezone`, required), never the box clock's zone.
- The default branch is never assumed anywhere (not relevant to this plan, but don't hardcode `main` in docs or examples).
- Secrets: the Tailscale auth key in user-data is single-use, ephemeral, preauthorized, and expires in 1 h. It's shredded after join. The only other secret in user-data is the Hetzner token for self-delete, written to `/etc/jauntbox/hcloud.token` mode 0600. **Nothing else** goes into user-data.
- The shared provisioning scripts keep the paths they hardcode: `/etc/jaunt/provision.env`, `/opt/jaunt/dev-setup.sh`, `/opt/jaunt/dev-setup-user.sh`.
- Commits: conventional style (`feat(jauntbox): …`), **no Co-Authored-By trailer**.
- Model use when executing: Sonnet implementers; Opus for reviews and for any task that stalls.

## Review Focus

1. **The binary push never happens** (laptop closed right after `up`): the box must not bill forever. The safety net deletes it once the uptime is over 45 min and the supervisor heartbeat is missing or older than 15 min. Test lives in Task 5.
2. **`up` run twice, or from the laptop and the Lenovo at once:** no second server, no second volume. Test lives in Task 8.
3. **Timezone and daylight saving:** the box clock is UTC; the 19:00 cutoff and night hours follow `Europe/Paris` (or whatever is configured), including across a DST change. Test lives in Task 3.
4. **An abandoned open SSH session** (pts present, no input for 3 h) must not count as human activity. Tests live in Tasks 3 and 9.
5. **Box brought up after the cutoff** (e.g. 20:00) must stop promptly once the human leaves. **A human returning during wrap-up** cancels the wrap-up. Tests live in Task 3.

---

## File Structure

```
go.mod / go.sum
cmd/jauntbox/main.go                 # cobra root; wires client + box subcommands
internal/config/config.go            # Config, Profile, Limits, Load, Validate, defaults
internal/config/duration.go          # Duration type with YAML "2h"/"30m"
internal/events/events.go            # Event, Sink, JSONLFile, Discard
internal/lifecycle/decide.go         # Inputs, Verdict, Decide (pure)
internal/lifecycle/clock.go          # cutoff/night-window helpers (pure)
internal/boxinfo/boxinfo.go          # BoxInfo (/etc/jauntbox/box.json) read/write
internal/cloudinit/render.go         # Render(Params) (string, error)
internal/cloudinit/user-data.yaml.tmpl
internal/cloudinit/units/*.service|*.timer, safety-net.sh, safety-net_test.sh
internal/mesh/mesh.go                # Mesh interface
internal/mesh/tailscale.go           # REST client: CreateAuthKey, DeviceOnline, ACLSnippet
internal/provider/provider.go        # Provider interface + types
internal/provider/hetzner/hetzner.go # hcloud-go implementation + SelfID
internal/remote/ssh.go               # Runner interface + SSH implementation
internal/client/up.go, down.go, status.go, night.go, resize.go, push.go
internal/supervisor/activity.go      # HumanIdle, AgentCounter
internal/supervisor/run.go           # RunOnce
internal/supervisor/state.go         # persisted State, night file, stop request
provision/                           # shared scripts (already here) + embed.go
internal/client/init.go              # jauntbox init wizard
examples/jauntbox.yaml
docs/jauntbox/SMOKE.md
```

---

### Task 1: Module scaffold, config, CI

**Files:**
- Create: `go.mod`, `cmd/jauntbox/main.go`, `internal/config/config.go`, `internal/config/duration.go`, `internal/config/config_test.go`, `examples/jauntbox.yaml`
- Create: `.github/workflows/ci.yml` (one `go` job: `actions/setup-go` with `go-version-file: go.mod`, then `go vet ./... && go test ./...`)
- Delete: `cmd/.gitkeep`, `internal/.gitkeep`

**Interfaces:**
- Produces:
  - `type Duration time.Duration`: YAML-unmarshals from `"2h"`, `"30m"`.
  - `type Profile struct { ServerType string \`yaml:"server_type"\`; Arch string \`yaml:"arch"\` }`
  - `type Limits struct { MaxUptime, IdleDay, IdleNight, HumanActiveWindow, WrapUpGrace Duration; EveningCutoff string \`yaml:"evening_cutoff"\`; NightHours string \`yaml:"night_hours"\` }` (yaml keys snake_case as in Global Constraints)
  - `type Config struct { User, SSHPublicKey, Location, Image, Timezone, DefaultProfile, BoxBinary string; VolumeSizeGB int; Profiles map[string]Profile; Limits Limits; HetznerTokenEnv, TailscaleAPIKeyEnv string }`. Yaml keys: `user, ssh_public_key, location, image, timezone, default_profile, box_binary, volume_size_gb, profiles, limits, hetzner_token_env, tailscale_api_key_env`.
  - `func Default() Config`: location `fsn1`, image `ubuntu-26.04`, volume 100, default_profile `small`, the limits above, token envs `HCLOUD_TOKEN` / `TAILSCALE_API_KEY`, profiles `small: cpx42`, `big: ccx33`, `huge: ccx43`. **These server types are unverified placeholders; Task 10 checks them.**
  - `func Load(path string) (Config, error)`: Default() overlaid with the file, then Validate.
  - `func (c Config) Validate() error`
  - `func (c Config) Loc() (*time.Location, error)`
  - `func DefaultPath() string`: `$XDG_CONFIG_HOME/jauntbox/config.yaml`, falling back to `~/.config/jauntbox/config.yaml`.
  - **Policy seam (spec "Open core"):** `internal/policy/policy.go` with `type Policy struct { Limits config.Limits; Profiles map[string]config.Profile }`, `type Source interface { Load(ctx context.Context) (Policy, error) }`, and `type Local struct { Cfg config.Config }` implementing it from the config file. `client.Up` gets profiles and limits **only** through a `policy.Source`. The limits it returns are frozen into BoxInfo at `up`. Test: `TestLocalSourceReturnsConfigLimits`.

- [ ] **Step 1: Write failing tests** in `config_test.go`:
  - `TestLoadOverlaysDefaults`: a file with only `user`, `ssh_public_key`, `timezone: Europe/Paris` gives `Limits.MaxUptime == 10h`, `EveningCutoff == "19:00"`, `Profiles["small"].ServerType == "cpx42"`, `Profiles["small"].Arch == "amd64"`.
  - `TestValidateRejects`: table with expected error substrings:
    - missing user → `user is required`
    - missing timezone → `timezone is required`
    - `timezone: Mars/Base` → `unknown timezone`
    - `default_profile: xl` (not in profiles) → `default_profile "xl" not in profiles`
    - profile with `arch: arm64` → `arch "arm64" not supported in v1 (amd64 only)`
    - `evening_cutoff: "25:00"` → `invalid evening_cutoff`
    - `night_hours: "22:00"` → `invalid night_hours`
  - `TestUserIsDNSLabel`: `user: "Thomas!"` → `user must be lowercase letters, digits and dashes`.
- [ ] **Step 2:** `go test ./internal/config/` → FAIL (package missing).
- [ ] **Step 3:** Implement. `cmd/jauntbox/main.go` is a cobra root with a `--config` flag (default `config.DefaultPath()`) and no subcommands yet. `examples/jauntbox.yaml` documents every key with the defaults.
- [ ] **Step 4:** `go vet ./... && go test ./...` → PASS. `go build ./cmd/jauntbox` succeeds.
- [ ] **Step 5:** Commit `feat(jauntbox): go module, config loading and validation`.

### Task 2: Event stream

**Files:** Create `internal/events/events.go`, `internal/events/events_test.go`

**Interfaces:**
- Produces:
  - `type Event struct { Time time.Time \`json:"time"\`; Kind string \`json:"kind"\`; Fields map[string]string \`json:"fields,omitempty"\` }`
  - `type Sink interface { Emit(e Event) error }`
  - `func JSONLFile(path string) Sink`: appends one JSON object per line, creates the parent dir 0700 and the file 0600.
  - `var Discard Sink`
  - Kind constants: `BoxUpRequested = "box.up_requested"`, `BoxCreated = "box.created"`, `BoxDownRequested = "box.down_requested"`, `BoxDeleted = "box.deleted"`, `WrapUpStarted = "wrapup.started"`, `WrapUpCancelled = "wrapup.cancelled"`, `NightAuthorised = "night.authorised"`, `SupervisorTick = "supervisor.tick"` (emitted only when the verdict changes).

- [ ] **Step 1: Failing test** `TestJSONLAppends`: emit two events to a temp path. The file has exactly 2 lines; each unmarshals back with Kind and Fields equal; the file mode is 0600.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): JSONL event sink`.

### Task 3: Lifecycle decision (pure)

**Files:** Create `internal/lifecycle/decide.go`, `internal/lifecycle/clock.go`, `internal/lifecycle/decide_test.go`

**Interfaces:**
- Consumes: `config.Limits`
- Produces:
  ```go
  type Action int
  const ( Keep Action = iota; StartWrapUp; Delete )
  type Verdict struct { Action Action; Reason string }
  type Inputs struct {
      Now, UpAt, LastActive time.Time
      HumanIdle      time.Duration // smallest idle across sessions; very large if none
      AgentsRunning  int
      NightUntil     time.Time     // zero = no night session
      StopRequested  bool
      WrapUpStartedAt time.Time    // zero = not wrapping up
      Limits         config.Limits
      Loc            *time.Location
  }
  func Decide(in Inputs) Verdict
  func Deadline(upAt time.Time, l config.Limits, loc *time.Location) (time.Time, string) // earlier of upAt+MaxUptime and EveningCutoff on upAt's local date; reason "max-uptime" | "evening-cutoff"
  func InNightHours(t time.Time, nightHours string, loc *time.Location) bool          // "22:00-07:00" wraps midnight
  ```
  Reasons are exactly: `human-active`, `night`, `agents-done`, `night-deadline`, `max-uptime`, `evening-cutoff`, `idle`, `stop-requested`, `wrapup-in-progress`, `wrapup-grace-elapsed`, `no-agents`, `ok`.

- [ ] **Step 1: Failing table test** `TestDecide`. Use `loc = Europe/Paris` and default limits. Times below are Paris local time unless marked UTC:

| # | Setup | Expect |
|---|---|---|
| 1 | up 08:00, now 12:00, human idle 5m | Keep `human-active` |
| 2 | up 08:00, now 12:00, human idle 3h, agents 1, last active 11:58 | Keep `ok` |
| 3 | up 08:00, now 12:00, human idle 3h, agents 0, last active 09:30 | Delete `idle` (idle 2h30 > 2h, no agents → straight Delete) |
| 4 | up 08:00, now 18:01, human idle 40m, agents 1 | StartWrapUp `max-uptime` (deadline 18:00 < 19:00) |
| 5 | up 10:00, now 19:00, human idle 40m, agents 1 | StartWrapUp `evening-cutoff` |
| 6 | case 5 + WrapUpStartedAt 19:00, now 19:03 | Keep `wrapup-in-progress` |
| 7 | case 5 + WrapUpStartedAt 19:00, now 19:05 | Delete `wrapup-grace-elapsed` |
| 8 | case 6 but agents 0 | Delete `no-agents` |
| 9 | case 6 but human idle 1m | Keep `human-active` (human return cancels wrap-up) |
| 10 | up 10:00, now 23:00, NightUntil next day 07:00, agents 2, human idle 4h | Keep `night` |
| 11 | case 10 with agents 0 | Delete `agents-done` |
| 12 | NightUntil 07:00, now 07:01, agents 1 | StartWrapUp `night-deadline` |
| 13 | up 20:00, now 20:45, human idle 31m, agents 0 | Delete `evening-cutoff` (Review Focus 5) |
| 14 | StopRequested, human idle 1m, agents 1 | StartWrapUp `stop-requested` (explicit down beats human activity) |
| 15 | box clock UTC: up 2026-03-28 07:00 UTC, now 2026-03-29 17:00 UTC (= 19:00 CEST after DST), human idle 1h, agents 1 | StartWrapUp, reason `max-uptime` or `evening-cutoff`, whichever `Deadline` returns. Also assert `Deadline` for up 2026-03-29 08:00 CEST is 18:00 CEST, `max-uptime` (Review Focus 3) |
| 16 | open session idle 3h counted as HumanIdle 3h, agents 0, last active 3h ago, now 14:00 | Delete `idle` (Review Focus 4) |
| 17 | now 23:30, no night session, up 21:00, human idle 61m, agents 0, last active 22:20 | Delete (cutoff already passed → `evening-cutoff`) |

  Also `TestInNightHours`: 23:00 → true, 06:59 → true, 07:00 → false, 12:00 → false.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement `Decide`** in exactly this order (the order is the spec, so it's written out):
  1. `StopRequested` → stop condition `stop-requested`.
  2. Otherwise, if `HumanIdle <= HumanActiveWindow` → `Keep human-active`.
  3. Otherwise, if `NightUntil` is set:
     - before `NightUntil`: agents > 0 → `Keep night`, else stop condition `agents-done`;
     - at or after `NightUntil`: stop condition `night-deadline`.
  4. Otherwise, if `Now >= Deadline(UpAt)` → stop condition with Deadline's reason.
  5. Otherwise, idle window = `IdleNight` if `InNightHours(Now)`, else `IdleDay`. If `Now - LastActive > window` → stop condition `idle`.
  6. Otherwise → `Keep ok`.

  **Stop condition, reason R:**
  - If `WrapUpStartedAt` is zero: agents == 0 → `Delete R`, else `StartWrapUp R`.
  - If wrap-up has started: agents == 0 → `Delete no-agents`; `Now >= WrapUpStartedAt + WrapUpGrace` → `Delete wrapup-grace-elapsed`; otherwise `Keep wrapup-in-progress`.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): pure lifecycle Decide with limit table tests`.

### Task 4: Embed the shared provisioning scripts

**Files:**
- Create: `provision/embed.go` (`package provision`; `//go:embed dev-setup.sh dev-setup-user.sh jaunt-repos.sh`; `var FS embed.FS`)
- Test: `provision/embed_test.go`

- [ ] **Step 1: Failing test** `TestEmbedsScripts`: `FS.ReadFile("dev-setup.sh")` is non-empty and starts with `#!/usr/bin/env bash`. Same for the other two.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `embed.go`.
- [ ] **Step 4:** `go test ./provision/` → PASS.
- [ ] **Step 5:** Commit `feat(provision): embed shared provisioning scripts`.

### Task 5: cloud-init renderer, units, safety net

**Files:**
- Create: `internal/boxinfo/boxinfo.go`, `internal/cloudinit/render.go`, `internal/cloudinit/user-data.yaml.tmpl`, `internal/cloudinit/units/jauntbox-supervise.service`, `internal/cloudinit/units/jauntbox-supervise.timer`, `internal/cloudinit/units/jauntbox-safety.service`, `internal/cloudinit/units/jauntbox-safety.timer`, `internal/cloudinit/units/safety-net.sh`, `internal/cloudinit/units/safety-net_test.sh`, `internal/cloudinit/render_test.go`
- Modify: `.github/workflows/ci.yml` (run `sh internal/cloudinit/units/safety-net_test.sh`)

**Interfaces:**
- Consumes: `provision.FS`, `config.Limits`
- Produces:
  - `type BoxInfo struct { User, Hostname, Profile, Timezone string; UpAt time.Time; Limits config.Limits }` with `func Read(path string) (BoxInfo, error)` and `func Write(path string, b BoxInfo) error`. Path constant `boxinfo.Path = "/etc/jauntbox/box.json"`.
  - `type Params struct { Box boxinfo.BoxInfo; TailscaleAuthKey, HetznerToken, VolumeDevice, Tag string }`
  - `func Render(p Params) (string, error)`

Template content: port `git show trunk:tofu/modules/jauntbox/cloud-init/user-data.tftpl` with these changes.
- **Keep:** the volume wait/mount at `/home`, the tailscaled `--state` drop-in, `tailscale up --auth-key="$(cat …)" --hostname=<Hostname> --advertise-tags=<Tag> --ssh && shred -u` key file, the provision scripts at `/opt/jaunt/*` with `/etc/jaunt/provision.env` (`JAUNT_USER`, `JAUNT_UID=2000`, `JAUNT_INSTALL_TOOLCHAIN=true`, `JAUNT_DOCKER_DATA_ROOT=/home/.docker`), and running `/opt/jaunt/dev-setup.sh`.
- **Drop:** the idle-watch files and timer, and `setup.sh`.
- **Add:**
  - `/etc/jauntbox/box.json` (BoxInfo JSON, 0644);
  - `/etc/jauntbox/hcloud.token` (0600);
  - `/etc/jauntbox/ts.key` (0600);
  - the four units, which are **written but not enabled**, except the safety timer, which **is enabled in runcmd**;
  - `/opt/jauntbox/safety-net.sh` (0755).

Units:
- `jauntbox-supervise.timer`: `OnBootSec=3min`, `OnUnitActiveSec=2min`.
- The service runs `/usr/local/bin/jauntbox supervise`.
- `jauntbox-safety.timer`: `OnBootSec=10min`, `OnUnitActiveSec=10min`.

`safety-net.sh` has a pure `decide_safety UPTIME_SECS HEARTBEAT_AGE_SECS` (heartbeat age is `-1` when the file is missing). It prints `DELETE` when uptime > 2700 and (age == -1 or age > 900); otherwise `KEEP`. Its `main` reads `/proc/uptime` and the mtime of `/var/lib/jauntbox/heartbeat`. On `DELETE` it reads `/etc/jauntbox/hcloud.token` and the instance id from `http://169.254.169.254/hetzner/v1/metadata/instance-id`, then `curl -X DELETE`s itself. Same pattern as `git show trunk:tofu/hetzner/self-delete.sh.tftpl`, and skipped when `JAUNTBOX_TEST=1`.

- [ ] **Step 1: Failing tests.**
  - `safety-net_test.sh`, cases `(uptime, age) → verdict`: `(600,-1)→KEEP`, `(2701,-1)→DELETE`, `(2701,100)→KEEP`, `(2701,901)→DELETE`, `(99999,30)→KEEP`.
  - `render_test.go`:
    - `TestRenderIsCloudConfig`: output starts with `#cloud-config\n` and parses as YAML with `write_files` and `runcmd`.
    - `TestRenderSecretsOnlyInFiles`: the auth key `tskey-auth-TEST` and token `hc-TEST` appear only inside `write_files` contents, never in any `runcmd` entry.
    - `TestRenderBoxInfoRoundTrip`: the box.json content decodes to the input BoxInfo.
    - `TestRenderUsesTagAndHostname`: runcmd contains `--hostname=jauntbox-alice` and `--advertise-tags=tag:jauntbox-alice`.
    - `TestRenderEnablesOnlySafetyTimer`: runcmd contains `systemctl enable --now jauntbox-safety.timer` and does **not** enable `jauntbox-supervise.timer`.
- [ ] **Step 2:** Run both → FAIL.
- [ ] **Step 3:** Implement with `text/template` and `//go:embed`. Base64-encode file contents (`encoding: b64`), as the trunk tofu template does.
- [ ] **Step 4:** `go test ./internal/cloudinit/ ./internal/boxinfo/ && sh internal/cloudinit/units/safety-net_test.sh` → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): cloud-init renderer with heartbeat safety net`.

### Task 6: Mesh adapter: Tailscale REST client

**Files:** Create `internal/mesh/mesh.go`, `internal/mesh/tailscale.go`, `internal/mesh/tailscale_test.go`

**Interfaces:**
- Produces:
  ```go
  type Mesh interface {
      CreateAuthKey(ctx context.Context, tags []string, expiry time.Duration) (string, error)
      DeviceOnline(ctx context.Context, hostname string) (bool, error)
  }
  func NewTailscale(apiKey, baseURL string, hc *http.Client) *Tailscale // baseURL default "https://api.tailscale.com"
  func ACLSnippet(user string) string // JSON fragment: tagOwners entry for tag:jauntbox-<user>, an ssh rule letting the owner reach it, and a comment stating no grant from this tag to anything
  ```
- `CreateAuthKey` does `POST /api/v2/tailnet/-/keys` (Bearer auth) with `capabilities.devices.create = {reusable:false, ephemeral:true, preauthorized:true, tags}` and `expirySeconds`, and returns `key`.
- `DeviceOnline` does `GET /api/v2/tailnet/-/devices`. It matches a device whose `hostname` equals the argument, or whose `name` starts with `hostname + "."`, and returns `connectedToControl` (verify that field name against the current API docs; the fallback is `lastSeen` within 2 min).

- [ ] **Step 1: Failing tests** against `httptest.Server`:
  - `TestCreateAuthKeySendsEphemeralSingleUse`: asserts method, path, auth header, and the JSON body flags. Returns `{"key":"tskey-auth-x"}` → result `tskey-auth-x`.
  - `TestCreateAuthKeyErrorIncludesStatus`: a 403 → the error contains `403`.
  - `TestDeviceOnline`: two devices, one matching and online → true; no match → false, nil error.
  - `TestACLSnippetMentionsTag`: contains `tag:jauntbox-alice`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): tailscale mesh adapter (auth keys, device presence)`.

### Task 7: Provider adapter: Hetzner

**Files:** Create `internal/provider/provider.go`, `internal/provider/hetzner/hetzner.go`, `internal/provider/hetzner/hetzner_test.go`

**Interfaces:**
- Produces:
  ```go
  type Box struct { ID int64; Name, Status, ServerType string; Created time.Time }
  type Volume struct { ID int64; Name, Device string } // Device: /dev/disk/by-id/scsi-0HC_Volume_<ID>
  type CreateBoxSpec struct {
      Name, ServerType, Image, Location, UserData string
      Labels map[string]string
      VolumeID, SSHKeyID, FirewallID int64
  }
  type Provider interface {
      EnsureSSHKey(ctx context.Context, name, publicKey string, labels map[string]string) (int64, error)
      EnsureFirewall(ctx context.Context, name string, labels map[string]string) (int64, error) // inbound: none; applied via label selector jauntbox=<user>,role=box
      EnsureVolume(ctx context.Context, name string, sizeGB int, location string, labels map[string]string) (Volume, error)
      FindBox(ctx context.Context, labels map[string]string) (*Box, error) // nil, nil when none; error if >1
      CreateBox(ctx context.Context, spec CreateBoxSpec) (Box, error)     // public IPv4+IPv6 on (egress); volume attached, automount false
      DeleteBox(ctx context.Context, id int64) error
      WaitDeleted(ctx context.Context, id int64) error
  }
  func New(token string, opts ...hcloud.ClientOption) *Hetzner // tests pass hcloud.WithEndpoint(srv.URL)
  func SelfID(ctx context.Context, metadataURL string) (int64, error) // default http://169.254.169.254/hetzner/v1/metadata/instance-id
  ```
  "Ensure" means find by name; create if absent; never modify or delete an existing resource.

- [ ] **Step 1: Failing tests** with an `httptest` fake of the Hetzner API that records requests:
  - `TestEnsureVolumeIsIdempotent`: the first call (list returns empty) POSTs `/volumes` with the name, size, location, labels and `format: ext4`. The second call (list returns it) makes no POST.
  - `TestFindBoxBySelector`: the request has `label_selector=jauntbox%3Dalice%2Crole%3Dbox`. Two results → error containing `more than one`.
  - `TestCreateBoxBody`: the POST `/servers` body has the name, server_type, image, location, user_data, labels, `volumes:[id]`, `automount:false`, `firewalls:[{firewall:id}]`, and `ssh_keys:[id]`.
  - `TestEnsureFirewallDeniesInbound`: the created firewall has no `in` rules, and `apply_to` has the label selector.
  - `TestSelfID`: parses `"123456"`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement with `hcloud-go/v2`.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): hetzner provider adapter`.

### Task 8: Client: `up`, binary push, `acl-snippet`

**Files:** Create `internal/remote/ssh.go`, `internal/client/up.go`, `internal/client/push.go`, `internal/client/up_test.go`, `internal/client/fakes_test.go`. Modify `cmd/jauntbox/main.go`.

**Interfaces:**
- Consumes: `config.Config`, `provider.Provider`, `mesh.Mesh`, `cloudinit.Render`, `events.Sink`
- Produces:
  ```go
  type Runner interface {
      Run(ctx context.Context, host string, args ...string) ([]byte, error) // ssh <user>@<host> -- args
      Copy(ctx context.Context, host, localPath, remotePath string) error   // scp
  }
  func NewSSH(user string) Runner
  type Deps struct { Cfg config.Config; Policy policy.Source; Prov provider.Provider; Mesh mesh.Mesh; Remote remote.Runner; Events events.Sink; Now func() time.Time; Spawn func(args ...string) error }
  type UpResult struct { Box provider.Box; Created bool }
  func Up(ctx context.Context, d Deps, profile string) (UpResult, error)
  func PushBinary(ctx context.Context, d Deps, wait time.Duration) error
  func BoxBinaryPath(cfg config.Config) (string, error) // os.Executable() when linux/amd64, else cfg.BoxBinary (required, else error "box_binary must be set on non-linux/amd64 clients")
  ```
- `Up` steps:
  1. Validate the profile exists.
  2. EnsureSSHKey, EnsureFirewall, EnsureVolume.
  3. FindBox. **If a box exists, return `Created:false`** with no other side effects.
  4. Otherwise: CreateAuthKey(`[tag:jauntbox-<user>]`, 1h) → Render → CreateBox → emit `BoxCreated` → `Spawn("internal-push")` (detached).
- `PushBinary`:
  1. Poll `DeviceOnline` every 10 s, up to `wait` (default 15 min).
  2. Then `Copy(binary → /tmp/jauntbox)`.
  3. Then `Run("sudo install -m 0755 /tmp/jauntbox /usr/local/bin/jauntbox && sudo systemctl enable --now jauntbox-supervise.timer")`.
- CLI:
  - `jauntbox up [profile] [--wait]`: `--wait` runs PushBinary in the foreground instead of Spawn.
  - Hidden `jauntbox internal-push`: the detached child. Uses `Setsid`; logs to `$XDG_STATE_HOME/jauntbox/push.log`.
  - `jauntbox acl-snippet`.
  - Tokens come from the env vars named in config, with a clear error naming the variable when one is missing.

- [ ] **Step 1: Failing tests** with in-memory fakes of Provider, Mesh and Runner:
  - `TestUpCreatesOnce`: the first `Up` creates the box, calls Spawn once, and emits `box.created`. The second `Up` (fake now has a box) returns `Created:false`, makes no CreateAuthKey, CreateBox or Spawn call, and EnsureVolume returns the same ID (Review Focus 2).
  - `TestUpUnknownProfile`: `xl` → error `unknown profile "xl"`, and no provider calls.
  - `TestUpUserDataCarriesBoxInfo`: decoding the user-data rendered by the fake gives BoxInfo with `Profile:"big"`, `UpAt == Now()`, and `Timezone` from config.
  - `TestPushBinaryWaitsForMesh`: DeviceOnline returns false twice then true. Copy and Run are called once, in order, with the exact command above (use a 1 ms poll interval via an unexported variable).
  - `TestPushBinaryTimesOut`: never online → error containing `did not join the tailnet`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./internal/... && go build ./cmd/jauntbox` → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): up with idempotent provisioning and detached binary push`.

### Task 9: Box side: supervisor and box commands

**Files:** Create `internal/supervisor/activity.go`, `internal/supervisor/state.go`, `internal/supervisor/run.go`, `internal/supervisor/*_test.go`. Modify `cmd/jauntbox/main.go`.

**Interfaces:**
- Consumes: `lifecycle.Decide`, `boxinfo.Read`, `events.Sink`, `provider.Provider.DeleteBox`, `hetzner.SelfID`
- Produces:
  ```go
  func HumanIdle(ptsDir string, now time.Time) time.Duration // min(now - atime) over numeric entries in ptsDir; 1<<62 if none
  type AgentCounter interface { Count(ctx context.Context) (int, error) }
  type DockerAgents struct{} // `docker ps -q --filter label=jauntbox.agent=1`, counts non-empty lines; plan 03 starts such containers
  type State struct { LastActive, WrapUpStartedAt time.Time; WrapUpReason, LastVerdict string }
  type Paths struct { State, Night, StopRequest, Heartbeat, WrapUpFlag, Events string } // defaults: /var/lib/jauntbox/{state.json,night.json,stop-requested,heartbeat}, /run/jauntbox/wrapup, /home/.jauntbox/events.jsonl
  type Supervisor struct { Info boxinfo.BoxInfo; Paths Paths; PtsDir string; Agents AgentCounter; Events events.Sink; Now func() time.Time; DeleteSelf func(ctx context.Context) error; Sync func() }
  func (s *Supervisor) RunOnce(ctx context.Context) (lifecycle.Verdict, error)
  ```
- `RunOnce`:
  1. Touch the heartbeat.
  2. Load the state (missing → LastActive = Info.UpAt).
  3. Gather inputs. LastActive = Now if human idle ≤ window or agents > 0.
  4. Decide.
  5. On `StartWrapUp`: set WrapUpStartedAt and reason, create the `WrapUpFlag` file (plan 03's hook reads it), emit `wrapup.started`.
  6. On `Keep human-active` while WrapUpStartedAt is set: clear it, remove the flag, emit `wrapup.cancelled`.
  7. On `Delete`: emit `box.deleted` with the reason, call `Sync()`, then `DeleteSelf`.
  8. Save the state. Emit `supervisor.tick` only when the reason changed.
- CLI (box side, needs root):
  - `jauntbox supervise`
  - `jauntbox stop`: writes StopRequest, then RunOnce.
  - `jauntbox night-set --until <RFC3339>`: writes `{"until":…}`, emits `night.authorised`.
  - `jauntbox box-status --json`: BoxInfo, state, verdict preview with no side effects, and `deadline` from `lifecycle.Deadline`.

- [ ] **Step 1: Failing tests:**
  - `TestHumanIdleUsesAtime`: temp dir with files `0`, `1` and `ptmx`. Set atimes with `os.Chtimes` to now-3h and now-10m → 10m. `ptmx` is ignored. Empty dir → `1<<62` (Review Focus 4).
  - `TestRunOnceWrapUpThenDelete`: fake clock and agents=1, up 10:00, now 19:00, human idle 1h. Run 1 → StartWrapUp, the flag file exists. Clock +5m → Delete, and DeleteSelf and Sync are each called once.
  - `TestRunOnceHumanCancelsWrapUp`: after a StartWrapUp, a pts atime 1 min old → Keep; the flag is removed; `wrapup.cancelled` emitted.
  - `TestRunOnceStopRequest`: StopRequest file present, agents 0 → Delete `stop-requested`.
  - `TestRunOnceHeartbeat`: the heartbeat mtime is Now after every run.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement. Production wiring:
  - `DeleteSelf` = read `/etc/jauntbox/hcloud.token` → `hetzner.New(token).DeleteBox(ctx, SelfID)`;
  - `Sync` = `unix.Sync`;
  - `PtsDir` = `/dev/pts`.
- [ ] **Step 4:** `go test ./internal/supervisor/` → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): on-box supervisor with wrap-up flag, night sessions and heartbeat`.

### Task 10: Client `down`, `status`, `night`, `resize`, `ssh`; docs; live smoke test

**Files:** Create `internal/client/down.go`, `status.go`, `night.go`, `resize.go`, matching tests, `docs/jauntbox/SMOKE.md`. Modify `README.md` (replace the "Status" section's "nothing here runs yet" with a real quick start), `examples/jauntbox.yaml`, `cmd/jauntbox/main.go`.

**Interfaces:**
- Consumes: `client.Deps`, `remote.Runner`, `provider.Provider`
- Produces:
  - `func Down(ctx context.Context, d Deps, force bool) error`:
    - no box → nil with message `no box running`;
    - not forced → `Run("sudo jauntbox stop")`, then `WaitDeleted` (up to `WrapUpGrace + 3m`);
    - if the SSH fails, or `force` → `DeleteBox` directly;
    - emits `box.down_requested`.
  - `func Status(ctx context.Context, d Deps) (StatusReport, error)`: provider box (or none), mesh online, and the `box-status --json` output when reachable.
  - `func Night(ctx context.Context, d Deps, until string) error`: parses `HH:MM` in the config timezone as the next occurrence after Now, then `Run("sudo jauntbox night-set --until <RFC3339>")`.
  - `func Resize(ctx context.Context, d Deps, profile string) error` = `Down(false)` → `WaitDeleted` → `Up(profile)`.
  - `jauntbox ssh` execs `ssh <user>@jauntbox-<user>`.

- [ ] **Step 1: Failing tests** with fakes:
  - `TestDownGraceful`: Runner gets `sudo jauntbox stop`; no direct DeleteBox.
  - `TestDownFallsBackToForce`: Runner errors → DeleteBox is called.
  - `TestDownNoBox`.
  - `TestNightParsesNextOccurrence`: now 22:30 Paris, `07:00` → tomorrow 07:00+01:00 (or +02:00 in summer); now 06:00, `07:00` → today.
  - `TestResizeOrder`: calls happen in the order stop → WaitDeleted → CreateBox with the new server type.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement and wire the CLI.
- [ ] **Step 4:** `go vet ./... && go test ./...` → PASS.
- [ ] **Step 5:** Write `docs/jauntbox/SMOKE.md`, a manual live procedure in the style of `git show trunk:tofu/SMOKE.md`:
  1. Make a dedicated Hetzner project and token. Run `hcloud server-type list` and **replace the placeholder profile types** in `config.Default()` and `examples/jauntbox.yaml` with real x86 types for ~16/32/64 GB in `fsn1`. Commit that change.
  2. Paste the `acl-snippet` output into the tailnet policy.
  3. `jauntbox up small --wait` → `ssh` works; `systemctl list-timers` shows `jauntbox-supervise` and `jauntbox-safety`.
  4. `jauntbox status` shows the deadline.
  5. On the box, set `max_uptime: 15m` by editing `/etc/jauntbox/box.json`, disconnect, and wait → the box deletes itself and the volume remains.
  6. `jauntbox up` again → same hostname, `/home` content intact.
  7. `jauntbox night --until <now+10m>`, then a fake agent `docker run -d --label jauntbox.agent=1 alpine sleep 3600` → the box survives past the cutoff and deletes itself at the deadline.
  8. `jauntbox down` → gone within the grace period.
  9. Safety net: `up` without `--wait`, kill the push helper → the box deletes itself ~45–55 min later.
- [ ] **Step 6:** Commit `feat(jauntbox): down/status/night/resize + smoke procedure`.
- [ ] **Step 7:** Thomas runs SMOKE.md on a real box. Fix anything found (each fix as its own commit, with its test) before plan 02 starts.

### Task 11: `jauntbox init`, the one-coffee setup

**Files:** Create `internal/client/init.go`, `internal/client/init_test.go`. Modify `cmd/jauntbox/main.go`, `README.md`, `docs/jauntbox/SMOKE.md` (step 1 becomes `jauntbox init`).

**Interfaces:**
- Consumes: `config.Config`, `config.Default`, `mesh.ACLSnippet`
- Produces:
  ```go
  type Prompter interface { Ask(question, def string) (string, error); Secret(question string) (string, error); Confirm(question string, def bool) (bool, error) }
  type Detected struct { User, SSHPublicKey, Timezone string }
  func Detect(home string, env func(string) string, localZone func() string) Detected // user from $USER lower-cased and DNS-cleaned; first of ~/.ssh/id_ed25519.pub, id_ecdsa.pub, id_rsa.pub; timezone from localZone (reads /etc/localtime link or TZ)
  type ServerType struct { Name string; Cores int; MemoryGB float64; Arch string; Locations []string; Deprecated bool }
  type TypeLister interface { ServerTypes(ctx context.Context) ([]ServerType, error) } // Hetzner adapter implements it (add to provider/hetzner)
  func PickProfiles(types []ServerType, location string) (map[string]config.Profile, error) // small/big/huge = cheapest non-deprecated x86 type in location with MemoryGB >= 16 / 32 / 64
  func Init(ctx context.Context, p Prompter, det Detected, lister func(token string) TypeLister, meshCheck func(apiKey string) error, path string) (config.Config, error)
  ```
- `Init` flow:
  1. Show the detected user, SSH key and timezone, each confirmable or editable.
  2. Ask for the Hetzner token; validate it by listing server types. Ask for the Tailscale API key; validate it with one devices call. Each secret is read with `Secret` (no echo).
  3. Pick profiles automatically.
  4. Write the config with mode 0600. Tokens are **not** written: print the two `export` lines to add to the shell profile, or offer `~/.config/jauntbox/env` with mode 0600.
  5. Print the ACL snippet and the next command, `jauntbox up small`.
  6. If the config already exists, ask before overwriting.

- [ ] **Step 1: Failing tests:**
  - `TestPickProfiles`: from a fixture list (cx22 4 GB, cpx42 16 GB, ccx33 32 GB, ccx43 64 GB, a cheaper but deprecated 32 GB type, an arm64 cax41 32 GB) → small=cpx42, big=ccx33, huge=ccx43; arm and deprecated types are never picked. A missing size → error `no amd64 server type with >= 64 GB in fsn1`.
  - `TestDetect`: a fake home with `id_ed25519.pub`, `USER=Thomas.S`, zone `Europe/Paris` → `thomas-s`, key contents, `Europe/Paris`.
  - `TestInitWritesConfigWithoutSecrets`: a scripted Prompter accepting the defaults → the written file loads with `config.Load`, has mode 0600, and contains neither token string.
  - `TestInitRejectsBadToken`: the lister errors → re-prompts once, then fails with `Hetzner token rejected: <reason>`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement. The terminal Prompter uses `golang.org/x/term` for secrets.
- [ ] **Step 4:** `go test ./...` → PASS.
- [ ] **Step 5:** Commit `feat(jauntbox): init wizard — detect, validate tokens, auto-pick profiles`.
- [ ] **Step 6:** Thomas, on a clean user account: time `jauntbox init` → `jauntbox up small --wait` → `jauntbox ssh`. Record the time in SMOKE.md. Target: one coffee, about 15 min including box boot.
