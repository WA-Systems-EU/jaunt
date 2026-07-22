# jaunt

> **an ephemeral remote development environment you pop into and out of**

Spin a powerful remote dev box up in the morning and delete it at night. Your
work stays put on a persistent volume, and the box is always reachable at a
stable [Tailscale](https://tailscale.com) MagicDNS hostname (`jauntbox-<you>`).
When you stop working, the box tears *itself* down — because a stopped cloud
server still bills, only a deleted one stops the meter. Next morning, `apply`
again and your volume re-attaches in about two minutes.

It runs on commodity EU cloud (Hetzner and UpCloud), so it's cheap and
sovereign by construction. jaunt isn't a SaaS — the value is the pattern, not a
hosted service.

## 30-second try

You'll need an [OpenTofu](https://opentofu.org) install, a Hetzner Cloud API
token, and a Tailscale API key. Then:

```sh
cd tofu/hetzner
cp terraform.tfvars.example terraform.tfvars   # set your user + SSH public key
tofu init
tofu apply -var box_on=true                    # box up, volume attached, Tailscale SSH ready
# ... work over `ssh jauntbox-<you>` ...
tofu apply -var box_on=false                   # box deleted, volume (your work) kept
```

The same commands work from `tofu/upcloud` for a second EU provider.

## Status

This repo is the **tofu foundation**: a complete, toggle-able jauntbox on
Hetzner and UpCloud, with the signature idle self-teardown. A fast `jaunt` Go
CLI (`jaunt up` / `connect` / `down`) that replaces the tofu hot path with a
one-command daily workflow is a **later milestone** — the `cmd/` and `internal/`
directories are reserved for it.

## Learn more

<!-- DOMAIN -->

Built by [WA Systems](https://wa-systems.eu).
