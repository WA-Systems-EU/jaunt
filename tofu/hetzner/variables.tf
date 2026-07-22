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
  type    = string
  default = "cax41"
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
variable "public_ipv4" {
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
  default = "ubuntu-24.04"
}
variable "setup_steps" {
  type    = list(string)
  default = []
}
