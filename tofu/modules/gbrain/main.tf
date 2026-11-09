locals {
  # Shared box: named by revision, not by engineer. Bump `instance` to stand up a
  # replacement alongside the old one.
  hostname = "${var.hostname_prefix}-${var.instance}"

  # Build setup.sh in HCL and ship it base64-encoded (like every other file),
  # instead of an inline YAML loop — block-scalar indentation is too fragile.
  setup_script = join("\n", concat(["#!/bin/sh", "set -eu"], var.setup_steps))

  user_data = templatefile("${path.module}/cloud-init/user-data.tftpl", {
    setup_b64 = base64encode(local.setup_script)
    # provisioning is shared with modules/jauntbox — one copy, no drift
    provision_b64        = base64encode(file("${path.module}/../provision/dev-setup.sh"))
    provision_user_b64   = base64encode(file("${path.module}/../provision/dev-setup-user.sh"))
    install_toolchain    = var.install_dev_toolchain
    docker_data_root     = "/home/.docker"
    volume_device        = var.volume_device
    tailscale_key_b64    = base64encode(var.tailscale_auth_key)
    tailscale_tag        = var.tailscale_tag
    tailscale_extra_args = var.tailscale_extra_args
    hostname             = local.hostname
    user                 = var.user
  })
}
