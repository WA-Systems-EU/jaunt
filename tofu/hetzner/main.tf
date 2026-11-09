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
  tags          = ["tag:jaunt-${var.user}"] # per-user tag → Tailscale ACL isolates each engineer to their own box
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

# --- gbrain: the SHARED, always-on box (Tan gbrain project) ---------------------------
# Not per-engineer: named `gbrain-<instance>` and carrying the shared tag `tag:gbrain`.
# ⚠ It lives in this per-engineer root, so it is described by whichever engineer's state
# applies it. If a second engineer applies this root, they will try to create the same
# `gbrain-00` server and volume and collide. Move it to its own root before that happens.
resource "tailscale_tailnet_key" "gbrain" {
  ephemeral     = true
  reusable      = true
  preauthorized = true
  tags          = ["tag:gbrain"] # must be declared in the tailnet policy or the join is rejected
}

# Deliberately NOT gated by gbrain_on — the box's state survives every toggle.
resource "hcloud_volume" "gbrain" {
  name     = "gbrain-${var.gbrain_instance}-data"
  size     = var.gbrain_volume_size
  location = var.location
  format   = "ext4"
}

module "gbrain" {
  source             = "../modules/gbrain"
  instance           = var.gbrain_instance
  user               = var.user
  tailscale_auth_key = tailscale_tailnet_key.gbrain.key
  setup_steps        = var.gbrain_setup_steps
  volume_device      = "/dev/disk/by-id/scsi-0HC_Volume_${hcloud_volume.gbrain.id}"
}

resource "hcloud_server" "gbrain" {
  count       = var.gbrain_on ? 1 : 0
  name        = module.gbrain.hostname
  server_type = var.gbrain_plan
  image       = var.base_image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.dev.id]
  user_data   = module.gbrain.user_data
  # `jaunt = var.user` is what pulls this box under hcloud_firewall.jauntbox's
  # label_selector — same default-deny-inbound posture, no second firewall.
  labels = { jaunt = var.user, role = "gbrain" }
  public_net {
    ipv4_enabled = var.public_ipv4
    ipv6_enabled = true
  }
}

resource "hcloud_volume_attachment" "gbrain" {
  count     = var.gbrain_on ? 1 : 0
  volume_id = hcloud_volume.gbrain.id
  server_id = hcloud_server.gbrain[0].id
  automount = false # cloud-init mounts at /home explicitly
}

# Applies to BOTH boxes: the label_selector matches every server labelled jaunt=<user>,
# which is the jauntbox and the gbrain box alike.
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
  # TEMP/DEBUG: inbound SSH, gated by var.allow_ssh (default false). Lets you reach a
  # box directly while Tailscale SSH isn't yet working. Set allow_ssh=false to re-lock.
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
