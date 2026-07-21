# jaunt Tofu Devbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a working ephemeral remote-devbox that stands up end-to-end with `tofu apply` on Hetzner and on UpCloud, sharing one provider-neutral cloud-init + idle-self-teardown agent.

**Architecture:** A shared `tofu/modules/devbox` renders the provider-neutral artifacts — a cloud-init user-data document and the idle-watch agent (a pure `decide()` shell function + systemd timer). Thin `tofu/hetzner` and `tofu/upcloud` roots wire provider-specific resources (server behind a `box_on` toggle, persistent volume, default-deny firewall, ephemeral Tailscale key) and inject a provider-specific `self-delete.sh`. Data lives on a persistent volume so the server is disposable.

**Tech Stack:** OpenTofu (`tofu`), Hetzner Cloud provider (`hetznercloud/hcloud`), UpCloud provider (`UpCloudLtd/upcloud`), Tailscale provider (`tailscale/tailscale`), cloud-init, POSIX shell (idle agent), systemd.

## Global Constraints

- **License:** MIT. Repo is public (`WA-Systems-EU/jaunt`).
- **Canonical hook line, verbatim everywhere (README/site/copy):** `an ephemeral remote development environment you pop into and out of`.
- **No secrets in git.** `.gitignore` excludes `*.tfvars` (keep `*.tfvars.example`), `.terraform/`, `*.tfstate*`. Secrets (`HCLOUD_TOKEN`, `UPCLOUD_USERNAME`/`UPCLOUD_PASSWORD`, Tailscale API key) come from env.
- **State:** local state for examples; document "use a remote backend for real use". Do NOT wire a remote backend in this plan.
- **Git commits:** plain messages, NO `Co-Authored-By` trailer. Repo git identity: `Thomas <thomas@wa-systems.eu>` (already configured).
- **Discovery convention:** server carries label `jaunt=<user>`; MagicDNS hostname is `<hostname_prefix>-<user>` (default prefix `devbox`).
- **Idle defaults:** day window `2h`, night window `1h`.
- **Server toggle:** `box_on` bool; `count = var.box_on ? 1 : 0` on server + attachment. Volume is never gated by the toggle.
- **Shared variable surface** both roots MUST expose identically: `box_on`, `user`, `plan`, `volume_size`, `ssh_public_key`, `hostname_prefix`, `public_ipv4`, `idle_day`, `idle_night`, `base_image`, plus one location var (`location` on Hetzner, `zone` on UpCloud).
- **Idle agent is the most-tested unit.** `decide()` is a pure function with exhaustive table tests; it must not read the clock, the process table, or files.

---

### Task 1: Repo scaffold

**Files:**
- Create: `README.md`
- Create: `LICENSE`
- Create: `.gitignore`
- Create: `examples/jaunt.yaml`
- Create: `cmd/.gitkeep`, `internal/.gitkeep` (reserved for the later Go CLI)

**Interfaces:**
- Consumes: nothing.
- Produces: the repo skeleton the rest of the plan writes into.

- [ ] **Step 1: Write `LICENSE` (MIT)**

Standard MIT text, copyright line: `Copyright (c) 2026 WA Systems`.

- [ ] **Step 2: Write `.gitignore`**

```gitignore
# tofu
.terraform/
.terraform.lock.hcl
*.tfstate
*.tfstate.*
crash.log
*.tfvars
!*.tfvars.example
override.tf
override.tf.json

# secrets
*.pem
.env
.env.*

# node (site plan)
node_modules/
site/out/
site/.next/
```

- [ ] **Step 3: Write `examples/jaunt.yaml`** (the per-project config from spec §1 / jaunt-cli-plan §6)

```yaml
plan: cax41                      # override server size for this project
hostname_prefix: devbox          # → MagicDNS name devbox-<user>
repos:
  - git@github.com:acme/app.git
setup:                           # idempotent; runs on every up, fast when the volume is warm
  - mise install
  - docker compose pull
ports: [3000, 5432]              # surfaced for connect --open (later CLI milestone)
idle:
  day:   2h                      # teardown after this much genuine idle
  night: 1h                      # tighter window overnight
```

- [ ] **Step 4: Write `README.md`**

Must contain, in order: an H1 `# jaunt`, the exact tagline `> **an ephemeral remote development environment you pop into and out of**`, a one-paragraph "what it is" (spin a powerful remote dev box up in the morning, delete it at night, work persists on a volume, reachable over a Tailscale MagicDNS hostname), a "30-second try" section pointing at `tofu/hetzner` (`tofu init && tofu apply -var box_on=true`), a "Status" note that the Go CLI is a later milestone and this is the tofu foundation, and a cross-link line: `Built by [WA Systems](https://wa-systems.eu).` Use a `<!-- DOMAIN -->` placeholder where the public site domain will go.

- [ ] **Step 5: Create reserved dirs**

```bash
mkdir -p cmd internal && touch cmd/.gitkeep internal/.gitkeep
```

- [ ] **Step 6: Commit**

```bash
git add README.md LICENSE .gitignore examples/jaunt.yaml cmd/.gitkeep internal/.gitkeep
git commit -m "chore: repo scaffold (README, MIT license, gitignore, example config)"
```

---

### Task 2: Idle `decide()` agent + table tests (TDD heart)

**Files:**
- Create: `tofu/modules/devbox/agent/idle-watch.sh`
- Create: `tofu/modules/devbox/agent/decide_test.sh`

**Interfaces:**
- Produces: shell function `decide` sourced by both the test and the agent's `main`. Signature (positional args, all integers except the verdict):
  `decide <active_sessions> <cpu_pct> <cpu_threshold> <keep_until_epoch> <now_epoch> <idle_seconds> <is_night 0|1> <day_window_secs> <night_window_secs> <grace_secs>` → prints one of `KEEP` / `WARN` / `DELETE` to stdout.
- Produces: `main()` in `idle-watch.sh` that gathers real inputs and calls `decide`, then acts (WARN → notify; DELETE → run `/opt/jaunt/self-delete.sh`). The agent sources itself guarded so the test can source without running `main`.

- [ ] **Step 1: Write the failing test** — `tofu/modules/devbox/agent/decide_test.sh`

```sh
#!/bin/sh
# Pure table tests for decide(). No external deps.
set -eu
DIR=$(dirname "$0")
JAUNT_TEST=1 . "$DIR/idle-watch.sh"   # JAUNT_TEST guard prevents main() from running

fail=0
check() {  # check <description> <expected> <actual>
  if [ "$2" = "$3" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1: expected $2 got $3"
    fail=1
  fi
}

DAY=7200      # 2h
NIGHT=3600    # 1h
GRACE=120     # 2m
CPU_T=15      # busy threshold %

# 1. Active session always KEEPs — even deep in the night, long idle timer, no keep marker.
check "active session vetoes at night" KEEP \
  "$(decide 1 0 $CPU_T 0 1000000 999999 1 $DAY $NIGHT $GRACE)"
# 2. keep_marker in the future KEEPs.
check "future keep marker" KEEP \
  "$(decide 0 0 $CPU_T 2000 1000 999999 0 $DAY $NIGHT $GRACE)"
# 3. CPU above threshold = busy = KEEP (a running build).
check "busy cpu keeps" KEEP \
  "$(decide 0 90 $CPU_T 0 1000 999999 0 $DAY $NIGHT $GRACE)"
# 4. Idle but within day window: KEEP.
check "idle under day window" KEEP \
  "$(decide 0 0 $CPU_T 0 1000 7000 0 $DAY $NIGHT $GRACE)"
# 5. Idle just past day window, within grace: WARN.
check "past day window in grace warns" WARN \
  "$(decide 0 0 $CPU_T 0 1000 7250 0 $DAY $NIGHT $GRACE)"
# 6. Idle past day window + grace: DELETE.
check "past day window and grace deletes" DELETE \
  "$(decide 0 0 $CPU_T 0 1000 7400 0 $DAY $NIGHT $GRACE)"
# 7. Night window is tighter: idle 3700s at night is past 1h window → WARN.
check "night tighter window warns earlier" WARN \
  "$(decide 0 0 $CPU_T 0 1000 3700 1 $DAY $NIGHT $GRACE)"
# 8. Same 3700s idle in the day is under 2h → KEEP.
check "day window lenient for same idle" KEEP \
  "$(decide 0 0 $CPU_T 0 1000 3700 0 $DAY $NIGHT $GRACE)"
# 9. CPU boundary: exactly at threshold counts as busy → KEEP.
check "cpu at threshold is busy" KEEP \
  "$(decide 0 15 $CPU_T 0 1000 999999 0 $DAY $NIGHT $GRACE)"
# 10. Expired keep marker does not save an idle box.
check "expired keep marker deletes" DELETE \
  "$(decide 0 0 $CPU_T 500 1000 7400 0 $DAY $NIGHT $GRACE)"

exit $fail
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `sh tofu/modules/devbox/agent/decide_test.sh`
Expected: FAIL — `idle-watch.sh` does not exist yet (`No such file`).

- [ ] **Step 3: Write minimal `idle-watch.sh` with `decide()` and a guarded `main`**

```sh
#!/bin/sh
# jaunt idle-watch agent. Provider-neutral.
# decide() is pure: no clock, no /proc, no files — all inputs are arguments.

decide() {
  as=$1; cpu=$2; cpu_t=$3; keep=$4; now=$5; idle=$6; night=$7; day_w=$8; night_w=$9
  shift 9; grace=$1

  [ "$as" -gt 0 ] && { echo KEEP; return; }          # rule 1: active session vetoes, always
  [ "$keep" -gt "$now" ] && { echo KEEP; return; }   # rule 2: keep marker in the future
  [ "$cpu" -ge "$cpu_t" ] && { echo KEEP; return; }  # busy (running build) is not idle

  if [ "$night" -eq 1 ]; then w=$night_w; else w=$day_w; fi
  if [ "$idle" -le "$w" ]; then echo KEEP
  elif [ "$idle" -le $((w + grace)) ]; then echo WARN
  else echo DELETE
  fi
}

# --- real-input gathering + action (skipped under test) ---
main() {
  now=$(date +%s)
  hour=$(date +%H)
  if [ "$hour" -ge 22 ] || [ "$hour" -lt 7 ]; then is_night=1; else is_night=0; fi

  active=$(who | wc -l)
  tmux ls >/dev/null 2>&1 && active=$((active + 1))
  cpu=$(awk '{printf "%d", $1*100/'"$(nproc)"'}' /proc/loadavg)   # rough % of one core-equiv

  keep_file=/var/lib/jaunt/keep-until
  keep=0; [ -f "$keep_file" ] && keep=$(cat "$keep_file")

  act_file=/var/lib/jaunt/last-active
  [ -f "$act_file" ] || echo "$now" > "$act_file"
  last=$(cat "$act_file")
  if [ "$active" -gt 0 ] || [ "$cpu" -ge "${JAUNT_CPU_THRESHOLD:-15}" ]; then
    echo "$now" > "$act_file"; last=$now
  fi
  idle=$((now - last))

  verdict=$(decide "$active" "$cpu" "${JAUNT_CPU_THRESHOLD:-15}" "$keep" "$now" \
                   "$idle" "$is_night" "${JAUNT_IDLE_DAY:-7200}" \
                   "${JAUNT_IDLE_NIGHT:-3600}" "${JAUNT_GRACE:-120}")
  case "$verdict" in
    WARN)   logger -t jaunt "idle warning: box will self-delete soon"; \
            [ -x /opt/jaunt/notify.sh ] && /opt/jaunt/notify.sh "jaunt: idle, deleting soon" ;;
    DELETE) logger -t jaunt "idle: self-deleting"; \
            [ -x /opt/jaunt/self-delete.sh ] && /opt/jaunt/self-delete.sh ;;
    *)      : ;;
  esac
}

[ "${JAUNT_TEST:-0}" = "1" ] || main "$@"
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `sh tofu/modules/devbox/agent/decide_test.sh`
Expected: all `ok` lines, exit 0.

- [ ] **Step 5: Make the test executable and commit**

```bash
chmod +x tofu/modules/devbox/agent/idle-watch.sh tofu/modules/devbox/agent/decide_test.sh
git add tofu/modules/devbox/agent/
git commit -m "feat(agent): pure idle decide() with exhaustive table tests"
```

---

### Task 3: systemd units + shared cloud-init template + module wiring

**Files:**
- Create: `tofu/modules/devbox/agent/jaunt-idle.service`
- Create: `tofu/modules/devbox/agent/jaunt-idle.timer`
- Create: `tofu/modules/devbox/cloud-init/user-data.tftpl`
- Create: `tofu/modules/devbox/variables.tf`
- Create: `tofu/modules/devbox/main.tf`
- Create: `tofu/modules/devbox/outputs.tf`

**Interfaces:**
- Consumes: `agent/idle-watch.sh`, `agent/decide_test.sh` (Task 2).
- Produces: module output `user_data` (string). Module input variables: `user`, `hostname_prefix`, `tailscale_tag`, `tailscale_auth_key` (sensitive), `idle_day_secs`, `idle_night_secs`, `cpu_threshold`, `setup_steps` (list(string)), `self_delete_script` (string — provider-specific body of `/opt/jaunt/self-delete.sh`), `volume_device` (string, e.g. `/dev/sdb` or by-id path), `tailscale_extra_args` (string, default "").

- [ ] **Step 1: Write `jaunt-idle.service`**

```ini
[Unit]
Description=jaunt idle-watch (self-teardown when idle)

[Service]
Type=oneshot
EnvironmentFile=-/etc/jaunt/agent.env
ExecStart=/opt/jaunt/idle-watch.sh
```

- [ ] **Step 2: Write `jaunt-idle.timer`**

```ini
[Unit]
Description=Run jaunt idle-watch every 2 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=2min
AccuracySec=30s

[Install]
WantedBy=timers.target
```

- [ ] **Step 3: Write `cloud-init/user-data.tftpl`** (a cloud-config document; templatefile vars in `${...}`)

```yaml
#cloud-config
write_files:
  - path: /opt/jaunt/idle-watch.sh
    permissions: '0755'
    encoding: b64
    content: ${idle_watch_b64}
  - path: /opt/jaunt/self-delete.sh
    permissions: '0700'
    encoding: b64
    content: ${self_delete_b64}
  - path: /etc/jaunt/agent.env
    permissions: '0600'
    content: |
      JAUNT_IDLE_DAY=${idle_day_secs}
      JAUNT_IDLE_NIGHT=${idle_night_secs}
      JAUNT_CPU_THRESHOLD=${cpu_threshold}
      JAUNT_GRACE=120
  - path: /etc/systemd/system/jaunt-idle.service
    content: ${service_b64}
    encoding: b64
  - path: /etc/systemd/system/jaunt-idle.timer
    content: ${timer_b64}
    encoding: b64
  - path: /opt/jaunt/setup.sh
    permissions: '0755'
    content: |
      #!/bin/sh
      set -eu
      %{ for step in setup_steps ~}
      ${step}
      %{ endfor ~}
runcmd:
  - mkdir -p /var/lib/jaunt
  # mount the persistent volume at /home (format only if it has no filesystem yet)
  - if ! blkid ${volume_device}; then mkfs.ext4 -F ${volume_device}; fi
  - mkdir -p /mnt/jaunt-home && mount ${volume_device} /mnt/jaunt-home
  - if [ ! -d /mnt/jaunt-home/home-seeded ]; then cp -a /home/. /mnt/jaunt-home/ 2>/dev/null || true; mkdir -p /mnt/jaunt-home/home-seeded; fi
  - umount /mnt/jaunt-home && mount ${volume_device} /home
  - grep -q '${volume_device} /home' /etc/fstab || echo '${volume_device} /home ext4 defaults,nofail 0 2' >> /etc/fstab
  # tailscale
  - curl -fsSL https://tailscale.com/install.sh | sh
  - tailscale up --auth-key=${tailscale_auth_key} --hostname=${hostname_prefix}-${user} --advertise-tags=${tailscale_tag} --ssh ${tailscale_extra_args}
  # idempotent project setup
  - /opt/jaunt/setup.sh || true
  # arm idle-watch
  - systemctl daemon-reload
  - systemctl enable --now jaunt-idle.timer
```

- [ ] **Step 4: Write `variables.tf`**

```hcl
variable "user"               { type = string }
variable "hostname_prefix"    { type = string  default = "devbox" }
variable "tailscale_tag"      { type = string  default = "tag:devbox" }
variable "tailscale_auth_key" { type = string  sensitive = true }
variable "tailscale_extra_args" { type = string default = "--ephemeral" }
variable "idle_day_secs"      { type = number  default = 7200 }
variable "idle_night_secs"    { type = number  default = 3600 }
variable "cpu_threshold"      { type = number  default = 15 }
variable "setup_steps"        { type = list(string) default = [] }
variable "self_delete_script" { type = string  sensitive = true }
variable "volume_device"      { type = string }
```

- [ ] **Step 5: Write `main.tf` (render user_data) and `outputs.tf`**

`main.tf`:
```hcl
locals {
  user_data = templatefile("${path.module}/cloud-init/user-data.tftpl", {
    idle_watch_b64    = base64encode(file("${path.module}/agent/idle-watch.sh"))
    self_delete_b64   = base64encode(var.self_delete_script)
    service_b64       = base64encode(file("${path.module}/agent/jaunt-idle.service"))
    timer_b64         = base64encode(file("${path.module}/agent/jaunt-idle.timer"))
    idle_day_secs     = var.idle_day_secs
    idle_night_secs   = var.idle_night_secs
    cpu_threshold     = var.cpu_threshold
    setup_steps       = var.setup_steps
    volume_device     = var.volume_device
    tailscale_auth_key = var.tailscale_auth_key
    tailscale_tag      = var.tailscale_tag
    tailscale_extra_args = var.tailscale_extra_args
    hostname_prefix    = var.hostname_prefix
    user               = var.user
  })
}
```

`outputs.tf`:
```hcl
output "user_data" {
  value     = local.user_data
  sensitive = true
}
```

- [ ] **Step 6: Verify the module renders** (no provider needed — pure templatefile)

Run:
```bash
cd tofu/modules/devbox && tofu fmt && cat > /tmp/probe.tf <<'EOF'
module "d" {
  source = "./"
  user = "alice"
  tailscale_auth_key = "tskey-test"
  self_delete_script = "#!/bin/sh\necho delete"
  volume_device = "/dev/sdb"
}
output "ud" { value = module.d.user_data  sensitive = true }
EOF
mkdir -p /tmp/probe && cp -r . /tmp/probe/mod && cp /tmp/probe.tf /tmp/probe/main.tf
sed -i 's#source = "./"#source = "./mod"#' /tmp/probe/main.tf
cd /tmp/probe && tofu init -backend=false >/dev/null && tofu validate
```
Expected: `Success! The configuration is valid.` Then clean up `/tmp/probe`.

- [ ] **Step 7: Commit**

```bash
git add tofu/modules/devbox/
git commit -m "feat(module): shared cloud-init + systemd units + user_data rendering"
```

---

### Task 4: Hetzner root (`tofu/hetzner`)

**Files:**
- Create: `tofu/hetzner/providers.tf`
- Create: `tofu/hetzner/variables.tf`
- Create: `tofu/hetzner/main.tf`
- Create: `tofu/hetzner/outputs.tf`
- Create: `tofu/hetzner/self-delete.sh.tftpl`
- Create: `tofu/hetzner/terraform.tfvars.example`

**Interfaces:**
- Consumes: `../modules/devbox` (Task 3) via `module.devbox.user_data`.
- Produces: a `tofu apply`-able root exposing the shared variable surface (Global Constraints). Outputs: `hostname`, `magicdns`, `volume_id`.

- [ ] **Step 1: Write `providers.tf`**

```hcl
terraform {
  required_version = ">= 1.6"
  required_providers {
    hcloud    = { source = "hetznercloud/hcloud", version = "~> 1.48" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}
provider "hcloud"    { token = var.hcloud_token }
provider "tailscale" {}   # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
```

- [ ] **Step 2: Write `variables.tf`** (shared surface + Hetzner specifics)

```hcl
variable "hcloud_token"   { type = string  sensitive = true }
variable "box_on"         { type = bool     default = false }
variable "user"           { type = string }
variable "plan"           { type = string   default = "cax41" }
variable "volume_size"    { type = number   default = 100 }
variable "ssh_public_key" { type = string }
variable "hostname_prefix"{ type = string   default = "devbox" }
variable "location"       { type = string   default = "fsn1" }
variable "public_ipv4"    { type = bool      default = false }
variable "idle_day"       { type = number    default = 7200 }
variable "idle_night"     { type = number    default = 3600 }
variable "base_image"     { type = string    default = "ubuntu-24.04" }
variable "setup_steps"    { type = list(string) default = [] }
```

- [ ] **Step 3: Write `self-delete.sh.tftpl`** (Hetzner self-delete via metadata + hcloud API)

```sh
#!/bin/sh
# Deletes THIS Hetzner server via the hcloud API. Token is devboxes-project-scoped.
set -eu
ID=$(curl -fsSL http://169.254.169.254/hetzner/v1/metadata/instance-id)
curl -fsSL -X DELETE -H "Authorization: Bearer ${hcloud_token}" \
  "https://api.hetzner.cloud/v1/servers/$ID"
```

- [ ] **Step 4: Write `main.tf`**

```hcl
resource "hcloud_ssh_key" "dev" {
  name       = "jaunt-${var.user}"
  public_key = var.ssh_public_key
}

resource "hcloud_volume" "data" {
  name     = "jaunt-${var.user}-data"
  size     = var.volume_size
  location = var.location
  format   = "ext4"
}

resource "tailscale_tailnet_key" "ephemeral" {
  ephemeral     = true
  reusable      = true
  preauthorized = true
  tags          = ["tag:devbox"]
}

module "devbox" {
  source             = "../modules/devbox"
  user               = var.user
  hostname_prefix    = var.hostname_prefix
  tailscale_auth_key = tailscale_tailnet_key.ephemeral.key
  idle_day_secs      = var.idle_day
  idle_night_secs    = var.idle_night
  setup_steps        = var.setup_steps
  volume_device      = "/dev/disk/by-id/scsi-0HC_Volume_${hcloud_volume.data.id}"
  self_delete_script = templatefile("${path.module}/self-delete.sh.tftpl", {
    hcloud_token = var.hcloud_token
  })
}

resource "hcloud_firewall" "devbox" {
  name = "jaunt-${var.user}"
  # default-deny inbound: NO port 22 (Tailscale SSH only). Allow all egress.
  rule { direction = "out" protocol = "tcp" port = "any" destination_ips = ["0.0.0.0/0", "::/0"] }
  rule { direction = "out" protocol = "udp" port = "any" destination_ips = ["0.0.0.0/0", "::/0"] }
  apply_to { label_selector = "jaunt=${var.user}" }
}

resource "hcloud_server" "devbox" {
  count       = var.box_on ? 1 : 0
  name        = "${var.hostname_prefix}-${var.user}"
  server_type = var.plan
  image       = var.base_image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.dev.id]
  user_data   = module.devbox.user_data
  labels      = { jaunt = var.user }
  public_net {
    ipv4_enabled = var.public_ipv4
    ipv6_enabled = true
  }
}

resource "hcloud_volume_attachment" "data" {
  count     = var.box_on ? 1 : 0
  volume_id = hcloud_volume.data.id
  server_id = hcloud_server.devbox[0].id
  automount = false   # cloud-init mounts at /home explicitly
}
```

- [ ] **Step 5: Write `outputs.tf`**

```hcl
output "hostname" { value = "${var.hostname_prefix}-${var.user}" }
output "magicdns" { value = "${var.hostname_prefix}-${var.user} (MagicDNS; reachable on your tailnet)" }
output "volume_id" { value = hcloud_volume.data.id }
```

- [ ] **Step 6: Write `terraform.tfvars.example`**

```hcl
# Copy to terraform.tfvars and fill in. NEVER commit terraform.tfvars.
# hcloud_token also readable from HCLOUD_TOKEN env; tailscale from TAILSCALE_API_KEY/TAILSCALE_TAILNET.
hcloud_token   = "REDACTED-devboxes-project-token"
user           = "alice"
ssh_public_key = "ssh-ed25519 AAAA... alice@laptop"
# box_on         = true   # flip to stand the box up
# plan           = "ccx43"
# public_ipv4    = true
```

- [ ] **Step 7: fmt + validate**

Run:
```bash
cd tofu/hetzner && tofu fmt && tofu init -backend=false && tofu validate
```
Expected: `Success! The configuration is valid.`

- [ ] **Step 8: Commit**

```bash
git add tofu/hetzner/
git commit -m "feat(hetzner): toggle-able devbox root (volume, firewall, tailscale, self-delete)"
```

---

### Task 5: UpCloud root (`tofu/upcloud`)

**Files:**
- Create: `tofu/upcloud/providers.tf`
- Create: `tofu/upcloud/variables.tf`
- Create: `tofu/upcloud/main.tf`
- Create: `tofu/upcloud/outputs.tf`
- Create: `tofu/upcloud/self-delete.sh.tftpl`
- Create: `tofu/upcloud/terraform.tfvars.example`

**Interfaces:**
- Consumes: `../modules/devbox` (Task 3).
- Produces: a `tofu apply`-able root mirroring the Hetzner variable surface, with `zone` in place of `location`. Outputs: `hostname`, `magicdns`, `volume_id`.

- [ ] **Step 1: Write `providers.tf`**

```hcl
terraform {
  required_version = ">= 1.6"
  required_providers {
    upcloud   = { source = "UpCloudLtd/upcloud", version = "~> 5.0" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}
provider "upcloud"   {}   # reads UPCLOUD_USERNAME / UPCLOUD_PASSWORD from env
provider "tailscale" {}
```

- [ ] **Step 2: Write `variables.tf`** (mirror Hetzner; `zone` replaces `location`; no token var — env creds)

```hcl
variable "box_on"         { type = bool     default = false }
variable "user"           { type = string }
variable "plan"           { type = string   default = "2xCPU-4GB" }
variable "volume_size"    { type = number   default = 100 }
variable "ssh_public_key" { type = string }
variable "hostname_prefix"{ type = string   default = "devbox" }
variable "zone"           { type = string   default = "fi-hel2" }
variable "public_ipv4"    { type = bool      default = true }
variable "idle_day"       { type = number    default = 7200 }
variable "idle_night"     { type = number    default = 3600 }
variable "base_image"     { type = string    default = "Ubuntu Server 24.04 LTS (Noble Numbat)" }
variable "setup_steps"    { type = list(string) default = [] }
variable "upcloud_username" { type = string sensitive = true }
variable "upcloud_password" { type = string sensitive = true }
```

- [ ] **Step 3: Write `self-delete.sh.tftpl`** (UpCloud self-delete via metadata + API)

```sh
#!/bin/sh
# Deletes THIS UpCloud server via the UpCloud API.
set -eu
UUID=$(curl -fsSL http://169.254.169.254/metadata/v1/instance/uuid)
curl -fsSL -X DELETE -u "${upcloud_username}:${upcloud_password}" \
  "https://api.upcloud.com/1.3/server/$UUID?storages=0"   # storages=0: keep the persistent volume
```

- [ ] **Step 4: Write `main.tf`**

```hcl
resource "upcloud_storage" "data" {
  title = "jaunt-${var.user}-data"
  size  = var.volume_size
  zone  = var.zone
}

resource "tailscale_tailnet_key" "ephemeral" {
  ephemeral     = true
  reusable      = true
  preauthorized = true
  tags          = ["tag:devbox"]
}

module "devbox" {
  source             = "../modules/devbox"
  user               = var.user
  hostname_prefix    = var.hostname_prefix
  tailscale_auth_key = tailscale_tailnet_key.ephemeral.key
  idle_day_secs      = var.idle_day
  idle_night_secs    = var.idle_night
  setup_steps        = var.setup_steps
  volume_device      = "/dev/vdb"   # second attached disk on UpCloud KVM
  self_delete_script = templatefile("${path.module}/self-delete.sh.tftpl", {
    upcloud_username = var.upcloud_username
    upcloud_password = var.upcloud_password
  })
}

resource "upcloud_server" "devbox" {
  count    = var.box_on ? 1 : 0
  hostname = "${var.hostname_prefix}-${var.user}"
  zone     = var.zone
  plan     = var.plan
  metadata = true
  labels   = { jaunt = var.user }

  login {
    keys = [var.ssh_public_key]
  }
  template {
    storage = var.base_image
    size    = 25
  }
  storage_devices {
    storage = upcloud_storage.data.id
    address = "virtio"
  }
  network_interface {
    type = "public"
  }
  user_data = module.devbox.user_data
}
```

- [ ] **Step 5: Write `outputs.tf`**

```hcl
output "hostname" { value = "${var.hostname_prefix}-${var.user}" }
output "magicdns" { value = "${var.hostname_prefix}-${var.user} (MagicDNS; reachable on your tailnet)" }
output "volume_id" { value = upcloud_storage.data.id }
```

- [ ] **Step 6: Write `terraform.tfvars.example`**

```hcl
# Copy to terraform.tfvars and fill in. NEVER commit terraform.tfvars.
# Credentials also readable from UPCLOUD_USERNAME / UPCLOUD_PASSWORD env.
upcloud_username = "REDACTED"
upcloud_password = "REDACTED"
user             = "alice"
ssh_public_key   = "ssh-ed25519 AAAA... alice@laptop"
# box_on           = true
# plan             = "4xCPU-8GB"
# zone             = "de-fra1"
```

- [ ] **Step 7: fmt + validate**

Run:
```bash
cd tofu/upcloud && tofu fmt && tofu init -backend=false && tofu validate
```
Expected: `Success! The configuration is valid.`

- [ ] **Step 8: Commit**

```bash
git add tofu/upcloud/
git commit -m "feat(upcloud): devbox root mirroring hetzner variable surface"
```

---

### Task 6: CI + top-level tofu docs

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `tofu/README.md`

**Interfaces:**
- Consumes: everything above.
- Produces: CI that runs the `decide()` table tests and `tofu fmt -check` + `validate` on both roots.

- [ ] **Step 1: Write `.github/workflows/ci.yml`**

```yaml
name: ci
on: [push, pull_request]
jobs:
  agent-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: idle decide() table tests
        run: sh tofu/modules/devbox/agent/decide_test.sh
  tofu:
    runs-on: ubuntu-latest
    strategy:
      matrix: { root: [hetzner, upcloud] }
    steps:
      - uses: actions/checkout@v4
      - uses: opentofu/setup-opentofu@v1
      - name: fmt check
        run: tofu -chdir=tofu/${{ matrix.root }} fmt -check -recursive
      - name: validate
        run: |
          tofu -chdir=tofu/${{ matrix.root }} init -backend=false
          tofu -chdir=tofu/${{ matrix.root }} validate
```

- [ ] **Step 2: Write `tofu/README.md`**

Document: the shared-module + two-roots layout; the shared variable surface table; `HCLOUD_TOKEN` / `UPCLOUD_USERNAME`+`UPCLOUD_PASSWORD` / `TAILSCALE_API_KEY`+`TAILSCALE_TAILNET` env vars; the `tofu init && tofu apply -var box_on=true` / `-var box_on=false` lifecycle; the "local state now, use a remote backend for real" note; and the "delete, don't stop — a stopped server still bills" rule.

- [ ] **Step 3: Verify CI locally (the parts that don't need cloud creds)**

Run:
```bash
sh tofu/modules/devbox/agent/decide_test.sh
tofu -chdir=tofu/hetzner fmt -check -recursive && tofu -chdir=tofu/upcloud fmt -check -recursive
```
Expected: tests pass; fmt reports no diffs.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml tofu/README.md
git commit -m "ci: agent tests + tofu fmt/validate for both roots; tofu docs"
```

---

### Task 7 (opt-in, manual): live smoke test

**Not run in CI.** Documents the one real create/destroy check, guarded by having real creds. This task adds a doc, not automation.

**Files:**
- Create: `tofu/SMOKE.md`

- [ ] **Step 1: Write `tofu/SMOKE.md`**

Document the manual smoke procedure for each provider:
1. Export creds (`HCLOUD_TOKEN` or `UPCLOUD_USERNAME`/`UPCLOUD_PASSWORD`; `TAILSCALE_API_KEY`/`TAILSCALE_TAILNET`).
2. `cp terraform.tfvars.example terraform.tfvars`; set `user`, `ssh_public_key`.
3. `tofu init && tofu apply -var box_on=true`.
4. Wait for the box to appear on the tailnet; `ssh devbox-<user>` (Tailscale SSH); confirm `/home` is the mounted volume (`df -h /home`) and `systemctl status jaunt-idle.timer` is active.
5. Create a marker file in `/home`; `tofu apply -var box_on=false` (deletes server, keeps volume); `tofu apply -var box_on=true` again; confirm the marker file survived.
6. `tofu destroy` to remove the volume when done.

Note the expected costs (CAX41 ≈ €0.045/hr; volume €5.72/mo) and the warm-`up` timing target (< ~2 min) to validate against jaunt-cli-plan §12.

- [ ] **Step 2: Commit**

```bash
git add tofu/SMOKE.md
git commit -m "docs: manual live smoke-test procedure for both providers"
```

---

## Self-Review notes (author)

- **Spec coverage:** §2 layout → Tasks 1,3,4,5; §3 shared module (cloud-init + idle decide) → Tasks 2,3; §4 Hetzner → Task 4; §5 UpCloud → Task 5; §6 secrets/state → Task 1 `.gitignore` + `.tfvars.example` + `tofu/README.md`; §9 build order → task order; §10 testing → Tasks 2 (decide tests), 6 (CI validate), 7 (opt-in smoke). Site (§7) and Publish (§8) are the **separate site plan** — intentionally not here.
- **Deferred, per spec:** remote state backend, Packer base image, the Go CLI (`cmd/`/`internal/` reserved in Task 1).
- **Type consistency:** module input names in Task 3 (`user`, `tailscale_auth_key`, `self_delete_script`, `volume_device`, `idle_day_secs`, `idle_night_secs`, `setup_steps`) are used identically in Tasks 4 & 5. `decide()` arg order in Task 2's test matches the function definition and the `main()` call site.
- **Open validation risk to confirm during execution:** exact provider attribute names (`hcloud` volume by-id device path; UpCloud `storage_devices`/`network_interface`/`metadata` block shapes; `tailscale_tailnet_key` argument names) should be checked against the installed provider versions during `tofu validate` — fix inline if a provider renamed an attribute.
