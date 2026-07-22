output "hostname" { value = "${var.hostname_prefix}-${var.user}" }
output "magicdns" { value = "${var.hostname_prefix}-${var.user} (MagicDNS; reachable on your tailnet)" }
output "volume_id" { value = hcloud_volume.data.id }
