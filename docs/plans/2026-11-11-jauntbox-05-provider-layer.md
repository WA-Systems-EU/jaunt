# jauntbox 05: a provider layer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hetzner stays the only cloud, but everything Hetzner-specific sits behind one named provider, chosen by `provider.name` in the config and recorded in `box.json`, so a second cloud later means adding one package and one self-delete script, without touching `up`, `down`, the supervisor or the config format again. Nothing changes for a Hetzner user: an existing config keeps loading and a running box keeps deleting itself.

**Architecture:** Today the seam exists (`internal/provider.Provider`, one `hetzner` implementation) but leaks: `int64` IDs, `hetzner.New` called directly from `cmd/jauntbox` (`main.go`, `init.go`), a flat `hetzner_token_env` key, a `HetznerToken` field through `client.Deps` and `cloudinit.Params`, the supervisor importing `hetzner` for self-delete, and a safety-net script that curls the Hetzner API. This plan makes IDs strings, adds a provider catalogue (`internal/provider/providers`: one `Kit` per cloud with its constructor, type lister, token prompt and self-delete), moves the token setting into a `provider:` block, records the provider in `box.json`, and splits the safety net into a generic decision script plus a per-provider delete script. Tailscale (`mesh.Mesh`) and GitHub (`forge.Forge`) already sit behind interfaces and are out of scope.

**Tech Stack:** Go, `hcloud-go`, POSIX sh (safety net), cloud-init.

**Spec:** `docs/specs/2026-11-09-jauntbox-single-machine-design.md` (§3 components, §5 lifecycle and self-delete). No spec section asks for multi-cloud; this plan only moves code behind names, so the spec's behaviour is unchanged.

## Global Constraints

- No behaviour change for Hetzner: same API calls, same resources, same names and labels, same events (the `id` field stays the decimal server ID, now carried as a string).
- Old inputs keep working:
  - a config with the flat `hetzner_token_env:` key loads, with a deprecation warning;
  - a `box.json` without `provider` means `hetzner`;
  - a box without `/etc/jauntbox/provider.token` falls back to `/etc/jauntbox/hcloud.token`.
- Tests never call real Hetzner, Tailscale or GitHub APIs (fakes, `httptest`, stubbed `curl` in shell tests).
- Commits: plain conventional messages, no attribution trailers.
- Exact values used across tasks:
  - config block:
    ```yaml
    provider:
      name: hetzner          # default
      token_env: HCLOUD_TOKEN # default
    ```
    Go: `config.Provider{Name string \`yaml:"name" json:"name"\`; TokenEnv string \`yaml:"token_env" json:"token_env"\`}`, field `Config.Provider \`yaml:"provider"\``.
  - legacy key `hetzner_token_env` (still parsed): when set, it fills `provider.token_env`; when both are set to different values → `config: hetzner_token_env and provider.token_env disagree; keep provider.token_env and remove hetzner_token_env`. Warning text: `hetzner_token_env is deprecated; use provider.token_env (same value)`.
  - unknown provider: `unknown provider "<name>" (known: hetzner)`.
  - `box.json` field `provider` (string, always written by `up`).
  - box files: `/etc/jauntbox/provider.token` (0600) replaces `/etc/jauntbox/hcloud.token`; `/opt/jauntbox/selfdelete.sh` (0755) is the provider's delete script, sourced by `/opt/jauntbox/safety-net.sh`.
  - per-provider script source: `internal/cloudinit/units/selfdelete-<name>.sh`, defining `delete_self TOKEN`.

## Rulings

1. **IDs become opaque strings** (`Box.ID`, `Volume.ID`, the `Ensure*` returns, `CreateBoxSpec.VolumeID/SSHKeyID/FirewallID`, `DeleteBox`, `WaitDeleted`, `WaitVolumeDetached`). Hetzner parses with `strconv.ParseInt` and fails with `hetzner: invalid id "<s>"`. Other clouds use strings (AWS `i-…`, GCP names). Cost if wrong: none, strings hold numbers.
2. **One `provider:` block with `name` and `token_env`; `location`, `image`, `profiles` and `volume_size_gb` stay top-level** and are read by the provider (every cloud has a region, an image and machine types). Their defaults stay Hetzner's in `config.Default()`. Cost if wrong: when a second provider arrives, its defaults move into its `Kit` (`config.Default` takes the kit's values); the file format does not change.
3. **The catalogue is an explicit map, not `init()` registration.** `providers.Get` lists exactly what is compiled in, readable in one file. Cost if wrong: none.
4. **No provider question in `jauntbox init` while only one provider exists.** `init` writes `provider: {name: hetzner}` and asks for the token with the kit's prompt. Cost if wrong: one prompt once a second kit exists.
5. **The safety net stays shell.** It exists for when the Go supervisor is broken, so it must not call `jauntctl`. The decision (`decide_safety`) stays generic; the delete moves to a per-provider `delete_self` function rendered by cloud-init. `Render` fails for a provider without a script. Cost if wrong: none.
6. **Self-delete in Go goes through the kit** (`Kit.DeleteSelf(ctx, token)`); the supervisor picks the kit from `box.json`. An unknown provider on the box makes `DeleteSelf` return the unknown-provider error (the safety net still fails too, so the box is kept by neither, which `up` prevents by validating first). Cost if wrong: a box that can't delete itself; same exposure as a revoked token today.
7. **Metadata stays at `169.254.169.254`** in the netguard and metadata guard (the address most clouds share); only the guard unit's description stops naming Hetzner. A provider with another address gets a kit field then. Cost if wrong: one field.
8. **Mesh and forge are untouched.** They already have interfaces and one implementation each; renaming config keys for them is a separate decision.

## Review Focus

1. **An existing config** with `hetzner_token_env: HCLOUD_TOKEN` and no `provider:` block: loads, uses `HCLOUD_TOKEN`, prints the deprecation warning once per command, and `jauntbox up` behaves exactly as before (Task 2 `TestLegacyHetznerTokenEnv`, `TestLegacyAndNewDisagree`; Task 3 `TestLoadDepsLegacyConfig`).
2. **A running box made by the previous version** (no `provider` in `box.json`, token at `hcloud.token`) after a binary re-push: the supervisor still deletes itself through Hetzner (Task 4 `TestDeleteSelfOldBox`).
3. **A non-numeric or empty ID reaching the Hetzner adapter**: a clear error, never a call for server 0 (Task 1 `TestHetznerRejectsBadID`).
4. **A typo in `provider.name`** (`hetzer`): every command that needs the cloud fails before any API call with the known list; `box.json` never carries an unknown name (Task 3 `TestUnknownProvider`).
5. **The safety net after the split**: the token still goes to `curl` on stdin (never in argv), the delete still targets the instance ID from metadata, and the decision logic is unchanged (Task 4 shell tests).

---

### Task 1: Provider-neutral interface (string IDs)

**Files:**
- Modify: `internal/provider/provider.go`, `internal/provider/hetzner/hetzner.go`, `internal/client/up.go`, `internal/client/down.go`, `internal/client/push.go`, `internal/client/status.go`, `internal/client/fakes_test.go`, `internal/client/up_test.go`, `internal/client/down_test.go`
- Test: `internal/provider/hetzner/hetzner_test.go`

**Interfaces:**
- Produces: `provider.Provider` with every ID as `string` (Ruling 1); `EnsureFirewall`'s comment becomes "a firewall with no inbound rules that applies to the user's box (how it attaches is the provider's business; Hetzner uses a label selector)". `Volume.Device` stays provider-filled (already true).
- `hetzner.parseID(s string) (int64, error)` (unexported).

- [ ] **Step 1: Failing tests:** update the existing hetzner tests to string IDs (`"42"`); `TestHetznerRejectsBadID`: `DeleteBox(ctx, "abc")`, `DeleteBox(ctx, "")`, `WaitVolumeDetached(ctx, "-")` → error containing `invalid id`, and the `httptest` server saw no request. Update the client fakes and tests to string IDs; events still carry `id: "42"`.
- [ ] **Step 2:** `go test ./internal/provider/... ./internal/client` → FAIL (compile).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go vet ./... && go test ./...`.
- [ ] **Step 5: Commit** `refactor(jauntbox): provider IDs are opaque strings`.

### Task 2: `provider:` config block

**Files:**
- Modify: `internal/config/config.go`, `examples/jauntbox.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Provider` and `Config.Provider` (Global Constraints), default `{Name: "hetzner", TokenEnv: "HCLOUD_TOKEN"}`; `Config.HetznerTokenEnv` removed from use, kept only as the unexported-in-spirit legacy field `LegacyHetznerTokenEnv string \`yaml:"hetzner_token_env" json:"-"\``; `Config.Warnings []string \`yaml:"-" json:"-"\`` filled by `Load`.
- `Validate`: `provider.name` matches the existing `dnsLabel` regexp (`provider.name must be lowercase letters, digits and dashes`); `provider.token_env` non-empty (`provider.token_env is required`). Whether the name is known is checked by the catalogue (Task 3), so `config` stays a leaf package.
- `examples/jauntbox.yaml`: replace `hetzner_token_env: HCLOUD_TOKEN` with the `provider:` block and a comment: `# the cloud the box runs on; only hetzner exists today`.

- [ ] **Step 1: Failing tests:** `TestProviderDefaults`; `TestLoadProviderBlock` (`provider: {name: hetzner, token_env: MY_TOKEN}` loads); `TestLegacyHetznerTokenEnv` (`hetzner_token_env: OLD` only → `Provider.TokenEnv == "OLD"`, `Warnings` holds the exact deprecation text); `TestLegacyAndNewDisagree` (both set, different → the exact error; both set, same → loads with the warning); `TestProviderNameValidated` (`Hetzner!` → error); `TestUnknownTopLevelKeyStillFails` (a typo'd key is still refused, `KnownFields` unchanged).
- [ ] **Step 2:** `go test ./internal/config` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go test ./...` (callers of `HetznerTokenEnv` move to `Provider.TokenEnv` in Task 3; until then update them in place).
- [ ] **Step 5: Commit** `feat(jauntbox): provider block in the config`.

### Task 3: Provider catalogue on the laptop

**Files:**
- Create: `internal/provider/providers/providers.go`, `internal/provider/providers/providers_test.go`
- Modify: `cmd/jauntbox/main.go` (`loadDeps`), `cmd/jauntbox/init.go`, `internal/client/init.go`, `internal/client/up.go` (`Deps.HetznerToken` → `Deps.ProviderToken`, `box.json` gets `Provider`), `internal/boxinfo/boxinfo.go`, `internal/client/init_test.go`, `cmd/jauntbox/*_test.go`

**Interfaces:**
- Produces:
  ```go
  type Kit struct {
      Name        string // "hetzner"
      Display     string // "Hetzner Cloud"
      TokenPrompt string // the current init prompt text, verbatim
      New         func(token string) provider.Provider
      Types       func(token string) provider.TypeLister
      DeleteSelf  func(ctx context.Context, token string) error // run on the box
  }
  func Get(name string) (Kit, error) // unknown → the Global Constraints error
  func Names() []string              // sorted
  ```
  The `hetzner` kit's `DeleteSelf` is today's supervisor closure body (`hetzner.SelfID` then `DeleteBox`).
- `boxinfo.BoxInfo.Provider string \`json:"provider,omitempty"\``; `func (b BoxInfo) ProviderName() string` returns `"hetzner"` when empty.
- `client.Init(ctx, p, det, kit providers.Kit, meshCheck, path, opts...)`: the `lister` parameter becomes the kit (`kit.Types`), the token prompt and the error label come from it; the written config has the `provider:` block and no `hetzner_token_env`; the env file line uses `cfg.Provider.TokenEnv`.
- `loadDeps`: `providers.Get(cfg.Provider.Name)` before reading any token; `requireEnv(cfg.Provider.TokenEnv, kit.Display+" API token", …)`; prints `cfg.Warnings` to stderr, each prefixed `warning: `.

- [ ] **Step 1: Failing tests:** `TestGetHetzner` (`Get("hetzner")` has all fields non-nil); `TestUnknownProvider` (`Get("hetzer")` → `unknown provider "hetzer" (known: hetzner)`); `TestLoadDepsLegacyConfig` (a legacy config file and `OLD=tok` in env → deps built, stderr has the warning); `TestLoadDepsUnknownProvider` (error before any token is read: the token env var unset still gives the provider error); `TestInitWritesProviderBlock` (the written YAML has `provider:` with `name: hetzner` and no `hetzner_token_env`); `TestUpWritesProviderInBoxInfo`; `TestBoxInfoProviderDefault` (`{}` → `ProviderName() == "hetzner"`).
- [ ] **Step 2:** `go test ./internal/provider/... ./internal/client ./internal/boxinfo ./cmd/jauntbox` → FAIL.
- [ ] **Step 3:** Implement; `cmd/jauntbox` no longer imports `internal/provider/hetzner` (check: `go list -deps ./cmd/jauntbox` still has it only through `providers`).
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): provider catalogue chosen by provider.name`.

### Task 4: The box side: token file, self-delete, safety net

**Files:**
- Modify: `internal/cloudinit/render.go` (`Params.HetznerToken` → `ProviderToken`; write `/etc/jauntbox/provider.token`; add `/opt/jauntbox/selfdelete.sh` from `units/selfdelete-<Box.ProviderName()>.sh`, error `no self-delete script for provider "<name>"` when missing), `internal/cloudinit/units/safety-net.sh`, `internal/cloudinit/units/jauntbox-metadata-guard.service` (Description: `jauntbox: only root may reach the cloud metadata service`), `internal/supervisor/run.go`, `.github/workflows/ci.yml`
- Create: `internal/cloudinit/units/selfdelete-hetzner.sh`, `internal/cloudinit/units/selfdelete-hetzner_test.sh`
- Test: `internal/cloudinit/render_test.go`, `internal/cloudinit/units/safety-net_test.sh`, `internal/supervisor/run_test.go` (or the file holding the supervisor wiring tests)

**Interfaces:**
- `safety-net.sh`: `TOKEN_FILE` = `/etc/jauntbox/provider.token`, falling back to `/etc/jauntbox/hcloud.token` when missing; after `decide_safety` says `DELETE`, sources `${SELFDELETE:-/opt/jauntbox/selfdelete.sh}` and calls `delete_self "$token"`. `decide_safety` unchanged.
- `selfdelete-hetzner.sh`: `delete_self()` = today's two `curl` calls, token through stdin (`-H @-`).
- Supervisor `DeleteSelf`: `providers.Get(info.ProviderName())`, token from `provider.token` (fallback `hcloud.token`), then `kit.DeleteSelf(ctx, token)`; `internal/supervisor` no longer imports `hetzner`. The token path variables are package vars the tests can point at temp files.

- [ ] **Step 1: Failing tests:**
  - `render_test.go`: `TestRenderProviderToken` (the decoded `/etc/jauntbox/provider.token` holds the token; no `hcloud.token` file); `TestRenderSelfDeleteScript` (`/opt/jauntbox/selfdelete.sh` equals `units/selfdelete-hetzner.sh`); `TestRenderUnknownProviderScript` (`Box.Provider = "nope"` → the exact error); `TestRenderOldBoxInfoMeansHetzner` (empty `Provider` → the hetzner script). Update the existing file-list test.
  - `selfdelete-hetzner_test.sh` (`JAUNTBOX_TEST=1`, a stub `curl` on `PATH` that logs argv and stdin): `delete_self tok` → the metadata URL is fetched, the DELETE targets `https://api.hetzner.cloud/v1/servers/<id from stub>`, `tok` appears in the logged stdin and never in any logged argv.
  - `safety-net_test.sh`: existing `decide_safety` cases unchanged; new: with `SELFDELETE` pointing at a stub defining `delete_self`, a DELETE decision calls it with the token from a temp `provider.token`, and from `hcloud.token` when only that exists.
  - Supervisor: `TestDeleteSelfUsesKit` (box info `provider: hetzner`, a token file, a fake kit swapped in through a package var → called with the token); `TestDeleteSelfOldBox` (no `provider`, only the old token file → still called); `TestDeleteSelfUnknownProvider` (→ the unknown-provider error, nothing called).
- [ ] **Step 2:** `go test ./internal/cloudinit ./internal/supervisor`; `sh internal/cloudinit/units/safety-net_test.sh`; `sh internal/cloudinit/units/selfdelete-hetzner_test.sh` → FAIL.
- [ ] **Step 3:** Implement; add the new shell test to `ci.yml` beside `safety-net_test.sh`, and to the shellcheck list.
- [ ] **Step 4:** PASS; `go test ./...`, both shell tests, `shellcheck --severity=warning` on the changed scripts.
- [ ] **Step 5: Commit** `refactor(jauntbox): box self-delete goes through the provider`.

### Task 5: Docs: the provider contract

**Files:**
- Create: `docs/providers.md`
- Modify: `README.md` (configuration: the `provider:` block, the deprecated key), `site/docs.html` (config table: rows `provider.name` default `hetzner` "The cloud the box runs on; only Hetzner exists today." and `provider.token_env` default `HCLOUD_TOKEN`; drop the `hetzner_token_env` row; a one-line note that the old key still loads with a warning), `CONTRIBUTING.md` (one line linking `docs/providers.md`), `docs/jauntbox/SMOKE.md` (one row, below)

- [ ] **Step 1: `docs/providers.md`, "Adding a cloud provider".** What a new provider must supply, one short paragraph each:
  - the `Provider` methods and their promises, copied from the interface comments (Ensure = find by name or create, never modify or delete; `FindBox` errors on more than one match; the firewall has no inbound rules; `CreateBox` gives the box public egress and attaches the volume without automounting it; `Volume.Device` is the stable device path; `WaitVolumeDetached` waits out provider lag and fails fast on `ErrVolumeInUse`);
  - `TypeLister` (types with arch, locations and monthly prices; `init` picks small/big/huge from them);
  - a `Kit` entry in `internal/provider/providers`;
  - `units/selfdelete-<name>.sh` with `delete_self TOKEN` (token on stdin to `curl`, never in argv) and `Kit.DeleteSelf` doing the same in Go;
  - the token: scoped to a dedicated project or account, able to delete only its own servers if the cloud allows it, stored on the box at `/etc/jauntbox/provider.token`;
  - things to check: user-data size (Hetzner caps it at 32 KiB, which is why files are gzipped), the metadata address (the netguard blocks `169.254.169.254`), the image name and its cloud-init support;
  - the fakes and `httptest` rule for tests.
- [ ] **Step 2:** README, site, CONTRIBUTING.
- [ ] **Step 3: SMOKE row** under a new "Plan 05 checks": with the old config (flat `hetzner_token_env`), `jauntbox up` prints the deprecation warning and the box comes up; on the box `cat /etc/jauntbox/box.json` shows `"provider": "hetzner"`, `/etc/jauntbox/provider.token` exists, `/opt/jauntbox/selfdelete.sh` exists; `jauntbox down` deletes the box as before; after moving to the `provider:` block the warning is gone.
- [ ] **Step 4: Commit** `docs(jauntbox): provider block and the contract for adding a cloud`.
