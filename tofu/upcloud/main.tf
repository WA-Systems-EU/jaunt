resource "upcloud_storage" "data" {
  title = "jaunt-${var.user}-data"
  size  = var.volume_size
  zone  = var.zone
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
  firewall = true # default-deny inbound; rules below. NEVER expose port 22.
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

# Firewall parity with Hetzner: default-deny inbound (NO port 22 — Tailscale SSH
# only), allow all egress. UpCloud's firewall is stateful, so return traffic for
# outbound-initiated connections (Tailscale/DERP, HTTPS, DNS) is permitted;
# Tailscale reaches the box via NAT traversal / DERP with no inbound rule, exactly
# as on Hetzner. Explicit accept/drop rules make the policy deterministic.
resource "upcloud_firewall_rules" "jauntbox" {
  count     = var.box_on ? 1 : 0
  server_id = upcloud_server.jauntbox[0].id

  firewall_rule {
    action    = "accept"
    direction = "out"
    family    = "IPv4"
  }
  firewall_rule {
    action    = "accept"
    direction = "out"
    family    = "IPv6"
  }
  firewall_rule {
    action    = "drop"
    direction = "in"
    family    = "IPv4"
  }
  firewall_rule {
    action    = "drop"
    direction = "in"
    family    = "IPv6"
  }
}
