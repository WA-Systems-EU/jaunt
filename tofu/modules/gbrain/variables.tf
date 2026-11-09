# gbrain — a SHARED, always-on box for the Tan gbrain project. Same provider-neutral
# shape as modules/jauntbox, minus the idle-watch agent: this box is a standing resource
# people connect to, so nothing reaps it. Its only off switch is the root's `gbrain_on`.
#
# Unlike the jauntbox this box is NOT named per engineer — it's `gbrain-<instance>`,
# where instance is a revision number you bump when you replace it.
variable "instance" {
  type    = string
  default = "00"
}
# The unix account created on the box (and the owner of /home). It's a shared box, but it
# still needs one account; the root passes the applying engineer's `user`.
variable "user" { type = string }
variable "hostname_prefix" {
  type    = string
  default = "gbrain"
}
variable "tailscale_tag" {
  type    = string
  default = "tag:gbrain"
}
variable "tailscale_auth_key" {
  type      = string
  sensitive = true
}
# Extra flags appended to `tailscale up`. NOTE: `tailscale up` has no `--ephemeral`
# flag — a node is ephemeral because its AUTH KEY is ephemeral (see the
# tailscale_tailnet_key resource in each root). Default empty; set only for real
# `tailscale up` flags (e.g. "--accept-routes").
variable "tailscale_extra_args" {
  type    = string
  default = ""
}
# Idempotent setup, re-run on every boot. Empty for now — gbrain gets installed by hand
# on the box until we know what it needs.
variable "setup_steps" {
  type    = list(string)
  default = []
}
# Install the language toolchain (asdf/mise + Go, Rust, Ruby, Node) into the account's
# home on first boot. The user + Docker are created regardless; this gates only the
# (slow, cold-boot) language runtimes.
variable "install_dev_toolchain" {
  type    = bool
  default = true
}
variable "volume_device" { type = string }
