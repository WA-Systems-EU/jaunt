# jauntbox 06: terminal UI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `jauntbox up` (and bare `jauntbox`, `down`, `resize`) show a live step list and stay in the foreground until done; `status` prints a styled summary; `init` becomes an interactive form; everything falls back to plain lines when the output is not a terminal.

**Architecture:** A new `internal/ui` package defines a small step-reporting interface and three implementations: a plain line writer, a live (animated, in-place) writer, and a recorder for tests. `internal/client` reports its steps through `Deps.Steps` instead of printing; `cmd/jauntbox` picks the renderer (live or plain) and prints the header and end lines. `init` gets a second front-end built on Charm `huh`, sharing one `ApplyInit` with today's line-by-line wizard, which stays as the plain fallback.

**Tech Stack:** Go, cobra, `golang.org/x/term` (already a dependency), `github.com/charmbracelet/lipgloss` (styling), `github.com/charmbracelet/huh` (init form). Versions: whatever `go get` resolves as the latest tagged release of each, recorded in go.mod.

**Spec:** `docs/specs/2026-11-11-jauntbox-terminal-ui-design.md` (approved 2026-11-11, including its "Calls made after the builder check").

## Global Constraints

- Scope: `up`, bare `jauntbox` (the steps before it attaches), `down`, `resize`, `status`, `init`. Leave the output of `setup github`, `unlock`, `secrets`, `night`, `ssh`, `acl-snippet` and every `jauntctl` command unchanged.
- No change to what commands do, their order of operations or exit codes, except: `up` runs in the foreground by default (Task 4) and `resize` waits for the new box (Task 3).
- Step symbols and colours (live mode): pending `·` dim; running braille spinner `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` in yellow `#f3d36b`; done `✓` teal `#4fc2b9`; warning `!` yellow; skipped `–` dim; failed `✕` red `#ff6a5c`; secondary text dim `#6f8196`; end marker `●` teal. Plain mode uses the same symbols with no colour or animation.
- Duration format `ui.FormatDuration`: under 10 s → one decimal (`0.2s`, `8.1s`); under 60 s → whole seconds (`41s`); otherwise `1m12s`, `10m0s` (minutes and whole seconds).
- Plain mode when any of: the stream is not a terminal (`term.IsTerminal`), `NO_COLOR` is set (any value), `TERM=dumb`, or the global `--plain` flag.
- Live mode never uses the alternate screen: finished lines stay in the scrollback.
- Tests never call real Hetzner, Tailscale or GitHub; no test needs a real terminal (live renderer tests write to a buffer).
- Commits: plain conventional messages, no attribution trailers.

## Rulings (decisions the spec leaves open or the code forces)

1. **One step stream on stderr.** Steps, header and end lines go to stderr (stdout stays clean for anything a script parses, e.g. `status --json`). Mode detection looks at stderr. Cost if wrong: one writer to swap.
2. **Hand-rolled live renderer, not Bubble Tea, for the step list.** A ticker (100 ms) redraws only the unfinished block in place (`\x1b[<n>A\x1b[J`); finished lines above it are never touched. Simpler to test and no alternate screen. Bubble Tea is used only through `huh` for `init`. Cost if wrong: swap the renderer behind the same interface.
3. **"Joined the tailnet" shows the box name only.** `mesh.Mesh` has no IP lookup; adding one is out of scope. Cost if wrong: one Mesh method later.
4. **Prices in the init form are monthly** (`€12.49/mo`), from `provider.ServerType.PriceMonthly`, which is what the API wrapper already returns. The spec says hourly; monthly is what we have and what Hetzner bills as the cap. Cost if wrong: divide by 730.
5. **`up --detach`** restores today's return-at-once behaviour (background push). `--wait` stays accepted and does nothing (help text: "default; kept for scripts").
6. **Timezone list** = the zone names found under the system zoneinfo dir (`/usr/share/zoneinfo`, else `/var/db/timezone/zoneinfo`, else `$ZONEINFO`), filtered with `time.LoadLocation`; when none is found the field is a free text input validated the same way as today.
7. **Existing home volume for the location default**: a new optional provider interface `provider.VolumeFinder` (Hetzner implements it); `init` uses it when the token is valid. Cost if wrong: one call removed.

## Review Focus

1. **Output not a terminal** (`jauntbox up | tee`, CI): no ANSI escape bytes, one line per finished step, nothing lost (Task 1 `TestPlainNoEscapes`, Task 4 `TestUpPlainWhenNotTTY`).
2. **Ctrl-C after the server exists**: exactly one background push is spawned, after the foreground push has returned, and the message names `push.log`; Ctrl-C before the server exists spawns nothing (Task 4 `TestUpInterruptAfterCreateHandsOff`, `TestUpInterruptBeforeCreateNoSpawn`).
3. **A failure in the middle** (provisioning times out): that step shows `✕` with the error, later steps stay pending, the command exits non-zero exactly as today, and the live block is finalised (no spinner left running, cursor back below the block) (Task 2 `TestLiveFailFinalises`, Task 3 `TestPushBinaryTimeoutFailsProvisioning`).
4. **Unlock problems never turn into a failure**: no secrets → skipped, unlock error → warning, run ends on "Ready …, with warnings", exit 0 (Task 3 `TestUnlockStepOutcomes`, Task 4 `TestUpReadyWithWarnings`).
5. **`init` in a pipe or with `--plain`**: the old line-by-line wizard runs with the same questions, defaults and validation, and writes byte-identical files to the form for the same answers (Task 5 `TestInitPlainAndFormWriteSameFiles`).

---

### Task 1: `internal/ui`: step interface, plain renderer, recorder, mode

**Files:**
- Create: `internal/ui/steps.go`, `internal/ui/plain.go`, `internal/ui/record.go`, `internal/ui/mode.go`, `internal/ui/ui_test.go`

**Interfaces:**
- Produces:
  ```go
  type Steps interface {
      Header(title, context string)          // "jauntbox", "tsultrim · small (cpx42) · fsn1"
      Plan(labels ...string)                 // declare pending steps in order (shown dim in live mode)
      Start(label string) Step               // label must be one of Plan's, or new (appended)
      Note(line string)                      // a dim line under the current block (e.g. Ctrl-C hand-off)
      End(ok bool, summary string, lines ...string) // "● Ready in 2m31s." + indented lines; ok=false → no ● line, just lines
      Close()                                // stop animation, leave the cursor below the block; idempotent
  }
  type Step interface {
      Detail(s string)                       // short dim detail after the duration
      Warn(msg string)                       // marks the step "!" when it finishes; msg printed under it
      Done()                                 // ✓ (or ! if warned)
      Skip(reason string)                    // –, reason dim
      Fail(err error, hint string)           // ✕, err and hint under it
  }
  func Nop() Steps                           // no output; used when Deps.Steps is nil
  func NewPlain(w io.Writer, now func() time.Time) Steps
  type Recorder struct{ Events []string }    // implements Steps; events like "start Server created", "done Server created", "warn Unlock secrets: …", "fail Provisioning: …", "skip …", "end ok Ready in …", "note …"
  func FormatDuration(d time.Duration) string
  func Live(w io.Writer, plainFlag bool, getenv func(string) string, isTerminal func(fd int) bool) bool  // true → use the live renderer
  ```
- A Step reports at most one outcome; a second call is ignored.

- [ ] **Step 1: Failing tests:** `TestFormatDuration` (table: 200ms→`0.2s`, 8100ms→`8.1s`, 41s→`41s`, 72s→`1m12s`, 600s→`10m0s`); `TestPlainLines` (fixed clock: Header then Start/Detail/Done, Start/Warn/Done, Start/Skip, Start/Fail, End → exactly `jauntbox · tsultrim · small (cpx42) · fsn1\n✓ Server created 8.1s #42\n! Unlock secrets 0.9s\n  could not unlock: busy\n– Unlock secrets no secrets set\n✕ Provisioning 10m0s\n  boom\n  check the log\n● Ready in 2m31s.\n  ssh jauntbox-u\n`; pending/started lines print nothing in plain mode); `TestPlainNoEscapes` (no byte `0x1b` in the output); `TestRecorderEvents`; `TestStepSingleOutcome` (Done then Fail → only done recorded); `TestLiveModeDecision` (table over plainFlag, `NO_COLOR` set/empty, `TERM=dumb`, isTerminal false → false; all clear → true).
- [ ] **Step 2:** `go test ./internal/ui` → FAIL.
- [ ] **Step 3:** Implement. Plain format: `<sym> <label> <duration>[ <detail>]`, messages indented two spaces; `End` prints `● <summary>` then each line indented two spaces (ok=false prints only the indented lines).
- [ ] **Step 4:** PASS; `go vet ./... && go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): step reporting with a plain renderer`.

### Task 2: Live renderer

**Files:**
- Create: `internal/ui/live.go`, `internal/ui/live_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/charmbracelet/lipgloss`)

**Interfaces:**
- Consumes: Task 1 types.
- Produces: `func NewLive(w io.Writer, now func() time.Time, tick <-chan time.Time) Steps` — `tick` nil → an internal 100 ms ticker; tests pass their own channel. Colours from Global Constraints via lipgloss with a renderer forced to true colour on `w` (lipgloss adapts down on real terminals).

- [ ] **Step 1: Failing tests** (manual tick channel, buffer writer): `TestLiveDrawsPendingAndSpinner` (after Header+Plan(3)+Start(first)+one tick: output contains `⠋` or `⠙`, the three labels, and pending `·`); `TestLiveFinishedLinesStay` (after Done on step 1 and a tick, the last frame's text, stripped of ANSI, starts with `✓ <label>`; earlier finished lines are never erased: the number of cursor-up moves never exceeds the unfinished block height); `TestLiveFailFinalises` (Fail then Close: no further writes on later ticks, the final text contains `✕` and the error, output ends with a newline and no cursor-up after the last line); `TestLiveCloseIdempotent`; `TestLiveNoAltScreen` (output never contains `\x1b[?1049h`).
- [ ] **Step 2:** `go test ./internal/ui` → FAIL.
- [ ] **Step 3:** Implement: a mutex-guarded model (finished lines, current block), a goroutine on `tick` redrawing the block with `\x1b[<n>A\x1b[J` (n = lines drawn last frame), finished lines printed once above the block; `Close` stops the goroutine and draws the final block once.
- [ ] **Step 4:** PASS (also with `-race`); `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): live step renderer`.

### Task 3: `internal/client` reports steps

**Files:**
- Modify: `internal/client/up.go` (`Deps.Steps ui.Steps`; `Up`), `internal/client/push.go` (`PushBinary`, `EnsureSupervisor`, `InstallSupervisor`), `internal/client/unlock.go` (`tryUnlock`), `internal/client/attach.go` (`Attach`, `waitProvisioned`, `waitOnline`), `internal/client/down.go`, `internal/client/resize.go`, their tests
- Test: `internal/client/*_test.go` (use `ui.Recorder`)

**Interfaces:**
- Consumes: Task 1 (`ui.Steps`, `ui.Nop`, `ui.Recorder`).
- Produces: `func (d Deps) steps() ui.Steps` (returns `ui.Nop()` when nil). Step labels, exact:
  - Up (new box): `Policy and limits`, `SSH key, firewall, volume`, `Server created` (detail `#<id>`)
  - PushBinary: `Joined the tailnet` (detail `jauntbox-<user>`), `Provisioning` (until the binary copy succeeds), `Install supervisor`, `Unlock secrets`
  - Up/InstallSupervisor on an existing box: `Box found` (detail `<name> (<status>)`) then `Install supervisor` (Skip `already running` when installed) and `Unlock secrets`
  - Down: `Ask the box to wrap up`, `Wait for the box to delete itself` (detail `up to N min`), `Delete the box directly` (only when it falls back), `Box deleted` (detail `volume kept`); `--force`: `Delete the box`, `Box deleted`. No box: `End(false, "", "no box running")`.
  - Resize: Down's steps then Up's and PushBinary's under one stream; Resize now calls `PushBinary(ctx, d, DefaultPushWait)` after `Up` instead of spawning (spec call 1).
- Warnings that used to print (`pushErr()` root-ssh retry, `warning: could not unlock`, Up's event-record warning, attach's `warning:` lines) become `Step.Warn` on the current step. Unlock outcomes: no secrets in the store → `Skip("no secrets set")`; error → `Warn(err.Error())` then `Done()`; else `Done()`.
- Lines that only announced a wait (`waiting for … to join the tailnet…`, `provisioning … (first boot takes ~N min)…`, Down's `waiting up to N min…`, `could not reach the box…`, `box did not delete itself in time…`) are removed; the step labels replace them. Errors returned are unchanged.

- [ ] **Step 1: Failing tests** (fakes as today plus a Recorder in `Deps.Steps`): `TestUpRecordsSteps` (new box: start/done in order for the three Up labels, `Server created` detail `#<id>`); `TestPushBinaryRecordsSteps` (join → provisioning → install → unlock, in order); `TestPushBinaryTimeoutFailsProvisioning` (box joins but ssh never works within the wait: `fail Provisioning: …` recorded, `Install supervisor` never started, returned error unchanged); `TestRootSSHRetryIsAWarning` (first ErrRootSSH → `warn Provisioning: …` once, nothing written to stderr); `TestUnlockStepOutcomes` (empty store → skip; EnsureUnlocked error → warn+done; ok → done); `TestExistingBoxSteps` (`Box found`, install skipped `already running`); `TestDownRecordsSteps` (graceful path and the direct-delete fallback path; `--force`); `TestDownNoBox` (end not ok, `no box running`; nothing on stdout); `TestResizeWaitsForNewBox` (Spawn never called; PushBinary steps recorded after Up's). Update existing tests that asserted on `attachOut`/stdout text.
- [ ] **Step 2:** `go test ./internal/client` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): commands report their steps`.

### Task 4: Commands: renderer choice, `up` in the foreground, end lines, `status`

**Files:**
- Modify: `cmd/jauntbox/main.go` (root `--plain` persistent flag; `up`; bare root attach; `newSteps` helper), `cmd/jauntbox/ops.go` (`down`, `resize`, `status`), tests in `cmd/jauntbox/*_test.go`
- Create: `cmd/jauntbox/steps.go` (renderer choice and end-line text)

**Interfaces:**
- Consumes: Task 1–3.
- Produces:
  - `func newSteps(cmd *cobra.Command, plain bool) ui.Steps` — live on stderr when `ui.Live(...)`, else plain on stderr.
  - `func readyLines(cfg config.Config, upAt time.Time, user string) (summary string, lines []string)` → summary `Ready in <FormatDuration>.`; lines: `Deletes itself at <HH:MM> (<evening cutoff|max uptime>) or after <idle_day> idle.` (from `lifecycle.Deadline` and `cfg.Limits.IdleDay`, in the config timezone) and `ssh jauntbox-<user>`.
  - `up` flags: `--detach` (today's background behaviour: Up with Spawn, prints the existing `installing the supervisor in the background; log: …` line, exits); `--wait` hidden-deprecated no-op.
- `up` (default): Header `jauntbox`, context `<user> · <profile> (<server type>) · <location>`; `Up` with `WithoutSpawn`; then `PushBinary` (new box) or `InstallSupervisor(ctx, d, true)` (existing box) with the same Steps; on success `End(true, readyLines…)`; if any step warned, the summary ends `, with warnings.` instead of `.`.
- Ctrl-C (spec call 3): the run context is cancelled; if the server exists (Up returned a box) the command waits for the foreground push to return, then calls `d.Spawn("internal-push")` once and `Note`s `the box keeps getting ready in the background; log: <stateDir>/push.log`; exit status as today for an interrupt. Before the server exists: no spawn.
- Bare `jauntbox`: Attach uses the same Steps and header; after its steps it prints `End(true, readyLines…)` only when it created the box, then attaches as today.
- `down`: header `jauntbox down`, context `<user>`; end `Box deleted. The home volume is kept.` `resize`: header context `resize <from> → <to>`; end as `up`.
- `status`: stdout, no animation. Lines: `<name>  <status>  <server type>` / `created <local time>` / `tailnet  ✓|✕` / `secrets  unlocked|locked` / `supervisor  <verdict from box-status>, next deadline <HH:MM> (<reason>)` when box-status JSON has them, then `rep.Note`/`rep.Err` lines as today. `--json` prints today's raw box-status JSON only. Colours only in live mode (same decision function, on stdout).
- Close the Steps on every exit path (`defer`).

- [ ] **Step 1: Failing tests** (fake Deps as in existing cmd tests; Recorder or plain buffer): `TestUpPlainWhenNotTTY` (plain output, no `0x1b`, ends with `● Ready in`); `TestUpReadyWithWarnings` (unlock warns → summary ends `, with warnings.`, exit 0); `TestReadyLines` (cutoff `19:00`, idle `2h`, up at 09:00 Europe/Paris → `Deletes itself at 19:00 (evening cutoff) or after 2h idle.`); `TestUpInterruptAfterCreateHandsOff` (cancel during PushBinary → Spawn called exactly once with `internal-push`, after PushBinary returned; note mentions `push.log`); `TestUpInterruptBeforeCreateNoSpawn`; `TestUpDetach` (Spawn called, returns immediately, prints the background line); `TestUpWaitFlagStillAccepted`; `TestDownNoBoxPlain`; `TestStatusJSONFlag` (only JSON on stdout); `TestStatusSummary` (fixed report → the lines above, `tailnet  ✓`); `TestPlainFlagForcesPlain`.
- [ ] **Step 2:** `go test ./cmd/jauntbox` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): live steps for up, down, resize and a status summary`.

### Task 5: `init` as an interactive form

**Files:**
- Modify: `internal/client/init.go` (split into `InitAnswers` + `ApplyInit`; today's `Init` becomes the plain front-end calling `ApplyInit`), `cmd/jauntbox/init.go`, `internal/provider/provider.go` (`VolumeFinder`), `internal/provider/hetzner/hetzner.go`, `go.mod`/`go.sum` (`github.com/charmbracelet/huh`)
- Create: `internal/client/initform.go`, `internal/client/initform_test.go`, `internal/client/zones.go`
- Test: `internal/client/init_test.go`, `internal/provider/hetzner/hetzner_test.go`

**Interfaces:**
- Produces:
  ```go
  type InitAnswers struct {
      User, SSHPublicKey, Timezone, Location string
      HetznerToken, TailscaleKey string
      Profiles map[string]config.Profile
      SaveEnv, AddSSHConfig bool
  }
  func ApplyInit(path string, a InitAnswers, opts ...InitOption) (config.Config, error) // writes config, env file (if SaveEnv), ssh block (if AddSSHConfig), prints the ✓ lines and the Tailscale step through the InitOption's Steps/out
  func InitForm(ctx context.Context, det Detected, lister func(token string) provider.TypeLister, finder func(token string) provider.VolumeFinder, meshCheck func(apiKey string) error, path string, opts ...InitOption) (config.Config, error)
  func SSHKeys(home string) []string                 // ~/.ssh/*.pub, sorted, detected key first
  func ZoneNames() []string                          // Ruling 6
  // provider:
  type VolumeFinder interface { FindVolume(ctx context.Context, name string) (location string, found bool, err error) }
  ```
- Form screens (huh groups), in order: overwrite confirm (only when `path` exists; default No; No → same error as today) · user (input, default det.User, today's validation) · SSH key (select over `SSHKeys`, plus "paste another" input) · timezone (select with filtering over `ZoneNames`, default det.Timezone) · Hetzner token (password input, validated by calling `lister(tok).ServerTypes` with a spinner; error shown inline) · location (select over the union of the types' locations; default: the existing volume's location from `finder(tok).FindVolume("jauntbox-<user>-home")` when found, else `fsn1`; choosing another when a volume exists shows the note `your home volume is in <loc>; volumes can't move, so the next up would fail`) · server types (three selects, small/big/huge, options `name  N vCPU · N GB  shared|dedicated  €x.xx/mo` for types available in the location, default = `PickProfiles` result) · Tailscale key (password input, validated with `meshCheck`) · review (two confirms, default Yes: save tokens to the env file, add the ssh config block).
- `cmd/jauntbox/init.go`: form when `ui.Live` says so (on stdout) and stdin is a terminal; else today's `Init`.

- [ ] **Step 1: Failing tests:** `TestInitPlainAndFormWriteSameFiles` (same `InitAnswers` through `ApplyInit` from both paths → byte-identical config, env file and ssh config in temp dirs); `TestApplyInitRespectsChoices` (SaveEnv=false → no env file, the export lines printed; AddSSHConfig=false → ssh config untouched); `TestSSHKeysOrder`; `TestZoneNamesFiltersInvalid` (temp zoneinfo dir with `Europe/Paris`, `posixrules`, `README` → only valid zones); `TestHetznerFindVolume` (httptest: found → its location; none → found=false); `TestFormLocationDefaultFromVolume` (unit-test the default-picking function `defaultLocation(found bool, loc string) string`); `TestFormTypeOptions` (formatting of one option line from a `provider.ServerType`, shared vs dedicated from the type name prefix `cpx`/`cx` vs `ccx`). The huh form itself is not driven in tests; keep its option-building and validation in plain functions that are.
- [ ] **Step 2:** `go test ./internal/client ./internal/provider/...` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `go test ./...`. Manual check (report it): run `go run ./cmd/jauntbox init --config /tmp/x.yaml` in a terminal up to the token screen and Ctrl-C → nothing written.
- [ ] **Step 5: Commit** `feat(jauntbox): interactive init form`.

### Task 6: Docs

**Files:**
- Modify: `README.md`, `site/docs.html`, `site/quickstart.html` (only where they show `up`/`init`/`status` output or flags), `docs/jauntbox/SMOKE.md` (new "Plan 06 checks")

- [ ] **Step 1:** README and site: `up` now waits until the box is ready and shows each step; `--detach` returns at once (old behaviour); `--plain` and `NO_COLOR`; `status --json`; `init` is a form (arrow keys, Esc back, Ctrl-C writes nothing) and falls back to plain questions when not in a terminal. Plain, short sentences; reuse the site's existing classes (rebuild `site/styles.css` with `cd site-src && npm run build:css` only if a new class is needed).
- [ ] **Step 2:** SMOKE "Plan 06 checks" rows: (1) `jauntbox up` on a new box shows each step with times and ends on Ready with the deletion time; (2) `jauntbox up | cat` prints plain lines only; (3) Ctrl-C during Provisioning → the note, then `jauntbox status` a few minutes later shows the box ready; (4) `jauntbox down` steps; (5) `jauntbox resize big` waits for the new box; (6) `jauntbox status` and `--json`; (7) `jauntbox init --config /tmp/t.yaml` walks the form, writes the files; (8) `jauntbox init < /dev/null` style plain fallback asks line by line; (9) bare `jauntbox` with no box shows the steps then attaches.
- [ ] **Step 3: Commit** `docs(jauntbox): the new terminal output, --detach, --plain and the init form`.
