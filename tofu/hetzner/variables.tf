variable "hcloud_token" {
  type      = string
  sensitive = true
}
variable "box_on" {
  type    = bool
  default = false
}
variable "user" { type = string }
variable "plan" {
  type = string
  # x86 is required (df stack has amd64-pinned images; toolchain pins are x86). cpx42 =
  # 8 vCPU / 16 GB, new gen (cpx41 retired). Do NOT default to an ARM (cax) type.
  default = "cpx42"
}
variable "volume_size" {
  type    = number
  default = 100
}
variable "ssh_public_key" { type = string }
variable "hostname_prefix" {
  type    = string
  default = "jauntbox"
}
variable "location" {
  type    = string
  default = "fsn1"
}
# Give the box a public IPv4. REQUIRED for egress, not for inbound: github.com,
# codeload.github.com and objects.githubusercontent.com have no IPv6 (no AAAA), so an
# IPv6-only box cannot clone repos or pull release assets and provisioning fails. Inbound
# stays default-deny at the firewall (no port 22 — Tailscale SSH only), so this only opens
# outbound reachability. Costs ~€0.50/mo per box; boxes self-delete when idle.
variable "public_ipv4" {
  type    = bool
  default = true
}
# TEMP/DEBUG ONLY. Opens inbound TCP 22 from anywhere on the box's public IP. The
# intended posture is default-deny (no port 22 — Tailscale SSH only); set this true
# only to debug a box you can't yet reach over Tailscale, then set it back to false.
variable "allow_ssh" {
  type    = bool
  default = false
}
variable "idle_day" {
  type    = number
  default = 7200
}
variable "idle_night" {
  type    = number
  default = 3600
}
variable "base_image" {
  type    = string
  default = "ubuntu-26.04"
}
variable "setup_steps" {
  type    = list(string)
  default = []
}
