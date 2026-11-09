# jaunt — tofu foundation

A toggle-able, ephemeral **jauntbox** dev server with idle self-teardown, on two
EU clouds. One shared module holds all the provider-neutral logic; each provider
is a thin root that wires it to real infrastructure.

The Hetzner root also stands up **gbrain** — a *shared*, always-on box for the Tan
gbrain project, named `gbrain-<instance>` rather than per engineer. It reuses the
jauntbox boot flow but has **no** idle agent, so it runs until you toggle it off.
See **The gbrain box** below.

## Quick start (full test run)

Only **Tailscale** needs environment variables; every other credential is a tofu
variable set once in `terraform.tfvars`.

```sh
export TAILSCALE_API_KEY=tskey-api-...   # admin key with auth-key + ACL write scope
export TAILSCALE_TAILNET=-               # your tailnet name, or "-" for the default

# 1. Declare tags + isolation FIRST — a box whose tag isn't in the policy is
#    rejected by Tailscale at join time.
cd tofu/tailnet
cp terraform.tfvars.example terraform.tfvars   # engineers = { you = "you@wa-systems.eu" }
tofu init && tofu apply -var overwrite_existing_content=true

# 2. Stand up a box. `user` MUST match a key in the `engineers` map above.
cd ../hetzner                                  # or ../upcloud
cp terraform.tfvars.example terraform.tfvars   # Hetzner: hcloud_token, user, ssh_public_key
                                               # UpCloud: upcloud_username/password, user, ssh_public_key
tofu init && tofu apply -var box_on=true       # ~2 min → ssh jauntbox-<you>

# 2b. Optional: the shared gbrain box, toggled independently (Hetzner only).
tofu apply -var gbrain_on=true                 # ~2 min → ssh gbrain-00

# 3. Toggle off (deletes the server, keeps your volume).
tofu apply -var box_on=false
```

Per-root credentials at a glance:

| Root           | tofu variables (in `terraform.tfvars`)                    |
| -------------- | --------------------------------------------------------- |
| `tofu/tailnet` | `engineers` (map: `user → login`)                         |
| `tofu/hetzner` | `hcloud_token`, `user`, `ssh_public_key`                  |
| `tofu/upcloud` | `upcloud_username`, `upcloud_password`, `user`, `ssh_public_key` |

## Layout

```
tofu/
├── modules/jauntbox/     # provider-neutral dev box: cloud-init, systemd units, idle decide()
│   ├── agent/            #   idle-watch.sh (+ decide_test.sh), jaunt-idle.service/.timer
│   ├── cloud-init/       #   user-data.tftpl → rendered cloud-config
│   ├── variables.tf  main.tf  outputs.tf   # renders module output `user_data`
├── modules/gbrain/       # the shared box: same shape, no idle agent, no self-delete
│   ├── cloud-init/       #   user-data.tftpl — a deliberate near-copy, kept diffable
│   ├── variables.tf  main.tf  outputs.tf   # renders module output `user_data`
├── modules/provision/    # SHARED by both modules — one copy, no drift
│   └── dev-setup.sh  dev-setup-user.sh  jaunt-repos.sh
├── hetzner/              # Hetzner root (hcloud_server/volume/firewall)   — per engineer
├── upcloud/              # UpCloud root (upcloud_server/storage)          — per engineer
└── tailnet/              # admin-run root: the Tailscale ACL that isolates engineers
```

The provider roots consume `module.jauntbox.user_data` and differ only in
provider-specific bits: server/volume resources, the self-delete API call, and
`location` vs `zone`. The hetzner root additionally consumes
`module.gbrain.user_data`. The `tailnet` root is different — it's run once by an
admin and manages the tailnet-wide access policy (see **Multi-engineer isolation**).

`modules/provision/` is shared deliberately: `dev-setup.sh` hardcodes
`/etc/jaunt/provision.env` and `/opt/jaunt/dev-setup-user.sh`, so **both** cloud-init
templates keep those paths even on the gbrain box.

## Shared variable surface

Both roots expose the same knobs (defaults shown):

| Variable          | Hetzner default | UpCloud default            | Meaning                                  |
| ----------------- | --------------- | -------------------------- | ---------------------------------------- |
| `box_on`          | `false`         | `false`                    | **the toggle** — box exists only when true |
| `user`            | _(required)_    | _(required)_               | owner; drives naming, labels, MagicDNS   |
| `ssh_public_key`  | _(required)_    | _(required)_               | key seeded onto the box                  |
| `plan`            | `cpx42`         | `2xCPU-4GB`                | server size (Hetzner: x86 required — the df stack is amd64-pinned; do **not** use a `cax` type) |
| `volume_size`     | `100`           | `100`                      | persistent volume size (GB)              |
| `hostname_prefix` | `jauntbox`      | `jauntbox`                 | → MagicDNS name `jauntbox-<user>`        |
| `location`/`zone` | `fsn1`          | `fi-hel2`                  | region                                   |
| `public_ipv4`     | `true`          | _(always on)_              | Hetzner: attach a public IPv4 — required for **egress** (github.com has no AAAA), not inbound. UpCloud always gets one (firewalled) |
| `idle_day`        | `7200`          | `7200`                     | idle seconds before teardown (day)       |
| `idle_night`      | `3600`          | `3600`                     | idle seconds before teardown (night)     |
| `setup_steps`     | `[]`            | `[]`                       | idempotent project setup, run on every up |

Hetzner also takes `hcloud_token`; UpCloud takes `upcloud_username` /
`upcloud_password`.

### gbrain knobs (Hetzner only)

| Variable             | Default | Meaning                                              |
| -------------------- | ------- | ---------------------------------------------------- |
| `gbrain_on`          | `false` | **the toggle** — independent of `box_on`             |
| `gbrain_instance`    | `"00"`  | revision number → MagicDNS name `gbrain-00`. Bump to replace |
| `gbrain_plan`        | `cpx42` | server size (x86, same constraint as `plan`)         |
| `gbrain_volume_size` | `50`    | persistent volume size (GB)                          |
| `gbrain_setup_steps` | `[]`    | idempotent setup, re-run every boot                  |

`base_image`, `location`, `public_ipv4`, `ssh_public_key` and the firewall are shared
with the jauntbox; there are no `idle_*` knobs because there is no idle agent.

## Credentials

**Tailscale** authenticates from the environment (all three roots):

| Env                  | Value                                          |
| -------------------- | ---------------------------------------------- |
| `TAILSCALE_API_KEY`  | admin API key with **auth-key + ACL** write scope |
| `TAILSCALE_TAILNET`  | your tailnet name, or `-` for the default      |

**Cloud provider creds are tofu variables** (set once in `terraform.tfvars`):

| Provider | Variables                                |
| -------- | ---------------------------------------- |
| Hetzner  | `hcloud_token`                           |
| UpCloud  | `upcloud_username`, `upcloud_password` (also authenticate the provider; fall back to `UPCLOUD_USERNAME`/`UPCLOUD_PASSWORD` env) |

Never commit `terraform.tfvars` — copy `terraform.tfvars.example` and fill it in
locally (`.tfvars` is gitignored).

## Lifecycle

```sh
cd tofu/hetzner            # or tofu/upcloud
cp terraform.tfvars.example terraform.tfvars   # set user + ssh_public_key
tofu init
tofu apply -var box_on=true    # box up, volume attached, Tailscale SSH ready (~2 min)
# ... work over `ssh jauntbox-<you>` ...
tofu apply -var box_on=false   # box DELETED, volume (your work) kept
```

The persistent volume is **never** gated by `box_on` — it survives every toggle.
Only the server, its volume attachment, and the ephemeral Tailscale key come and
go.

## Delete, don't stop

The whole point: **a stopped cloud server still bills; only a deleted one stops
the meter.** So `box_on=false` deletes the server outright, and the box's own
idle-watch agent self-deletes it after `idle_day`/`idle_night` of genuine idle.
Your work lives on the volume, not the server.

## The gbrain box

`gbrain-00` is a **shared** box for the Tan gbrain project — one instance the whole
team connects to, not one per engineer. Everything about its boot is the jauntbox flow
— volume mounted at `/home`, tailscaled state parked on that volume so the MagicDNS
name survives replacement, `tailscale up --ssh`, then the shared provisioning — with
one deliberate subtraction: **no idle-watch agent and no self-delete script.** A box
people connect to on demand can't be reaped after two quiet hours.

```sh
cd tofu/hetzner
tofu apply -var gbrain_on=true    # ~2 min → ssh gbrain-00
tofu apply -var gbrain_on=false   # the ONLY off switch; volume kept
```

> ⚠️ Nothing reaps this box. It bills until you flip `gbrain_on=false`. That is the
> intended trade, but it is the one box in this repo where forgetting costs money.

> ⚠️ **It lives in the per-engineer Hetzner root.** That root's state describes one
> engineer, so `gbrain-00` belongs to whichever engineer applied it. A second engineer
> applying this root will try to create the same server and volume and **collide**.
> Before anyone else runs it, move gbrain into its own root with its own state.

`gbrain_setup_steps` renders to `/opt/jaunt/setup.sh` and runs on every boot, so keep
the steps idempotent. It's empty today — gbrain gets installed by hand on the box.

Bumping `gbrain_instance` to `"01"` names the next box `gbrain-01`; note that the
volume is named after the instance too, so a bump means a **fresh** volume.

Verify the subtraction actually took on a live box: `systemctl list-timers | grep
jaunt-idle` should print nothing.

## Multi-engineer isolation

The model is **one pair of boxes per engineer, reachable only by that engineer.** It
rests on two independent layers:

1. **Cloud firewall** (both providers): default-deny inbound, no port 22. Nothing
   reaches a box over its public IP — full stop. On Hetzner one firewall covers both
   boxes: it selects on the label `jaunt=<user>`, which both carry.
2. **Tailscale ACL** (the `tailnet` root): each box advertises a per-engineer,
   per-box tag — `tag:jaunt-<user>`, plus the shared `tag:gbrain` — and the tailnet
   policy grants each engineer SSH + network access to **only their own two tags**,
   default-deny for everyone else. Admins keep full reach (toggle with
   `admin_full_access`).

Without layer 2 a box is merely *not public* — anyone on the tailnet could still
reach it. The `tailnet` root is what makes "only that engineer" true, so **apply
it first**:

```sh
cd tofu/tailnet
cp terraform.tfvars.example terraform.tfvars   # engineers = { alice = "alice@…", … }
tofu init
tofu plan            # review the rendered policy (output `policy_json`)
tofu apply -var overwrite_existing_content=true   # ⚠ this root OWNS the tailnet ACL
```

> ⚠️ **`tofu/tailnet` manages the entire tailnet ACL.** `overwrite_existing_content`
> defaults to `false` so a first apply won't silently clobber an existing policy;
> set it `true` only when you intend this root to own the policy. On `destroy` it
> resets the ACL to Tailscale's permissive default (`reset_acl_on_destroy`) so you
> don't lock yourself out. Prefer not to let tofu own it? Run `tofu plan` and paste
> the `policy_json` output into the admin console by hand.
>
> **As of today this root has an empty state — the live policy is hand-managed** and
> carries tags this root does not generate (`tag:jauntd`, `tag:home`) plus an
> `autogroup:self` SSH rule. Applying it with `overwrite_existing_content=true` would
> **delete those**. Until the live policy and this root are reconciled, add new tags
> to the admin console by hand and treat `policy_json` as a reference rendering.

Each engineer's `user` (in the hetzner/upcloud root) must match a key in the
`engineers` map — that's what ties `jauntbox-alice` to `tag:jaunt-alice` and
`alice@…`. The shared `gbrain-00` box is the exception — it carries one flat
`tag:gbrain` that every engineer is granted. A box whose tag isn't declared in the
policy will be **rejected** by Tailscale at join time, which is why the `tailnet`
apply comes first — including after you add `tag:gbrain` to an existing tailnet.

## State

Each engineer gets a **separate tofu state** — a provider root describes that one
engineer's boxes, so N engineers means N states. Keep them apart one of two ways:

- **Local / solo:** a workspace per engineer — `tofu workspace new alice` before
  `apply`. State stays on the laptop.
- **Team (recommended):** a remote backend with a per-engineer state key (e.g.
  `key = "jaunt/alice/hetzner"` in an S3-compatible bucket), so state isn't trapped
  on one machine and two engineers never share a state file.

The admin-run `tailnet` root has its own single state (there's one tailnet policy).
