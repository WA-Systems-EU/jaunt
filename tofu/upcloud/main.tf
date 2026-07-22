resource "upcloud_storage" "data" {
  title = "jaunt-${var.user}-data"
  size  = var.volume_size
  zone  = var.zone
}

resource "tailscale_tailnet_key" "ephemeral" {
  ephemeral     = true
  reusable      = true
  preauthorized = true
  tags          = ["tag:jauntbox"]
}

module "jauntbox" {
  source             = "../modules/jauntbox"
  user               = var.user
  hostname_prefix    = var.hostname_prefix
  tailscale_auth_key = tailscale_tailnet_key.ephemeral.key
  idle_day_secs      = var.idle_day
  idle_night_secs    = var.idle_night
  setup_steps        = var.setup_steps
  volume_device      = "/dev/vdb" # second attached disk on UpCloud KVM
  self_delete_script = templatefile("${path.module}/self-delete.sh.tftpl", {
    upcloud_username = var.upcloud_username
    upcloud_password = var.upcloud_password
  })
}

resource "upcloud_server" "jauntbox" {
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
  user_data = module.jauntbox.user_data
}
