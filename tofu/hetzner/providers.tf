terraform {
  required_version = ">= 1.6"
  required_providers {
    hcloud    = { source = "hetznercloud/hcloud", version = "~> 1.48" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}

provider "hcloud" { token = var.hcloud_token }
provider "tailscale" {} # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
