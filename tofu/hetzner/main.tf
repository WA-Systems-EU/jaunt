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
  volume_device      = "/dev/disk/by-id/scsi-0HC_Volume_${hcloud_volume.data.id}"
  self_delete_script = templatefile("${path.module}/self-delete.sh.tftpl", {
    hcloud_token = var.hcloud_token
  })
}

resource "hcloud_firewall" "jauntbox" {
  name = "jaunt-${var.user}"
  # default-deny inbound: NO port 22 (Tailscale SSH only). Allow all egress.
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
  apply_to { label_selector = "jaunt=${var.user}" }
}

resource "hcloud_server" "jauntbox" {
  count       = var.box_on ? 1 : 0
  name        = "${var.hostname_prefix}-${var.user}"
  server_type = var.plan
  image       = var.base_image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.dev.id]
  user_data   = module.jauntbox.user_data
  labels      = { jaunt = var.user }
  public_net {
    ipv4_enabled = var.public_ipv4
    ipv6_enabled = true
  }
}

resource "hcloud_volume_attachment" "data" {
  count     = var.box_on ? 1 : 0
  volume_id = hcloud_volume.data.id
  server_id = hcloud_server.jauntbox[0].id
  automount = false # cloud-init mounts at /home explicitly
}
