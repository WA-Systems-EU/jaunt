terraform {
  required_version = ">= 1.6"
  required_providers {
    upcloud   = { source = "UpCloudLtd/upcloud", version = "~> 5.0" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}

provider "upcloud" {}   # reads UPCLOUD_USERNAME / UPCLOUD_PASSWORD from env
provider "tailscale" {} # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
