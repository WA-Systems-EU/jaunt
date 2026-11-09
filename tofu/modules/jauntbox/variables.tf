variable "user" { type = string }
variable "hostname_prefix" {
  type    = string
  default = "jauntbox"
}
variable "tailscale_tag" {
  type    = string
  default = "tag:jauntbox"
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
variable "idle_day_secs" {
  type    = number
  default = 7200
}
variable "idle_night_secs" {
  type    = number
  default = 3600
}
variable "cpu_threshold" {
  type    = number
  default = 15
}
variable "setup_steps" {
  type    = list(string)
  default = []
}
# Install the language toolchain (asdf/mise + Go, Rust, Ruby, Node) into the engineer's
# home on first boot. The engineer user + Docker are created regardless; this gates only
# the (slow, cold-boot) language runtimes.
variable "install_dev_toolchain" {
  type    = bool
  default = true
}
variable "self_delete_script" {
  type      = string
  sensitive = true
}
variable "volume_device" { type = string }
