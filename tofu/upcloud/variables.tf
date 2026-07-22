variable "box_on" {
  type    = bool
  default = false
}
variable "user" { type = string }
variable "plan" {
  type    = string
  default = "2xCPU-4GB"
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
variable "zone" {
  type    = string
  default = "fi-hel2"
}
variable "public_ipv4" {
  type    = bool
  default = true
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
  default = "Ubuntu Server 24.04 LTS (Noble Numbat)"
}
variable "setup_steps" {
  type    = list(string)
  default = []
}
variable "upcloud_username" {
  type      = string
  sensitive = true
}
variable "upcloud_password" {
  type      = string
  sensitive = true
}
