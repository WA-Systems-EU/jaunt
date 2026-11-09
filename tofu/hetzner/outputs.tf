output "hostname" { value = "${var.hostname_prefix}-${var.user}" }
output "magicdns" { value = "${var.hostname_prefix}-${var.user} (MagicDNS; reachable on your tailnet)" }
output "volume_id" { value = hcloud_volume.data.id }

output "gbrain_hostname" { value = module.gbrain.hostname }
output "gbrain_magicdns" { value = "${module.gbrain.hostname} (MagicDNS; shared — reachable on the tailnet)" }
output "gbrain_volume_id" { value = hcloud_volume.gbrain.id }
