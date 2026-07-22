terraform {
  required_version = ">= 1.6"
  required_providers {
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}

provider "tailscale" {} # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
