# jauntbox 02 — Secrets and GitHub Forge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents and the owner on the box push to GitHub with short-lived, single-repo tokens minted from a private GitHub App, the App key and agent credentials live only in box RAM, the default branch only takes human merges, and no account-wide forge credential ever sits on the box.

**Architecture:** The laptop keeps master copies of secrets in `~/.config/jauntbox/secrets/` (files, mode 0600). `jauntbox setup github` creates the App through GitHub's manifest flow (owner `WA-Systems-EU` org, configurable), waits for its installation and applies a branch ruleset per installed repo, using the laptop's `gh auth token` once. Every client path that reaches the box pushes the secrets bundle over ssh stdin into `/run/jauntbox/secrets` (tmpfs, root-only); until then the box is **locked**. On the box, `sudo jauntctl mint owner/repo` mints (and caches) a single-repo installation token; a git credential helper (`jauntctl git-credential`) calls it for the owner's manual git operations, and `jaunt-repos` lists and clones through it. The supervisor revokes cached tokens before the box deletes itself. Plan 03 reuses the minter behind a per-container socket.

**Tech Stack:** Go 1.27 · cobra · GitHub REST API through a small `net/http` client (no go-github, same style as `internal/mesh/tailscale.go`) · RS256 JWT with `crypto/rsa` (no JWT library) · `ssh` stdin for secret delivery · bash for `jaunt-repos`.

**Spec:** `docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md` (§4 trust zones, §5 forge access, §7 secret delivery and the locked state, §9 setup). Plan 01: `docs/superpowers/plans/2026-11-09-jauntbox-01-walking-skeleton.md`. Plan 03 (agents, containers, wrap-up, per-container minter socket, metadata block) and the rich terminal UI (its own plan, after a design discussion) are out of scope.

## Starting point

Branch `feat/remote-workspace` at `394ee8a` (plan 01 done, live smoke mostly passed, see `docs/jauntbox/SMOKE.md` "Results log"). Never touch the git-ignored `tofu/` folder or `lenovo.txt`. Stop and ask if `git status` shows changes you didn't make. Never call real Hetzner, Tailscale or GitHub APIs from tests: use `httptest` and fakes.

## Global Constraints

- Module `github.com/WA-Systems-EU/jaunt`; binary `cmd/jauntbox`. Commits: conventional (`feat(jauntbox): …`), **no Co-Authored-By or other attribution lines**.
- GitHub App permissions, exactly: `contents: write`, `pull_requests: write`, `metadata: read`. **No `workflows`, no `administration`, no `secrets`, no `environments`.** Every mint request lists these permissions explicitly, so a token is never wider even if the App is widened by hand.
- Tokens are scoped to **one repository** per mint (spec §5). Only the repo listing uses an all-repos token, with `metadata: read` only, revoked right after use.
- App owner: config `github.org` (default empty = the admin's personal account). Thomas's setup uses `WA-Systems-EU`.
- App name: `jauntbox-<user>`. Ruleset name: `jauntbox: humans merge the default branch`.
- Ruleset: target `branch`, enforcement `active`, `ref_name.include` = `["~DEFAULT_BRANCH"]` plus `refs/heads/<p>` for each configured `github.protected_patterns` entry; rules `deletion`, `non_fast_forward`, `update`; bypass actors: only `{actor_id: 5, actor_type: "RepositoryRole", bypass_mode: "always"}` (repository admin = the human). The App is never a bypass actor.
- The default branch is never assumed: read `default_branch` from the API.
- The admin credential (`gh auth token` on the laptop, or `GITHUB_ADMIN_TOKEN` when gh is absent) is used only by `jauntbox setup github` on the client. It is never stored, never sent to the box.
- Client secrets dir: `<config dir>/secrets/` (dir 0700, files 0600). Files: `github-app.pem`, `github-app.json`, `agents.env`. A file with any group/other permission bit set is refused with `secret file <path> is readable by others (mode <m>); run chmod 600 <path>`.
- Box secrets dir: `/run/jauntbox/secrets` (tmpfs, root 0700, files 0600, marker `.loaded` written last). Token cache: `/run/jauntbox/tokens` (root 0700). Nothing secret is ever written to `/home` (the volume), `/tmp` or user-data.
- Locked message (exact, reused everywhere): ``the box is locked: its forge and agent credentials are not in memory (it was rebooted or never unlocked); run `jauntbox unlock` on your laptop``.
- Box-side commands use the box name `jauntctl` (Task 0); root ones are run by the client as `sudo jauntctl <cmd>` over ssh (the owner has passwordless sudo, plan 01).
- Model use when executing: Sonnet implementers; Opus for every review and for any task that stalls.

## Review Focus

1. **Box rebooted, or the push helper never ran:** `git push` on the box must fail with the locked message naming `jauntbox unlock`, not a raw 401 or a password prompt. Test in Task 6.
2. **App widened by hand in GitHub's UI (e.g. `workflows: write` added):** minted tokens must still carry only the three permissions, and `setup github --check` must report the drift. Tests in Tasks 2 and 3.
3. **A repo the App is not installed on:** the credential helper fails fast with a message naming the install URL (`https://github.com/apps/<slug>/installations/new`), not a hang. Test in Task 6.
4. **`setup github` re-run** (after installing the App on more repos, or after a crash between steps): no second App, no duplicate ruleset (update in place), already-done steps skipped. Tests in Tasks 3 and 4.
5. **A cached token about to expire** (GitHub caps at 1 h): the minter re-mints when fewer than 10 min remain, so a long `git push` never starts with a dying token. Test in Task 6.

---

## File Structure

```
cmd/jauntbox/main.go                      # dispatch on argv[0]: jauntbox (laptop) vs jauntctl (box)
internal/config/config.go                 # + GitHub{Org, ProtectedPatterns}
internal/secrets/store.go                 # client secret store (files 0600) + Bundle
internal/forge/forge.go                   # Forge interface, Repo, Token, Guarantees
internal/forge/github/app.go              # App: JWT, installation, Mint, Revoke, Repos, CheckPermissions
internal/forge/github/admin.go            # Admin: Login, ConvertManifest, EnsureRuleset, VerifyRuleset
internal/client/setupgithub.go            # SetupGitHub flow (manifest server, install wait, rulesets)
internal/client/unlock.go                 # PushSecrets, EnsureUnlocked
internal/vault/vault.go                   # box side: Load bundle into tmpfs, Locked, Read
internal/minter/minter.go                 # box side: Mint with cache, RevokeAll, Repos
internal/remote/ssh.go                    # + RunInput (stdin)
cmd/jauntbox/secrets.go                   # `secrets set|list`, `unlock`, `setup github` (client)
cmd/jauntbox/boxforge.go                  # `secrets-load`, `mint`, `repos`, `git-credential` (box)
provision/jaunt-repos.sh                  # rewritten: App repos via `jauntctl repos`, https clone
provision/jaunt-repos_test.sh             # stubbed `jauntctl` on PATH
docs/jauntbox/SMOKE.md, README.md         # plan 02 checks, setup section
```

## Where each command runs

| Command | Runs on | Started by |
|---|---|---|
| `jauntbox` (bare), `up`, `down`, `status`, `night`, `resize`, `ssh`, `init`, `acl-snippet` | laptop | the owner |
| `jauntbox setup github [--check]`, `secrets set\|list`, `unlock` | laptop | the owner |
| `jauntbox internal-push` | laptop | `up` (detached helper) |
| `jauntctl supervise` | box, root | systemd timer |
| `jauntctl stop`, `night-set`, `box-status`, `secrets-load` | box, root | the laptop over ssh (`sudo`) |
| `jauntctl mint`, `repos` | box, root | the credential helper and `jaunt-repos` (`sudo -n`) |
| `jauntctl git-credential` | box, as the owner | `git` |
| `jaunt-repos` | box, as the owner | the owner |

---

### Task 0: One binary, two names: `jauntbox` (laptop) and `jauntctl` (box)

**Files:**
- Modify: `cmd/jauntbox/main.go` (split the root into `newClientCmd()` and `newBoxCmd()`; `main` picks by `filepath.Base(os.Args[0])`)
- Modify: `internal/client/push.go` (`installCmd`, `supervisorCheck`), `internal/client/down.go`, `internal/client/night.go`, `internal/client/status.go` (remote calls use `jauntctl`)
- Modify: `internal/cloudinit/units/jauntbox-supervise.service` (`ExecStart=/usr/local/bin/jauntctl supervise`)
- Test: `cmd/jauntbox/main_test.go`, `internal/client/*_test.go`, `internal/cloudinit/render_test.go`

**Interfaces:**
- Produces:
  - `const BoxName = "jauntctl"` in `cmd/jauntbox`; `func rootFor(argv0 string) *cobra.Command` → `newBoxCmd()` when the base name is `jauntctl`, else `newClientCmd()`.
  - Box command set: `supervise`, `stop`, `night-set`, `box-status` (later tasks add `secrets-load`, `mint`, `repos`, `git-credential`). Client command set: everything else.
  - On the client, each box command name stays registered as a **hidden** command that runs the box command (transition alias: boxes created before this change run `/usr/local/bin/jauntbox supervise` from their unit). Remove the aliases in plan 03.
  - Wrong-side message, exact, for a laptop command run as `jauntctl` (e.g. `jauntctl up`): ``"up" is a laptop command: run `jauntbox up` on your laptop``. `jauntctl --help` lists box commands only.
  - `installCmd` = `sudo install -m 0755 /tmp/jauntbox /usr/local/bin/jauntctl && sudo ln -sf jauntctl /usr/local/bin/jauntbox && sudo systemctl enable --now jauntbox-supervise.timer` (the symlink keeps old units working through the hidden aliases).
  - `supervisorCheck` = `test -x /usr/local/bin/jauntctl && systemctl is-enabled --quiet jauntbox-supervise.timer` (an old box without `jauntctl` fails the check and gets re-pushed).
  - Remote calls: `sudo jauntctl stop`, `sudo jauntctl night-set --until …`, `sudo jauntctl box-status --json`.

- [ ] **Step 1: Write failing tests:**
  - `TestRootForBoxName`: `rootFor("/usr/local/bin/jauntctl")` has `supervise`, `box-status`, and no `up`; `rootFor("jauntbox")` has `up`, `status`, and `supervise` only as hidden.
  - `TestJauntctlRejectsLaptopCommand`: executing `jauntctl up` → error equals the exact wrong-side message.
  - `TestClientHelpHidesBoxCommands`: `jauntbox --help` output contains no `supervise`/`box-status`.
  - `TestRemoteCallsUseJauntctl`: fakes record `sudo jauntctl stop`, `night-set`, `box-status`.
  - `TestInstallCmdInstallsJauntctl` / `TestSupervisorCheckNeedsJauntctl`: exact strings above.
  - `render_test.go`: the supervise unit's `ExecStart` is `/usr/local/bin/jauntctl supervise`.
- [ ] **Step 2: Run** `go test ./cmd/jauntbox/ ./internal/client/ ./internal/cloudinit/` → FAIL.
- [ ] **Step 3: Implement.** Each command constructor is shared; the two roots just add different sets. No second build target: the box receives the same binary under the name `jauntctl`.
- [ ] **Step 4: Run** `go vet ./... && go test -count=1 ./... && sh internal/cloudinit/units/safety-net_test.sh` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): box-side commands answer to jauntctl`

---

### Task 1: Config `github` section and the client secret store

**Files:**
- Modify: `internal/config/config.go` (Config gains `GitHub GitHub \`yaml:"github"\``)
- Create: `internal/secrets/store.go`, `internal/secrets/store_test.go`
- Create: `cmd/jauntbox/secrets.go` (`secrets set`, `secrets list`)
- Test: `internal/config/config_test.go`, `cmd/jauntbox/main_test.go`

**Interfaces:**
- Produces:
  - `config.GitHub{Org string \`yaml:"org"\`; ProtectedPatterns []string \`yaml:"protected_patterns"\`}`; validation: each pattern non-empty, no leading `refs/`.
  - `secrets.DirFor(cfgPath string) string` → `filepath.Join(filepath.Dir(cfgPath), "secrets")`
  - `secrets.Store{Dir string}` with `Put(name string, data []byte) error`, `Get(name string) ([]byte, error)` (wraps `os.ErrNotExist`), `SetEnv(key, value string) error`, `EnvKeys() ([]string, error)`, `Bundle() (Bundle, error)`
  - `secrets.Bundle{Files map[string][]byte \`json:"files"\`}`; constants `AppKeyFile = "github-app.pem"`, `AppMetaFile = "github-app.json"`, `AgentEnvFile = "agents.env"`
  - `var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)`, `var EnvKeyRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)`

- [ ] **Step 1: Write failing tests** in `store_test.go`:
  - `TestPutCreatesPrivateFiles`: dir mode 0700, file mode 0600; `Put("../x", …)` errors.
  - `TestSetEnvUpserts`: `SetEnv("CLAUDE_CODE_OAUTH_TOKEN","a")` then `"b"` → `agents.env` has exactly one line `CLAUDE_CODE_OAUTH_TOKEN=b`; other keys untouched; `SetEnv("bad key", …)` errors; a value containing `\n` errors.
  - `TestBundleRefusesOpenFile`: chmod a file to 0644 → error equals the Global Constraints message with the path and `644`.
  - `TestBundleEmptyDir`: missing dir → empty `Bundle` and nil error (a client without secrets still unlocks).
  - `config_test.go`: `github.org` and `protected_patterns` round-trip; `protected_patterns: ["refs/heads/x"]` is rejected with `protected_patterns: write "x", not "refs/heads/x"`.
- [ ] **Step 2: Run** `go test ./internal/secrets/ ./internal/config/` → FAIL (undefined).
- [ ] **Step 3: Implement.** Writes are atomic (temp file in the same dir, chmod 0600, rename). `secrets set <KEY>` reads the value with `term.ReadPassword` when stdin is a terminal, else one line from stdin; `secrets list` prints key names only, never values.
- [ ] **Step 4: Run** `go test ./internal/secrets/ ./internal/config/ ./cmd/jauntbox/` → PASS. Add `TestSecretsListNeverPrintsValues` in `cmd/jauntbox/main_test.go`.
- [ ] **Step 5: Commit** `feat(jauntbox): client secret store and secrets set/list`

### Task 2: Forge interface and the GitHub App client

**Files:**
- Create: `internal/forge/forge.go`, `internal/forge/github/app.go`, `internal/forge/github/app_test.go`

**Interfaces:**
- Produces:
  - `forge.Repo{Owner, Name, DefaultBranch, CloneURL string}`; `func (r Repo) FullName() string` (`owner/name`)
  - `forge.Token{Value, Repo string; ExpiresAt time.Time}`
  - `forge.Guarantees{AutoExpiry, WorkflowIsolation bool}`
  - `forge.Forge` interface: `Repos(ctx) ([]Repo, error)`, `Mint(ctx, repo string) (Token, error)` (repo = `owner/name`), `Revoke(ctx, token string) error`, `Guarantees() Guarantees`
  - `var forge.ErrNotInstalled = errors.New("repository is not installed on the forge app")`
  - `github.AppMeta{AppID int64 \`json:"app_id"\`; Slug string \`json:"slug"\`; Owner string \`json:"owner"\`; OwnerIsOrg bool \`json:"owner_is_org"\`; InstallationID int64 \`json:"installation_id,omitempty"\`}`
  - `github.NewApp(meta AppMeta, pemKey []byte, apiURL string) (*App, error)` — `apiURL == ""` → `https://api.github.com`; parses PKCS#1 and PKCS#8 RSA keys.
  - `(*App)` implements `forge.Forge`; `Guarantees()` returns `{AutoExpiry: true, WorkflowIsolation: true}`.
  - `(*App).CheckPermissions(ctx) error` — `GET /app` (JWT); error listing every permission outside `Permissions` or above its level.
  - `var github.Permissions = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}`
  - `(*App).InstallURL() string` → `https://github.com/apps/<slug>/installations/new`

- [ ] **Step 1: Write failing tests** (httptest server as the API; generate a 2048-bit test key in the test):
  - `TestJWT`: header `{"alg":"RS256","typ":"JWT"}`; claims `iss` = app id as string, `iat` = now−60s, `exp` = now+9m; signature verifies with the public key.
  - `TestMintRequestsOneRepoAndExplicitPermissions`: server asserts `POST /app/installations/7/access_tokens`, `Authorization: Bearer <jwt>`, body `{"repositories":["web"],"permissions":{"contents":"write","metadata":"read","pull_requests":"write"}}`; returns `{"token":"ghs_x","expires_at":"2026-11-10T12:00:00Z"}` → `Token{Value:"ghs_x", Repo:"WA-Systems-EU/web", ExpiresAt:…}`.
  - `TestMintRepoOfOtherOwnerRefused`: `Mint(ctx,"someone/web")` → error, no HTTP call.
  - `TestMintNotInstalled`: API answers 422 with `"There is at least one repository that does not exist or is not accessible"` → `errors.Is(err, forge.ErrNotInstalled)`.
  - `TestInstallationLookup`: `InstallationID == 0` → `GET /orgs/WA-Systems-EU/installation` (OwnerIsOrg) or `GET /users/<owner>/installation`; 404 → error containing the install URL.
  - `TestReposPaginatesAndRevokesListingToken`: mint with `{"permissions":{"metadata":"read"}}` and no `repositories`; `GET /installation/repositories?per_page=100&page=1..2` (100 + 3 repos); then `DELETE /installation/token` with that token; result has 103 repos with `DefaultBranch` from each repo's `default_branch` (use `trunk`, `master` in fixtures, never assume `main`).
  - `TestRevoke`: `DELETE /installation/token`, `Authorization: token ghs_x`; 204 and 401 (already expired) both return nil.
  - `TestCheckPermissionsDrift`: `GET /app` returns permissions with `"workflows":"write"` → error mentions `workflows`; exact set → nil.
- [ ] **Step 2: Run** `go test ./internal/forge/...` → FAIL.
- [ ] **Step 3: Implement** with `net/http` (10 s timeout), `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`. JWT: base64url (no padding) of header and claims, `rsa.SignPKCS1v15(nil, key, crypto.SHA256, sha256(signingInput))`.
- [ ] **Step 4: Run** `go test ./internal/forge/...` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): GitHub App client (single-repo tokens)`

### Task 3: GitHub admin operations (manifest conversion, rulesets)

**Files:**
- Create: `internal/forge/github/admin.go`, `internal/forge/github/admin_test.go`

**Interfaces:**
- Consumes: `AppMeta`, `Permissions` (Task 2).
- Produces:
  - `github.Admin{Token, APIURL string}` (`APIURL == ""` → `https://api.github.com`)
  - `(Admin).Login(ctx) (string, error)` — `GET /user`
  - `(Admin).ConvertManifest(ctx, code string) (AppMeta, []byte, error)` — `POST /app-manifests/{code}/conversions`; returns meta (`id`, `slug`, `owner.login`, `owner.type == "Organization"`) and the `pem`.
  - `(Admin).EnsureRuleset(ctx, owner, repo string, patterns []string) (created bool, err error)`
  - `(Admin).VerifyRuleset(ctx, owner, repo string, patterns []string) error`
  - `const github.RulesetName = "jauntbox: humans merge the default branch"`
  - `func github.RulesetBody(patterns []string) map[string]any` (exact JSON per Global Constraints)

- [ ] **Step 1: Write failing tests:**
  - `TestRulesetBody`: `RulesetBody([]string{"release/*"})` marshals to the exact JSON from Global Constraints with include `["~DEFAULT_BRANCH","refs/heads/release/*"]`.
  - `TestEnsureRulesetCreates`: `GET /repos/o/r/rulesets` returns `[]` → `POST /repos/o/r/rulesets` with `RulesetBody` → `created == true`.
  - `TestEnsureRulesetUpdatesInPlace`: list returns `[{"id":9,"name":"jauntbox: humans merge the default branch"}]` → `PUT /repos/o/r/rulesets/9`, no POST → `created == false`.
  - `TestVerifyRulesetReportsDrift`: fetched ruleset (`GET /repos/o/r/rulesets/9`) with `enforcement: "evaluate"` → error mentions `enforcement`; with an extra bypass actor of type `Integration` → error mentions `bypass`; missing ruleset → error mentions `jauntbox setup github`.
  - `TestConvertManifest`: returns meta + pem from a fixture conversion response.
- [ ] **Step 2: Run** `go test ./internal/forge/github/ -run 'Ruleset|Manifest'` → FAIL.
- [ ] **Step 3: Implement** (admin calls use `Authorization: Bearer <token>`).
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): GitHub rulesets and App manifest conversion`

### Task 4: `jauntbox setup github`

**Files:**
- Create: `internal/client/setupgithub.go`, `internal/client/setupgithub_test.go`
- Modify: `cmd/jauntbox/secrets.go` (add `setup github` with `--check`)

**Interfaces:**
- Consumes: `secrets.Store`, `github.Admin`, `github.NewApp`, `(*App).CheckPermissions`, `(*App).Repos`, `EnsureRuleset`, `VerifyRuleset`.
- Produces:
  - `client.SetupGitHubOpts{Cfg config.Config; Store secrets.Store; Admin github.Admin; AppAPIURL, WebURL string; OpenBrowser func(url string) error; Out io.Writer; InstallWait time.Duration; Listen func() (net.Listener, error)}`
  - `client.SetupGitHub(ctx, o SetupGitHubOpts) error`; `client.CheckGitHub(ctx, o SetupGitHubOpts) error`
  - `client.AppManifest(user, redirectURL string) []byte`
  - `client.AdminToken(lookPath func(string)(string,error), run func(argv ...string)([]byte,error), getenv func(string) string) (string, error)` — `gh auth token` if gh exists and succeeds, else `GITHUB_ADMIN_TOKEN`, else error ``no GitHub admin credential: run `gh auth login` on this laptop or set GITHUB_ADMIN_TOKEN (used once, never stored)``.

Flow of `SetupGitHub` (each step skipped when already done, so a re-run resumes):
1. If `github-app.json` and `github-app.pem` exist → reuse. Else: listen on `127.0.0.1:0`; serve `/` = an HTML page whose form auto-POSTs field `manifest` to `<WebURL>/organizations/<org>/settings/apps/new?state=<32 hex>` (or `<WebURL>/settings/apps/new?state=…` when `github.org` is empty); serve `/callback?code=&state=`: wrong state → 400; right state → `ConvertManifest`, `Store.Put` the pem and the meta JSON, answer "done, you can close this tab". Print the local URL and call `OpenBrowser` (failure only prints).
2. Print `InstallURL()` and "install it on the repositories agents may work on"; open it; poll the installation lookup every 5 s up to `InstallWait` (default 10 min); store `installation_id` in the meta.
3. `CheckPermissions`; drift → error.
4. `Repos`; for each: `EnsureRuleset` then print `✓ <owner/repo> (default branch <b>) ruleset created|updated`.
5. Print guarantees: `token auto-expiry: yes (1 h)`, `workflow isolation: yes (no workflows permission)`, then ``next: `jauntbox unlock` (or any `jauntbox` command) pushes the App key to the box``.

`CheckGitHub` (`--check`): no browser, no writes; `CheckPermissions` + `VerifyRuleset` for every installed repo; prints one line per repo; non-nil error if anything drifted.

`AppManifest` JSON: `name` `jauntbox-<user>`, `url` `https://github.com/WA-Systems-EU/jaunt`, `redirect_url`, `public: false`, `default_permissions` = `Permissions`, `default_events: []`, `hook_attributes: {"url": "https://example.invalid/jauntbox", "active": false}`.

- [ ] **Step 1: Write failing tests** (httptest for API and web; `OpenBrowser` fake that performs the browser's part by calling the callback with the state it extracts from the page):
  - `TestSetupGitHubCreatesAppOnce`: full flow → store holds pem + meta with `installation_id`; second run makes zero manifest/conversion requests.
  - `TestSetupGitHubWrongStateRejected`: callback with a wrong state → 400 and nothing stored.
  - `TestSetupGitHubAppliesRulesetPerRepo`: 2 installed repos → 2 `EnsureRuleset` calls; output lists both with their default branches.
  - `TestSetupGitHubPermissionDrift`: `/app` has `workflows` → error, no ruleset calls.
  - `TestSetupGitHubInstallTimeout`: installation never appears → error naming the install URL and ``re-run `jauntbox setup github` ``.
  - `TestAppManifest`: exact JSON; no `workflows` key.
  - `TestAdminTokenOrder`: gh present → gh token; gh missing → env; neither → exact error.
- [ ] **Step 2: Run** `go test ./internal/client/ -run 'SetupGitHub|AppManifest|AdminToken'` → FAIL.
- [ ] **Step 3: Implement**; cobra command `setup github [--check]` builds `Admin` from `AdminToken(exec.LookPath, …, os.Getenv)`, `OpenBrowser` = `xdg-open` (Linux) / `open` (darwin).
- [ ] **Step 4: Run** `go test ./internal/client/ ./cmd/jauntbox/` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): setup github (App via manifest flow, rulesets)`

### Task 5: Secret delivery and the locked state

**Files:**
- Create: `internal/vault/vault.go`, `internal/vault/vault_test.go`
- Create: `internal/client/unlock.go`, `internal/client/unlock_test.go`
- Modify: `internal/remote/ssh.go` (add `RunInput`), `internal/client/fakes_test.go`, `internal/client/up.go` (Deps gains `Secrets secrets.Store`), `internal/client/push.go`, `internal/client/status.go`, `internal/client/attach.go`, `cmd/jauntbox/ops.go` (`ssh`), `cmd/jauntbox/main.go` (`box-status` adds `"locked"`), `cmd/jauntbox/secrets.go` (`unlock`), `cmd/jauntbox/boxforge.go` (`secrets-load`)

**Interfaces:**
- Consumes: `secrets.Bundle`, `secrets.NameRE`.
- Produces:
  - `remote.Runner.RunInput(ctx, host string, stdin []byte, args ...string) ([]byte, error)`
  - `vault.Dir = "/run/jauntbox/secrets"`, `vault.Load(dir string, b secrets.Bundle) error`, `vault.Locked(dir string) bool`, `vault.Read(dir, name string) ([]byte, error)` (returns `ErrLocked` when locked)
  - `var vault.ErrLocked = errors.New(<locked message>)`
  - `client.PushSecrets(ctx, d Deps) error` — `Bundle()` → JSON → `RunInput(host, json, "sudo", "jauntctl", "secrets-load")`
  - `client.EnsureUnlocked(ctx, d Deps) error` — `Run(host, "sudo", "-n", "test", "-e", "/run/jauntbox/secrets/.loaded")`; on failure `PushSecrets`.
  - box command `secrets-load` (root): reads ≤ 1 MiB JSON bundle from stdin → `vault.Load`.

`vault.Load`: names must match `NameRE`; create dir 0700; for each file write `<name>.tmp` 0600 then rename; remove files not in the bundle (a secret deleted on the laptop disappears from the box); write `.loaded` last.

Wiring (each a call to `EnsureUnlocked`, never fatal for the command's main job — print `warning: could not unlock the box: <err>` and go on):
- `PushBinary`: after the binary is installed (so the internal-push helper unlocks a new box).
- `EnsureSupervisor`: after the check/push.
- `Status`: when online; report gains `Locked bool` from box-status `"locked"` (set after any unlock attempt).
- `Attach`: after `EnsureSupervisor`.
- `jauntbox ssh`: before `syscall.Exec` (config is loaded there; build Deps with `loadDeps`).
- `jauntbox unlock`: `PushSecrets` unconditionally (fatal on error).

- [ ] **Step 1: Write failing tests:**
  - `vault_test.go`: `TestLoadWritesPrivateFilesAndMarkerLast` (use `t.TempDir()`; marker exists only after all files; modes 0700/0600); `TestLoadRejectsBadNames` (`../x`, `.loaded`, `A`); `TestLoadRemovesStaleFiles`; `TestLockedWithoutMarker`; `TestReadWhenLocked` → `errors.Is(err, ErrLocked)`.
  - `unlock_test.go`: `TestEnsureUnlockedPushesOnlyWhenLocked` (fake Remote: marker test fails → one `RunInput` with the bundle JSON; succeeds → none); `TestPushSecretsRefusesOpenFile` (0644 file → error from Task 1, no remote call); `TestPushBinaryUnlocksAfterInstall` (call order `copy, install, test-marker, secrets-load`); `TestStatusReportsLocked`.
  - `cmd/jauntbox/main_test.go`: `TestSecretsLoadRejectsOversizedInput` (> 1 MiB → error).
- [ ] **Step 2: Run** `go test ./internal/vault/ ./internal/client/ ./cmd/jauntbox/` → FAIL.
- [ ] **Step 3: Implement** as above; `RunInput` is `Run` with `cmd.Stdin = bytes.NewReader(stdin)`.
- [ ] **Step 4: Run** `go vet ./... && go test -count=1 ./...` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): push secrets to box RAM; locked state and unlock`

### Task 6: Box-side minter, `repos` and the git credential helper

**Files:**
- Create: `internal/minter/minter.go`, `internal/minter/minter_test.go`
- Modify: `cmd/jauntbox/boxforge.go` (`mint`, `repos`, `git-credential`), `internal/client/push.go` (`installCmd` adds the git config)
- Test: `cmd/jauntbox/main_test.go`

**Interfaces:**
- Consumes: `vault.Read`, `vault.ErrLocked`, `github.NewApp`, `forge.Forge`, `forge.ErrNotInstalled`, `events.Sink`.
- Produces:
  - `minter.Minter{SecretsDir, CacheDir string; Now func() time.Time; NewForge func(meta github.AppMeta, key []byte) (forge.Forge, error); Events events.Sink}`
  - `minter.New(ev events.Sink) *Minter` (production paths `/run/jauntbox/secrets`, `/run/jauntbox/tokens`, `NewForge` = `github.NewApp` with the default API URL). `minter` must not import `supervisor` (the supervisor imports `minter` in Task 8); box commands pass `events.JSONLFile(supervisor.DefaultPaths().Events)`.
  - `(*Minter).Mint(ctx, repo string) (forge.Token, error)`; `(*Minter).Repos(ctx) ([]forge.Repo, error)`; `(*Minter).RevokeAll(ctx) (int, error)`
  - `var minter.ErrNotSetUp = errors.New("GitHub is not set up: run `jauntbox setup github` on your laptop, then `jauntbox unlock`")`
  - `const minter.ReuseMargin = 10 * time.Minute`
  - events `token.minted`, `token.revoked`, `token.revoke_failed` (fields `repo`, `expires_at`; never the token)
  - box commands: `mint <owner/repo>` (root) prints `{"token":…,"expires_at":…}`; `repos [--json]` (root); `git-credential get|store|erase` (owner, no root)

Cache: one file per repo `<owner>__<name>.json` (`{"token","repo","expires_at"}`, 0600). `Mint` reuses a cached token when `ExpiresAt − Now > ReuseMargin`, else mints, writes the cache, emits `token.minted`. `RevokeAll` revokes every cached token, deletes its file, emits per token; continues past failures and returns the first error.

`git-credential get`: reads git's `key=value` lines from stdin; ignores anything but `host=github.com` with a `path` (needs `credential.useHttpPath`); path `owner/repo(.git)` → runs `sudo -n /usr/local/bin/jauntctl mint owner/repo`; prints `username=x-access-token` and `password=<token>`. On error: one line on stderr — the locked message, `ErrNotSetUp`, or `<owner/repo> is not installed on the jauntbox App: add it at <install URL>, then re-run `jauntbox setup github` on your laptop` — and exit 1. `store` and `erase`: read stdin, exit 0.

`installCmd` appends (run as the owner):
`git config --global --replace-all credential.https://github.com.helper '' && git config --global --add credential.https://github.com.helper '/usr/local/bin/jauntctl git-credential' && git config --global credential.https://github.com.useHttpPath true` (the empty entry resets helpers set elsewhere, e.g. gh's).

- [ ] **Step 1: Write failing tests** (fake `forge.Forge`; `t.TempDir()` for both dirs; fake clock):
  - `TestMintCachesUntilMargin`: first `Mint` → 1 forge mint; second at +30 min with a 1 h token → cached; at +51 min → re-mint (Review Focus 5).
  - `TestMintLocked`: no `.loaded` → `errors.Is(err, vault.ErrLocked)`, no forge call (Review Focus 1).
  - `TestMintNotSetUp`: loaded but no `github-app.pem` → `ErrNotSetUp`.
  - `TestRevokeAllContinuesPastFailure`: 3 cached, revoke of #2 fails → 3 revoke calls, 2 files gone, events 2× `token.revoked` + 1× `token.revoke_failed`, error returned.
  - `TestEventsNeverContainToken`: grep emitted events for the token value → absent.
  - `cmd`: `TestGitCredentialGet` (stub `mint` runner returns JSON → exact output lines); `TestGitCredentialIgnoresOtherHosts` (`host=gitlab.com` → no output, exit 0, no mint); `TestGitCredentialNotInstalled` (stub returns the not-installed error → stderr names the install URL, exit 1) (Review Focus 3); `TestGitCredentialLocked` (stderr is the locked message).
  - `internal/client`: `TestInstallCmdConfiguresGitHelper` (contains the three `git config` parts in order).
- [ ] **Step 2: Run** `go test ./internal/minter/ ./cmd/jauntbox/ ./internal/client/` → FAIL.
- [ ] **Step 3: Implement.** `git-credential` takes the mint runner as an injectable `func(repo string) (forge.Token, error)` so tests need no sudo.
- [ ] **Step 4: Run** `go vet ./... && go test -count=1 ./...` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): box-side token minter and git credential helper`

### Task 7: `jaunt-repos` through the App

**Files:**
- Modify: `provision/jaunt-repos.sh`
- Create: `provision/jaunt-repos_test.sh`

**Interfaces:**
- Consumes: `sudo -n jauntctl repos --json` → `[{"owner","name","default_branch","clone_url"}]` (Task 6).

Behaviour: no `gh` use at all. If `~/.config/gh/hosts.yml` exists, print first: `warning: an account-wide GitHub login (gh auth login) is stored on this volume; run "gh auth logout" and revoke it at https://github.com/settings/applications`. List already-cloned repos (as today). List the App's installed repos with their default branches, numbered; read picks (`all` / numbers / blank); clone with `git clone <clone_url> ~/src/<name>` (the credential helper supplies the token); skip existing dirs. `jauntctl repos` failing (locked / not set up) → print its stderr and exit 1.

- [ ] **Step 1: Write the failing test** `provision/jaunt-repos_test.sh` (style of `internal/cloudinit/units/safety-net_test.sh`): temp `HOME`, a stub `sudo` that drops `-n` and execs the rest, a stub `jauntctl` printing two repos, a stub `git` logging its argv; feed `all` on stdin → asserts two `git clone` lines with the clone URLs; with `hosts.yml` present → warning printed; stub `jauntctl` exiting 1 with the locked message → script exits 1 and shows it; asserts no `gh` invocation (stub `gh` that fails the test).
- [ ] **Step 2: Run** `bash provision/jaunt-repos_test.sh` → FAIL.
- [ ] **Step 3: Rewrite** `jaunt-repos.sh` (bash, `set -euo pipefail`, `jq` is installed by provisioning).
- [ ] **Step 4: Run** `bash provision/jaunt-repos_test.sh && go test ./provision/ ./internal/cloudinit/` → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): jaunt-repos lists and clones through the GitHub App`

### Task 8: Revoke tokens at teardown

**Files:**
- Modify: `internal/supervisor/run.go` (Supervisor gains `Revoke func(ctx context.Context) error`; `New` wires `minter.New(s.Events).RevokeAll`)
- Test: `internal/supervisor/run_test.go`

- [ ] **Step 1: Write failing tests:** `TestDeleteRevokesTokensFirst` (call order `revoke, sync, delete-self`); `TestRevokeFailureDoesNotBlockDelete` (revoke errors → event `token.revoke_failed` with the error, delete still happens); `TestKeepVerdictDoesNotRevoke`.
- [ ] **Step 2: Run** `go test ./internal/supervisor/` → FAIL.
- [ ] **Step 3: Implement** in the `lifecycle.Delete` branch; `Revoke == nil` is a no-op. Revoke runs with its own 30 s timeout.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(jauntbox): revoke forge tokens before the box deletes itself`

### Task 9: Carry-forwards from plan 01 and the smoke run

**Files:**
- Modify: `internal/client/push.go` (`InstallSupervisor`), `internal/provider/hetzner/hetzner.go` (`FindBox`, `EnsureSSHKey`, `SelfID`), `internal/config/config.go` (`DefaultPath`), `internal/supervisor/run.go` (one `now`), `internal/client/sshconfig.go`
- Test: the matching `_test.go` files

- [ ] **Step 1: Write failing tests:**
  - `TestInstallSupervisorNoWaitSkipsPushWhenInstalled`: `wait=false`, `supervisorCheck` succeeds → no `Spawn`; fails → one `Spawn("internal-push")`.
  - `TestFindBoxSkipsDeletingServer`: list returns one server with `status: "deleting"` → `nil, nil`; one `deleting` + one `running` → the running one.
  - `TestEnsureSSHKeyDifferentKeyRefused`: existing key with another fingerprint → error ``ssh key "jauntbox-<user>" in Hetzner differs from ssh_public_key in your config: delete it in the Hetzner console or fix the config``.
  - `TestSelfIDIgnoresProxy`: `HTTPS_PROXY`/`HTTP_PROXY` set to an unreachable address → still reaches the httptest metadata server.
  - `TestDefaultPathNoHome`: `HOME` and `XDG_CONFIG_HOME` empty → `DefaultPath() (string, error)` returns an error naming `--config`. Update callers.
  - `TestRunUsesOneNow`: a `Now` that advances each call is read once per `Run` (count calls).
  - `TestEnsureSSHConfigWarnsOnBareHostEntry`: an existing `Host jauntbox-<user>` block without `StrictHostKeyChecking` → returns `added=false` and a warning string naming the missing options.
- [ ] **Step 2: Run** `go test ./internal/...` → FAIL.
- [ ] **Step 3: Implement.** `InstallSupervisor(wait=false)` runs `supervisorCheck` first. `FindBox` filters `hcloud.ServerStatusDeleting` (the `up` volume-detach wait from `67d08f1` covers the gap). `SelfID` uses an `http.Client` whose transport has `Proxy: nil`. `EnsureSSHConfig` returns `(added bool, warning string, err error)`.
- [ ] **Step 4: Run** `go vet ./... && go test -count=1 ./...` → PASS.
- [ ] **Step 5: Commit** `fix(jauntbox): plan 01 carry-forwards (no-wait up re-push, deleting boxes, ssh key drift, proxy, config path)`

### Task 10: Docs and smoke checks

**Files:**
- Modify: `README.md` (setup section: `jauntbox setup github`, `jauntbox secrets set CLAUDE_CODE_OAUTH_TOKEN`, `jauntbox unlock`, the locked state), `docs/jauntbox/SMOKE.md` (new section "Plan 02: forge and secrets")

SMOKE.md section, each with what to record:
1. `jauntbox setup github` creates `jauntbox-<user>` under `WA-Systems-EU`; install it on one scratch repo; ruleset visible in the repo settings. Record whether GitHub accepted the manifest as written (`hook_attributes`).
2. On the box: `jaunt-repos` lists the scratch repo with its real default branch and clones it; `git push origin HEAD:agent/smoke` works; `git push origin HEAD:<default branch>` is **rejected** by the ruleset; merging the PR in the web UI as the human **works** (admin bypass).
3. `sudo reboot` the box → `git push` prints the locked message; `jauntbox unlock` on the laptop → push works again.
4. `jauntbox setup github --check` after adding `workflows` permission by hand in the App settings → reports drift; minted tokens still lack `workflows` (`curl -H "Authorization: token $(sudo jauntctl mint o/r | jq -r .token)" https://api.github.com/installation/repositories` works; editing `.github/workflows/x.yml` and pushing is rejected).
5. `jauntbox down` → events on the volume show `token.revoked` for each minted token; the old token returns 401.
6. `ls /home` and `grep -r ghs_ /home` find no token or key on the volume.

- [ ] **Step 1: Write the sections.**
- [ ] **Step 2: Run** `go vet ./... && go test -count=1 ./... && sh internal/cloudinit/units/safety-net_test.sh && bash provision/jaunt-repos_test.sh` → all pass.
- [ ] **Step 3: Commit** `docs(jauntbox): plan 02 setup and smoke checks`

---

## Not in this plan

- Per-container minter socket bound to one repo, agent credential injection into containers, metadata endpoint block, wrap-up: plan 03.
- Rich terminal UI: its own plan after a design discussion.
- Attended mode, Gitea/Forgejo adapter, credential sweep (GitHub tokens auto-expire): phase 2.
- Deferred minors not picked up here: CI push+PR double runs; Hetzner per-location deprecation in `PickProfiles`; tty-less humans (VS Code Remote) counting as idle.
