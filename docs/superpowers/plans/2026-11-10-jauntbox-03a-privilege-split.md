# jauntbox 03a: privilege split — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Code running as the box user (for example a test an agent rigged) can no longer become root or read the box's secrets: jauntbox's own root work comes from the laptop over Tailscale SSH as `root`, the box user gets password `sudo` plus passwordless `sudo` for a few safe `jauntctl` commands, leaves the `docker` group, and can't reach the Hetzner metadata service.

**Architecture:** The laptop gets a second ssh runner as `root@jauntbox-<user>` and moves every privileged step to it (binary install, units, secrets load, lock check, stop, night). On the box, a sudoers file grants password sudo for everything and `NOPASSWD` only for `jauntctl mint|repos|box-status|agent`; `secrets-load` sets the box user's password from the laptop secret `BOX_SUDO_PASSWORD`. A boot unit drops non-root traffic to `169.254.169.254`, where the cloud-init data (Hetzner token, Tailscale auth key) is readable. Plan 03 then builds agents on this: every Docker call happens inside root `jauntctl`.

**Tech Stack:** Go (cobra), OpenSSH via Tailscale SSH, sudoers, iptables owner match, systemd, POSIX sh.

**Spec:** `docs/superpowers/specs/2026-11-09-jauntbox-single-machine-design.md` §4 (trust zones). This plan narrows the spec's "Known accepted exposure" (passwordless sudo and the `docker` group for the box user); the spec stays the authority elsewhere.

## Global Constraints

- Root on the box is reachable from the laptop (Tailscale SSH as `root`, allowed only for the tailnet owner by the policy snippet) and by the box user only through password `sudo`.
- Passwordless sudo for the box user covers exactly: `/usr/local/bin/jauntctl mint *`, `/usr/local/bin/jauntctl repos`, `/usr/local/bin/jauntctl repos *`, `/usr/local/bin/jauntctl box-status`, `/usr/local/bin/jauntctl box-status *`, `/usr/local/bin/jauntctl agent *`. Nothing else, ever: in particular no `install`, `systemctl`, `secrets-load`, `stop`, `night-set`, `supervise`.
- The box user is not in the `docker` group.
- Non-root processes can't reach `169.254.169.254`.
- The laptop never falls back to the box user's sudo for a privileged step: if root ssh fails, the command fails with the fix (update the tailnet policy, `jauntbox acl-snippet`).
- `BOX_SUDO_PASSWORD` is a laptop secret (Plan 02 store). It travels in the unlock bundle, is applied with `chpasswd` on stdin, and is never written to the vault directory, the volume, logs or events.
- Tests never call real ssh, sudo, iptables or Tailscale; commands are asserted as argv/strings; shell scripts tested with stubs.
- Commits: plain conventional messages, no attribution trailers.

## Rulings

1. **Two ssh identities.** `Deps.Remote` (box user) stays for herdr, `jauntbox ssh`, attach. New `Deps.Root` (`remote.NewSSH("root")`) for privileged steps. Cost if wrong: a single runner with a `--root` flag later.
2. **Password sudo for the human**, set at every unlock from `BOX_SUDO_PASSWORD` (Thomas's choice). Without the secret, the box user has no password and only the `NOPASSWD` commands work; `jauntbox status` and `unlock` say how to set it. Accepted weakness, documented: code running as the box user can tamper with shell config to capture the password when typed.
3. **Old boxes:** a box provisioned before this plan keeps its old sudoers until it is rebuilt (daily). Nothing migrates in place.
4. **`jauntbox ssh --root` is not added**; root ssh exists for jauntbox's own commands. Cost if wrong: one flag.
5. **Metadata guard is host-wide for non-root**, in its own early boot unit, separate from Plan 03's container rules.

## Review Focus

1. The tailnet policy still lacks `root` → `jauntbox up`/`unlock`/`down` fail with a message naming `jauntbox acl-snippet`, and never retry through the box user's sudo (Task 1 test).
2. `BOX_SUDO_PASSWORD` never appears in ssh argv, error text, events or the vault directory, including when `chpasswd` fails (Task 3 test).
3. A box user running `sudo -n jauntctl secrets-load`, `sudo -n jauntctl stop` or `sudo -n install …` is refused by sudo (sudoers content test, Task 2; live in smoke).
4. `sudo jauntctl repos` with a hostile `HOME`/`XDG_CONFIG_HOME` reads nothing from the box user's files (Task 4 test).
5. A laptop secrets bundle with only `BOX_SUDO_PASSWORD` (no App key yet) still unlocks and sets the password (Task 3 test).

---

### Task 0: Metadata guard for non-root

**Files:**
- Create: `internal/cloudinit/units/jauntbox-metadata-guard.service`
- Modify: `internal/cloudinit/render.go`, `internal/cloudinit/user-data.yaml.tmpl` (enable and start it in `runcmd` before `tailscale up`), `internal/cloudinit/render_test.go`

**Interfaces:**
- Unit: `DefaultDependencies=no`, `After=local-fs.target`, `Before=network-pre.target ssh.service tailscaled.service`, `Wants=network-pre.target`, `Type=oneshot`, `RemainAfterExit=yes`, `ExecStart=/bin/sh -c 'iptables -C OUTPUT -d 169.254.169.254/32 -m owner ! --uid-owner 0 -j DROP 2>/dev/null || iptables -I OUTPUT 1 -d 169.254.169.254/32 -m owner ! --uid-owner 0 -j DROP'`, `WantedBy=sysinit.target`.

- [ ] **Step 1: Failing test** `TestRenderInstallsMetadataGuard`: the unit file is written to `/etc/systemd/system/jauntbox-metadata-guard.service` with the exact `ExecStart`, and `runcmd` runs `systemctl enable --now jauntbox-metadata-guard.service` before the `tailscale up` line.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`; `sh internal/cloudinit/units/safety-net_test.sh`.
- [ ] **Step 5: Commit** `feat(jauntbox): only root can reach the metadata service on the box`.

### Task 1: Root ssh from the laptop

**Files:**
- Modify: `internal/client/up.go` (`Deps.Root remote.Runner`), `cmd/jauntbox/main.go` (wire `remote.NewSSH("root")`), `internal/client/push.go` (`installCmd`, copy target), `internal/client/unlock.go`, `internal/client/down.go`, `internal/client/night.go`, `internal/client/status.go`, `internal/mesh/tailscale.go` (`ACLSnippet`), tests beside each

**Interfaces:**
- Produces: `client.ErrRootSSH` wrapping errors from `Deps.Root` with the text `root ssh to <host> failed; your tailnet policy must allow ssh as root to tag:jauntbox-<user>: run \`jauntbox acl-snippet\` and update the policy`.
- `PushBinary`: copy to `root@…:/tmp/jauntbox`, run `installCmd` through `Deps.Root`. `installCmd` drops every `sudo` (it runs as root) and runs the box user's git settings as that user: `runuser -u <user> -- git config --global …` (Plan 02 credential lines, plus Plan 03's four guard settings when that plan lands).
- `PushSecrets`/`EnsureUnlocked`: `jauntctl secrets-load` and `test -e <marker>` through `Deps.Root` (no `sudo`).
- `down`: `jauntctl stop`; `night`: `jauntctl night-set …`; `status`: `jauntctl box-status --json` — all through `Deps.Root`.
- `ACLSnippet`: the `ssh` rule's `users` becomes `["<user>", "root"]`; the comment above it says root is for jauntbox's own commands.

- [ ] **Step 1: Failing tests:** each command's fake `Root` runner receives the exact argv and the fake `Remote` receives none of them; `TestInstallCmdRunsAsRoot` (no `sudo`, git settings via `runuser -u <user> --`); `TestRootSSHFailureNamesACLSnippet` (Review Focus 1: error text, and the fake `Remote` saw no fallback call); `TestACLSnippetAllowsRoot`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): privileged box steps run over root ssh from the laptop`.

### Task 2: Narrow sudo, leave the docker group

**Files:**
- Modify: `provision/dev-setup.sh` (sudoers, docker group), `provision/embed_test.go` or a new `provision/dev-setup_test.sh`

**Interfaces:**
- `/etc/sudoers.d/90-jaunt-<user>` (0440), validated with `visudo -cf` before moving into place (a failing check leaves the previous file and logs a warning):
  ```
  <user> ALL=(ALL:ALL) ALL
  <user> ALL=(root) NOPASSWD: /usr/local/bin/jauntctl mint *, /usr/local/bin/jauntctl repos, /usr/local/bin/jauntctl repos *, /usr/local/bin/jauntctl box-status, /usr/local/bin/jauntctl box-status *, /usr/local/bin/jauntctl agent *
  ```
  (the `NOPASSWD` line comes last: sudo uses the last matching entry).
- Replace `usermod -aG docker "$JAUNT_USER"` with `gpasswd -d "$JAUNT_USER" docker 2>/dev/null || true`.

- [ ] **Step 1: Failing test** (`provision/dev-setup_test.sh`, sourcing the sudoers-writing function with a temp root and a stub `visudo`): exact file content and mode; a failing `visudo` keeps the old file; the script contains no `usermod -aG docker` and no `NOPASSWD:ALL` (grep).
- [ ] **Step 2:** FAIL. **Step 3:** Extract the sudoers part of `dev-setup.sh` into a function `write_sudoers <user> <dir>` and implement. **Step 4:** PASS (`bash provision/dev-setup_test.sh`); `go test ./provision/`.
- [ ] **Step 5: Commit** `feat(jauntbox): box user gets password sudo and leaves the docker group`.

### Task 3: The box sudo password from the laptop

**Files:**
- Modify: `internal/vault/vault.go` (or the `secrets-load` command in `cmd/jauntbox/boxforge.go`), `internal/secrets/store.go` (constant `BoxSudoPassword = "BOX_SUDO_PASSWORD"`), `internal/client/unlock.go`, `internal/client/status.go`, tests beside each

**Interfaces:**
- `secrets-load` (root): when the bundle has `BOX_SUDO_PASSWORD`, run `chpasswd` with stdin `<user>:<password>\n` (user from `boxinfo`), and exclude that entry from the files written to the vault; other entries load as today. A `chpasswd` failure is reported as `setting the box user's sudo password failed (chpasswd exit <n>)` with no password text, and the other secrets still load.
- `noSecretsError` / empty-bundle refusal (Plan 02) count `BOX_SUDO_PASSWORD` as a secret (Review Focus 5).
- `jauntbox unlock` and `status`: when the laptop store has no `BOX_SUDO_PASSWORD`, print `note: sudo on the box has no password yet; set one with \`jauntbox secrets set BOX_SUDO_PASSWORD\` and run \`jauntbox unlock\``.

- [ ] **Step 1: Failing tests:** `TestSecretsLoadSetsPasswordNotStored` (fake `chpasswd` runner gets the exact stdin; the vault dir has no such file); `TestSecretsLoadPasswordFailureHidesValue` (error and events lack the password; other files loaded); `TestBundleWithOnlyPasswordUnlocks`; `TestUnlockNotesMissingPassword`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement (`chpasswd` behind a `func(stdin []byte) error` field for tests). **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `feat(jauntbox): set the box user's sudo password from the laptop`.

### Task 4: `jauntctl` as a sudo target

**Files:**
- Modify: `cmd/jauntbox/main.go`, `cmd/jauntbox/boxforge.go`, `cmd/jauntbox/main_test.go`

**Interfaces:**
- When `SUDO_USER` is set, box commands ignore `HOME`, `XDG_CONFIG_HOME` and any config-path flag (they read only `/etc/jauntbox`, `/run/jauntbox`, `/var/lib/jauntbox`); `jauntctl --config …` under sudo is refused with `--config is not accepted under sudo`.
- `mint <repo>` refuses anything but `owner/name` (`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`) before touching the forge.

- [ ] **Step 1: Failing tests:** `TestBoxCommandsIgnoreUserHomeUnderSudo` (Review Focus 4: `SUDO_USER=x HOME=<temp with a hostile config>` → not read), `TestConfigFlagRefusedUnderSudo`, `TestMintRejectsBadRepoArg` (`../x`, `a/b/c`, empty).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS; `go test ./...`.
- [ ] **Step 5: Commit** `fix(jauntbox): jauntctl treats its sudo caller as untrusted`.

### Task 5: Docs, smoke checks, Plan 03 alignment

**Files:**
- Modify: `README.md`, `docs/jauntbox/SMOKE.md`, `docs/superpowers/plans/2026-11-10-jauntbox-03-agent-containers.md`

- [ ] **Step 1: README:** a "Root and sudo on the box" section: root comes from the laptop for jauntbox's own commands; `sudo` asks for `BOX_SUDO_PASSWORD`; passwordless only for the listed `jauntctl` commands; no `docker` group (`sudo docker …` for your own containers); update the tailnet policy with `jauntbox acl-snippet` (now includes `root`); the accepted weakness from ruling 2.
- [ ] **Step 2: SMOKE "Plan 03a"**, recording each output: policy updated, `jauntbox up` works over root ssh; as the box user `sudo -n true` fails, `sudo -n jauntctl repos` works, `sudo -n jauntctl stop` is refused; `sudo true` asks for the password and accepts `BOX_SUDO_PASSWORD`; `docker ps` fails, `sudo docker ps` works; `curl -m 5 http://169.254.169.254/hetzner/v1/userdata` fails as the box user and works with `sudo`; the supervisor still deletes the box at `down`; after a reboot the metadata rule is back (`sudo iptables -S OUTPUT`).
- [ ] **Step 3: Plan 03 alignment:** in Plan 03, state that 03a has landed: every Docker call runs inside root `jauntctl`; `jauntctl agent attach` re-execs through `sudo -n jauntctl agent attach …` (allowed by the sudoers line) and runs `docker exec` as root with `--user <uid>:<gid>`; the four host git guard settings go into `installCmd` through `runuser -u <user> --`.
- [ ] **Step 4: Commit** `docs(jauntbox): root and sudo on the box`.
