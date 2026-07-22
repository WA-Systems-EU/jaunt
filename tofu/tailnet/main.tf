# Generates the tailnet ACL that gives each engineer access to ONLY their own
# jauntbox. One box tag per engineer (`tag:jaunt-<user>`); an SSH + network grant
# from that engineer's login to that tag and nothing else. Default-deny otherwise.
locals {
  # Every per-user tag must be declared here or the box's auth key is rejected.
  tag_owners = { for u, _ in var.engineers : "tag:jaunt-${u}" => ["autogroup:admin"] }

  # Network-layer: engineer -> their box (all ports). Classic ACL dst is host:port.
  user_acls = [for u, login in var.engineers : {
    action = "accept"
    src    = [login]
    dst    = ["tag:jaunt-${u}:*"]
  }]

  # Optional catch-all so admins keep full reach (and don't lock themselves out).
  admin_acls = var.admin_full_access ? [{
    action = "accept"
    src    = ["autogroup:admin"]
    dst    = ["*:*"]
  }] : []

  # Tailscale SSH: engineer -> their box only, as root or a non-root user.
  user_ssh = [for u, login in var.engineers : {
    action = "accept"
    src    = [login]
    dst    = ["tag:jaunt-${u}"]
    users  = ["autogroup:nonroot", "root"]
  }]

  policy = {
    tagOwners = local.tag_owners
    acls      = concat(local.admin_acls, local.user_acls)
    ssh       = local.user_ssh
  }
}

resource "tailscale_acl" "jaunt" {
  acl                        = jsonencode(local.policy)
  overwrite_existing_content = var.overwrite_existing_content
  reset_acl_on_destroy       = var.reset_acl_on_destroy
}
