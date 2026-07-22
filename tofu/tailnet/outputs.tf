# The rendered policy — review this with `tofu plan` before applying, and paste it
# into the Tailscale admin console if you'd rather not have tofu own the ACL.
output "policy_json" {
  value = jsonencode(local.policy)
}

output "managed_tags" {
  value = keys(local.tag_owners)
}
