# jaunt control plane — Plan 01: tofu root split (infra + box)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the single `tofu/hetzner` root into an admin-owned `infra` root (durable
resources) and a `jauntd`-owned `box` root (server only), so the daily up/down path is a
one-variable apply that *cannot* touch durable infra — the foundation of the control plane.

**Architecture:** `infra` holds the volume, Tailscale key, firewall, SSH key, and computes
`user_data` (via the `jauntbox` module); it exports what the server needs as outputs. `box`
contains *only* `hcloud_server` (gated on `box_on`) + the volume attachment, reading `infra`'s
outputs via a `terraform_remote_state` data source. Both use local state (jauntd is the sole
applier). The live box's data volume is preserved across the migration.

**Tech Stack:** OpenTofu (`hcloud` ~> 1.48, `tailscale` ~> 0.16), local state, the existing
`tofu/modules/jauntbox` module (unchanged).

## Global Constraints

- OpenTofu `>= 1.6`. Providers: `hetznercloud/hcloud ~> 1.48`, `tailscale/tailscale ~> 0.16`.
- `hcloud` provider auth from `var.hcloud_token`; `tailscale` from `TAILSCALE_API_KEY` /
  `TAILSCALE_TAILNET` env (identical to `tofu/hetzner/providers.tf`).
- Per-user naming stays `jaunt-<user>` / `jauntbox-<user>`; tag stays `tag:jaunt-<user>`.
- Single engineer (`user = "tsultrim"`) for this plan; per-engineer state-key parameterization
  is deferred to a later plan.
- **The data volume (`/home`) must be preserved** through the migration — it is the only
  stateful resource. Everything else may be recreated.
- The `tofu/modules/jauntbox` module is NOT modified in this plan.
- Do not commit `*.tfvars` or `*.tfstate` (already gitignored). Commit only `*.tf`.

---

### Task 1: Scaffold the `infra` root

**Files:**
- Create: `tofu/infra/providers.tf`
- Create: `tofu/infra/variables.tf`
- Create: `tofu/infra/main.tf`
- Create: `tofu/infra/outputs.tf`

**Interfaces:**
- Produces (outputs consumed by the `box` root in Task 2): `user_data` (string, sensitive),
  `volume_id` (string), `ssh_key_id` (string), `server_type` (string), `image` (string),
  `location` (string), `public_ipv4` (bool), `user` (string), `hostname_prefix` (string).

- [ ] **Step 1: Write `tofu/infra/providers.tf`** (copied verbatim from `tofu/hetzner/providers.tf`)

```hcl
terraform {
  required_version = ">= 1.6"
  required_providers {
    hcloud    = { source = "hetznercloud/hcloud", version = "~> 1.48" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}

provider "hcloud" { token = var.hcloud_token }
provider "tailscale" {} # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
```

- [ ] **Step 2: Write `tofu/infra/variables.tf`** (the durable/declared inputs — the admin-owned knobs)

```hcl
variable "hcloud_token" {
  type      = string
  sensitive = true
}
variable "user" { type = string }
variable "ssh_public_key" { type = string }
variable "hostname_prefix" {
  type    = string
  default = "jauntbox"
}
variable "plan" {
  type    = string
  default = "cpx41" # 8 vCPU / 16 GB, x86 — sized for the df stack
}
variable "volume_size" {
  type    = number
  default = 100
}
variable "location" {
  type    = string
  default = "fsn1"
}
variable "base_image" {
  type    = string
  default = "ubuntu-26.04"
}
variable "public_ipv4" {
  type    = bool
  default = true # github.com et al. have no IPv6 — egress needs IPv4
}
variable "allow_ssh" {
  type    = bool
  default = false
}
variable "idle_day" {
  type    = number
  default = 7200
}
variable "idle_night" {
  type    = number
  default = 3600
}
variable "setup_steps" {
  type    = list(string)
  default = []
}
```

- [ ] **Step 3: Write `tofu/infra/main.tf`** (the durable resources + `user_data`, lifted from the current `tofu/hetzner/main.tf` minus the server & attachment)

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
  tags          = ["tag:jaunt-${var.user}"]
}

module "jauntbox" {
  source             = "../modules/jauntbox"
  user               = var.user
  hostname_prefix    = var.hostname_prefix
  tailscale_tag      = "tag:jaunt-${var.user}"
  tailscale_auth_key = tailscale_tailnet_key.ephemeral.key
  idle_day_secs      = var.idle_day
  idle_night_secs    = var.idle_night
  setup_steps        = var.setup_steps
  volume_device      = "/dev/disk/by-id/scsi-0HC_Volume_${hcloud_volume.data.id}"
  self_delete_script = templatefile("${path.module}/self-delete.sh.tftpl", {
    hcloud_token = var.hcloud_token
  })
}

resource "hcloud_firewall" "jauntbox" {
  name = "jaunt-${var.user}"
  rule {
    direction       = "out"
    protocol        = "tcp"
    port            = "any"
    destination_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction       = "out"
    protocol        = "udp"
    port            = "any"
    destination_ips = ["0.0.0.0/0", "::/0"]
  }
  dynamic "rule" {
    for_each = var.allow_ssh ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = ["0.0.0.0/0", "::/0"]
    }
  }
  apply_to { label_selector = "jaunt=${var.user}" }
}
```

- [ ] **Step 4: Copy the self-delete template into the `infra` root**

Run: `cp tofu/hetzner/self-delete.sh.tftpl tofu/infra/self-delete.sh.tftpl`
Expected: file exists at `tofu/infra/self-delete.sh.tftpl` (the module reads it via `path.module` of the *root*, matching the current layout).

- [ ] **Step 5: Write `tofu/infra/outputs.tf`** (exactly the surface the `box` root needs)

```hcl
output "user_data" {
  value     = module.jauntbox.user_data
  sensitive = true
}
output "volume_id"       { value = hcloud_volume.data.id }
output "ssh_key_id"      { value = hcloud_ssh_key.dev.id }
output "server_type"     { value = var.plan }
output "image"           { value = var.base_image }
output "location"        { value = var.location }
output "public_ipv4"     { value = var.public_ipv4 }
output "user"            { value = var.user }
output "hostname_prefix" { value = var.hostname_prefix }
```

- [ ] **Step 6: Init + validate**

Run: `tofu -chdir=tofu/infra init && tofu -chdir=tofu/infra validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 7: Commit**

```bash
git add tofu/infra/providers.tf tofu/infra/variables.tf tofu/infra/main.tf \
        tofu/infra/outputs.tf tofu/infra/self-delete.sh.tftpl
git commit -m "feat(tofu): infra root (durable resources + user_data outputs)"
```

---

### Task 2: Scaffold the `box` root

**Files:**
- Create: `tofu/box/providers.tf`
- Create: `tofu/box/variables.tf`
- Create: `tofu/box/main.tf`

**Interfaces:**
- Consumes (from Task 1's `infra` outputs via `terraform_remote_state`): `user_data`,
  `volume_id`, `ssh_key_id`, `server_type`, `image`, `location`, `public_ipv4`, `user`,
  `hostname_prefix`.
- Produces: a running `hcloud_server.jauntbox` when `box_on = true`; nothing when `false`.

- [ ] **Step 1: Write `tofu/box/providers.tf`** (only hcloud is needed here)

```hcl
terraform {
  required_version = ">= 1.6"
  required_providers {
    hcloud = { source = "hetznercloud/hcloud", version = "~> 1.48" }
  }
}

provider "hcloud" { token = var.hcloud_token }
```

- [ ] **Step 2: Write `tofu/box/variables.tf`**

```hcl
variable "hcloud_token" {
  type      = string
  sensitive = true
}
variable "box_on" {
  type    = bool
  default = false
}
# Path to the infra root's local state, read read-only for the server's inputs.
# Single-engineer for now; a later plan parameterizes this per user.
variable "infra_state_path" {
  type    = string
  default = "../infra/terraform.tfstate"
}
```

- [ ] **Step 3: Write `tofu/box/main.tf`** (server + attachment only, reading infra outputs)

```hcl
data "terraform_remote_state" "infra" {
  backend = "local"
  config  = { path = var.infra_state_path }
}

locals { infra = data.terraform_remote_state.infra.outputs }

resource "hcloud_server" "jauntbox" {
  count       = var.box_on ? 1 : 0
  name        = "${local.infra.hostname_prefix}-${local.infra.user}"
  server_type = local.infra.server_type
  image       = local.infra.image
  location    = local.infra.location
  ssh_keys    = [local.infra.ssh_key_id]
  user_data   = local.infra.user_data
  labels      = { jaunt = local.infra.user }
  public_net {
    ipv4_enabled = local.infra.public_ipv4
    ipv6_enabled = true
  }
}

resource "hcloud_volume_attachment" "data" {
  count     = var.box_on ? 1 : 0
  volume_id = local.infra.volume_id
  server_id = hcloud_server.jauntbox[0].id
  automount = false # cloud-init mounts /home explicitly
}
```

- [ ] **Step 4: Init + validate**

Run: `tofu -chdir=tofu/box init && tofu -chdir=tofu/box validate`
Expected: `Success! The configuration is valid.` (validate does not need the infra state to exist yet.)

- [ ] **Step 5: Commit**

```bash
git add tofu/box/providers.tf tofu/box/variables.tf tofu/box/main.tf
git commit -m "feat(tofu): box root (server-only, reads infra via remote_state)"
```

---

### Task 3: Migrate the live deployment, preserving the data volume

The current box runs from `tofu/hetzner` state. The **only** thing that must survive is the
data volume (`/home`). Strategy: drop the volume from the old state so it isn't destroyed,
tear the rest down, then adopt the volume into `infra` and stand the box back up via `box`.

**Files:**
- Uses (read/write state): `tofu/hetzner/`, `tofu/infra/`, `tofu/box/`. No `.tf` edits.

- [ ] **Step 1: Record the volume ID**

Run: `tofu -chdir=tofu/hetzner state show hcloud_volume.data | grep -E '^\s+id\b'`
Expected: a line like `    id = "123456789"`. **Record this value** as `<VOLUME_ID>`.

- [ ] **Step 2: Drop the volume from the old state so `destroy` cannot touch it**

Run: `tofu -chdir=tofu/hetzner state rm hcloud_volume.data`
Expected: `Removed hcloud_volume.data` / `Successfully removed 1 resource instance(s).`
Verify the real volume still exists: `hcloud volume list | grep jaunt-tsultrim-data` (or the
Hetzner console) → present.

- [ ] **Step 3: Tear down the remaining old resources (server, attachment, firewall, ssh key, tailscale key)**

Run: `tofu -chdir=tofu/hetzner destroy -auto-approve`
Expected: destroys everything still in the old state (server, attachment, firewall, ssh key,
tailscale key). The volume is untouched (no longer in state). This deletes the running box —
acceptable; the volume (data) is preserved.

- [ ] **Step 4: Adopt the volume into `infra` and apply**

Provide the same values the box used (via `tofu/infra/terraform.tfvars` — copy from
`tofu/hetzner/terraform.tfvars`: `hcloud_token`, `user`, `ssh_public_key`; keep `plan=cpx41`).

Run:
```bash
tofu -chdir=tofu/infra import hcloud_volume.data <VOLUME_ID>
tofu -chdir=tofu/infra plan
```
Expected plan: `hcloud_volume.data` shows **no changes** (adopted, not recreated);
`hcloud_ssh_key.dev`, `hcloud_firewall.jauntbox`, `tailscale_tailnet_key.ephemeral` are
**to be created** (fresh). **Zero resources to destroy.** If the volume shows as replace/destroy,
STOP — do not apply.

Run: `tofu -chdir=tofu/infra apply` → confirm.
Expected: durable resources created; volume adopted unchanged.

- [ ] **Step 5: Stand the box back up via the `box` root**

Provide `tofu/box/terraform.tfvars` (`hcloud_token`, `box_on = true`).

Run: `tofu -chdir=tofu/box init && tofu -chdir=tofu/box apply`
Expected: one `hcloud_server.jauntbox[0]` + one `hcloud_volume_attachment.data[0]` created,
reading name/spec/user_data from the infra outputs.

- [ ] **Step 6: Verify the box is healthy and the data survived**

Wait ~2–3 min, then:
- `tailscale status | grep jauntbox-tsultrim` → the box is online.
- SSH in and confirm `/home` has the pre-migration contents (e.g. `ls /home/tsultrim` shows the
  prior toolchain/home): `ssh root@jauntbox-tsultrim 'ls -la /home/tsultrim | head'`
- Confirm the volume was mounted (not reformatted): the home dir is populated, not empty.

Expected: box online, `/home/tsultrim` populated from the preserved volume.

> **Note:** the Tailscale `-1` hostname question is tracked separately and is not a gate for
> this plan — record whatever hostname the box joins as.

- [ ] **Step 7: Commit any tfvars.example / doc updates (no state, no secrets)**

```bash
git add -A ':!*.tfvars' ':!*.tfstate*'
git commit -m "chore(tofu): migrate live volume into infra; box up via split roots" || true
```

---

### Task 4: Verify the `box_on` toggle end-to-end

Proves the daily up/down path works against the split roots and that `down` never touches
durable infra.

**Files:** none (operational verification).

- [ ] **Step 1: Bring the box DOWN**

Run: `tofu -chdir=tofu/box apply -var box_on=false`
Expected plan/apply: destroys **only** `hcloud_server.jauntbox[0]` and
`hcloud_volume_attachment.data[0]`. **No changes to the infra root, no volume destruction.**

- [ ] **Step 2: Confirm durable infra is intact while the box is down**

Run: `tofu -chdir=tofu/infra plan`
Expected: `No changes. Your infrastructure matches the configuration.` (volume, key, firewall,
ssh key all still present.)

- [ ] **Step 3: Bring the box back UP**

Run: `tofu -chdir=tofu/box apply -var box_on=true`
Expected: recreates the server + attachment reading infra outputs; box rejoins the tailnet.
Verify: `tailscale status | grep jauntbox-tsultrim` → online, and `/home` still populated.

- [ ] **Step 4: Confirm the `box` root cannot express durable changes**

Run: `tofu -chdir=tofu/box plan` and inspect the resource set.
Expected: the only managed resources are `hcloud_server.jauntbox` and
`hcloud_volume_attachment.data` — there is no volume/firewall/key resource in this root. This is
the structural guarantee jauntd relies on.

---

### Task 5: Retire the old `tofu/hetzner` root

**Files:**
- Delete: `tofu/hetzner/main.tf`, `tofu/hetzner/variables.tf`, `tofu/hetzner/outputs.tf`,
  `tofu/hetzner/providers.tf`, `tofu/hetzner/self-delete.sh.tftpl`,
  `tofu/hetzner/terraform.tfvars.example`
- Keep locally (gitignored, not committed): any leftover `tofu/hetzner/terraform.tfstate*` —
  now empty; may be deleted manually after confirming Tasks 3–4 passed.

- [ ] **Step 1: Confirm the old state is empty**

Run: `tofu -chdir=tofu/hetzner state list`
Expected: empty output (all resources moved out / destroyed in Task 3).

- [ ] **Step 2: Remove the old root from the repo**

Run: `git rm tofu/hetzner/main.tf tofu/hetzner/variables.tf tofu/hetzner/outputs.tf tofu/hetzner/providers.tf tofu/hetzner/self-delete.sh.tftpl tofu/hetzner/terraform.tfvars.example`

- [ ] **Step 3: Grep for stale references to the old root**

Run: `grep -rn "tofu/hetzner" docs/ README.md 2>/dev/null || echo "no references"`
Expected: update or note any docs that still say `tofu/hetzner` (esp. the test-run quickstart);
fix them to reference `tofu/infra` + `tofu/box`.

- [ ] **Step 4: Commit**

```bash
git add -A ':!*.tfvars' ':!*.tfstate*'
git commit -m "refactor(tofu): retire single hetzner root in favor of infra + box"
```

---

## Roadmap (subsequent plans — not in this plan)

- **Plan 02 — jauntd box + minimal service (single engineer):** bootstrap tofu for the jauntd
  box; a Go `jauntd` that authenticates via Tailscale WhoIs and runs the `box` apply for the
  caller.
- **Plan 03 — `jaunt` CLI:** `jaunt` (up + auto-SSH), `jaunt down`, `jaunt status`.
- **Plan 04 — git-remote config delivery + ACL:** bare repo on jauntd over Tailscale SSH,
  promoted ref, `post-receive` → `infra` apply; apply the new Tailscale ACL.
- **Plan 05 — multi-engineer + admin ergonomics:** engineer registry, per-user state keys,
  per-engineer isolation ACL.

Plans 01–03 are the walking skeleton (one engineer, `jaunt up/down` over the tailnet).
