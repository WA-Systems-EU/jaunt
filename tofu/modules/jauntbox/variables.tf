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
variable "tailscale_extra_args" {
  type    = string
  default = "--ephemeral"
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
variable "self_delete_script" {
  type      = string
  sensitive = true
}
variable "volume_device" { type = string }
