# Generates the tailnet ACL that gives each engineer access to ONLY their own jauntbox,
# plus the shared gbrain box. Per-engineer tag `tag:jaunt-<user>` with an SSH + network
# grant from that engineer's login and nothing else; `tag:gbrain` is deliberately shared —
# every engineer reaches it, because that is the point of a shared box.
# Default-deny otherwise.
locals {
  # Every tag must be declared here or the box's auth key is rejected at join.
  tag_owners = merge(
    { for u, _ in var.engineers : "tag:jaunt-${u}" => ["autogroup:admin"] },
    { "tag:gbrain" = ["autogroup:admin"] },
  )

  # Network-layer: engineer -> their own box + the shared box. Classic ACL dst is host:port.
  user_acls = [for u, login in var.engineers : {
    action = "accept"
    src    = [login]
    dst    = ["tag:jaunt-${u}:*", "tag:gbrain:*"]
  }]

  # Optional catch-all so admins keep full reach (and don't lock themselves out).
  admin_acls = var.admin_full_access ? [{
    action = "accept"
    src    = ["autogroup:admin"]
    dst    = ["*:*"]
  }] : []

  # Tailscale SSH: engineer -> their own box + the shared gbrain box, as root or non-root.
  user_ssh = [for u, login in var.engineers : {
    action = "accept"
    src    = [login]
    dst    = ["tag:jaunt-${u}", "tag:gbrain"]
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
