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
├── hetzner/              # Hetzner root (hcloud_server/volume/firewall)
└── upcloud/              # UpCloud root (upcloud_server/storage) — same surface, `zone` for `location`
```

The roots consume `module.jauntbox.user_data` and differ only in
provider-specific bits: server/volume resources, the self-delete API call, and
`location` vs `zone`.

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
| `public_ipv4`     | `false`         | `true`                     | attach a public IPv4                     |
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

## State

These roots use **local state** for simplicity. For anything real — shared use,
CI, or a machine you might lose — configure a remote backend (e.g. an S3-compatible
bucket) so state isn't trapped on one laptop.
