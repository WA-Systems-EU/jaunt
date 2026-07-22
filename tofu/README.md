# jaunt — tofu foundation

A toggle-able, ephemeral **jauntbox** dev server with idle self-teardown, on two
EU clouds. One shared module holds all the provider-neutral logic; each provider
is a thin root that wires it to real infrastructure.

## Layout

```
tofu/
├── modules/jauntbox/     # provider-neutral: cloud-init, systemd units, idle decide()
│   ├── agent/            #   idle-watch.sh (+ decide_test.sh), jaunt-idle.service/.timer
│   ├── cloud-init/       #   user-data.tftpl → rendered cloud-config
│   ├── variables.tf  main.tf  outputs.tf   # renders module output `user_data`
├── hetzner/              # Hetzner root (hcloud_server/volume/firewall)   — per engineer
├── upcloud/              # UpCloud root (upcloud_server/storage)          — per engineer
└── tailnet/              # admin-run root: the Tailscale ACL that isolates engineers
```

The provider roots consume `module.jauntbox.user_data` and differ only in
provider-specific bits: server/volume resources, the self-delete API call, and
`location` vs `zone`. The `tailnet` root is different — it's run once by an admin
and manages the tailnet-wide access policy (see **Multi-engineer isolation**).

## Shared variable surface

Both roots expose the same knobs (defaults shown):

| Variable          | Hetzner default | UpCloud default            | Meaning                                  |
| ----------------- | --------------- | -------------------------- | ---------------------------------------- |
| `box_on`          | `false`         | `false`                    | **the toggle** — box exists only when true |
| `user`            | _(required)_    | _(required)_               | owner; drives naming, labels, MagicDNS   |
| `ssh_public_key`  | _(required)_    | _(required)_               | key seeded onto the box                  |
| `plan`            | `cax41`         | `2xCPU-4GB`                | server size                              |
| `volume_size`     | `100`           | `100`                      | persistent volume size (GB)              |
| `hostname_prefix` | `jauntbox`      | `jauntbox`                 | → MagicDNS name `jauntbox-<user>`        |
| `location`/`zone` | `fsn1`          | `fi-hel2`                  | region                                   |
| `public_ipv4`     | `false`         | _(always on)_              | Hetzner: attach a public IPv4. UpCloud always gets one (firewalled) |
| `idle_day`        | `7200`          | `7200`                     | idle seconds before teardown (day)       |
| `idle_night`      | `3600`          | `3600`                     | idle seconds before teardown (night)     |
| `setup_steps`     | `[]`            | `[]`                       | idempotent project setup, run on every up |

Hetzner also takes `hcloud_token`; UpCloud takes `upcloud_username` /
`upcloud_password`.

## Credentials (env)

| Provider  | Env                                              |
| --------- | ------------------------------------------------ |
| Hetzner   | `HCLOUD_TOKEN`                                    |
| UpCloud   | `UPCLOUD_USERNAME`, `UPCLOUD_PASSWORD`           |
| Tailscale | `TAILSCALE_API_KEY`, `TAILSCALE_TAILNET`         |

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

## Multi-engineer isolation

The model is **one box per engineer, reachable only by that engineer.** It rests
on two independent layers:

1. **Cloud firewall** (both providers): default-deny inbound, no port 22. Nothing
   reaches a box over its public IP — full stop.
2. **Tailscale ACL** (the `tailnet` root): each box advertises a per-engineer tag
   `tag:jaunt-<user>`, and the tailnet policy grants each engineer SSH + network
   access to **only their own tag**, default-deny for everyone else. Admins keep
   full reach (toggle with `admin_full_access`).

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

Each engineer's `user` (in the hetzner/upcloud root) must match a key in the
`engineers` map — that's what ties `jauntbox-alice` to `tag:jaunt-alice` to
`alice@…`. A box whose tag isn't declared in the policy will be **rejected** by
Tailscale at join time, which is why the `tailnet` apply comes first.

## State

Each engineer's box is a **separate tofu state** — a provider root describes one
box, so N engineers means N states. Keep them apart one of two ways:

- **Local / solo:** a workspace per engineer — `tofu workspace new alice` before
  `apply`. State stays on the laptop.
- **Team (recommended):** a remote backend with a per-engineer state key (e.g.
  `key = "jaunt/alice/hetzner"` in an S3-compatible bucket), so state isn't trapped
  on one machine and two engineers never share a state file.

The admin-run `tailnet` root has its own single state (there's one tailnet policy).
