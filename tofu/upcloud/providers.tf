terraform {
  required_version = ">= 1.6"
  required_providers {
    upcloud   = { source = "UpCloudLtd/upcloud", version = "~> 5.0" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.16" }
  }
}

# Creds come from the tofu vars (also used for the self-delete script), so you set
# them once. They still fall back to UPCLOUD_USERNAME / UPCLOUD_PASSWORD if unset.
provider "upcloud" {
  username = var.upcloud_username
  password = var.upcloud_password
}
provider "tailscale" {} # reads TAILSCALE_API_KEY / TAILSCALE_TAILNET from env
