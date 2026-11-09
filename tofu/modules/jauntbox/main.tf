locals {
  # Build setup.sh in HCL and ship it base64-encoded (like every other file),
  # instead of an inline YAML loop — block-scalar indentation is too fragile.
  setup_script = join("\n", concat(["#!/bin/sh", "set -eu"], var.setup_steps))

  user_data = templatefile("${path.module}/cloud-init/user-data.tftpl", {
    idle_watch_b64       = base64encode(file("${path.module}/agent/idle-watch.sh"))
    self_delete_b64      = base64encode(var.self_delete_script)
    service_b64          = base64encode(file("${path.module}/agent/jaunt-idle.service"))
    timer_b64            = base64encode(file("${path.module}/agent/jaunt-idle.timer"))
    setup_b64            = base64encode(local.setup_script)
    provision_b64        = base64encode(file("${path.module}/../provision/dev-setup.sh"))
    provision_user_b64   = base64encode(file("${path.module}/../provision/dev-setup-user.sh"))
    install_toolchain    = var.install_dev_toolchain
    docker_data_root     = "/home/.docker"
    idle_day_secs        = var.idle_day_secs
    idle_night_secs      = var.idle_night_secs
    cpu_threshold        = var.cpu_threshold
    volume_device        = var.volume_device
    tailscale_key_b64    = base64encode(var.tailscale_auth_key)
    tailscale_tag        = var.tailscale_tag
    tailscale_extra_args = var.tailscale_extra_args
    hostname_prefix      = var.hostname_prefix
    user                 = var.user
  })
}
