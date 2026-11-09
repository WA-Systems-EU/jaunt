# Map of engineer username => their Tailscale login (email, or a "group:..." ref).
# The username becomes the box tag `tag:jaunt-<username>` and must match the `user` each
# engineer passes to the hetzner/upcloud roots. (The shared gbrain box uses one flat
# `tag:gbrain` instead — it is not per-engineer.) Example:
#   engineers = { alice = "alice@wa-systems.eu", bob = "bob@wa-systems.eu" }
variable "engineers" {
  type = map(string)
}

# Let tailnet admins reach every box (prevents self-lockout and allows support/debug).
# Engineers are still restricted to their own jauntbox + the shared gbrain box regardless.
variable "admin_full_access" {
  type    = bool
  default = true
}

# This resource manages the ENTIRE tailnet ACL. Set true only when you intend this
# root to OWN the tailnet policy — it will replace any existing ACL content.
variable "overwrite_existing_content" {
  type    = bool
  default = false
}

# On `tofu destroy`, reset the tailnet ACL to Tailscale's permissive default rather
# than leaving a restrictive policy behind (avoids locking yourself out).
variable "reset_acl_on_destroy" {
  type    = bool
  default = true
}
