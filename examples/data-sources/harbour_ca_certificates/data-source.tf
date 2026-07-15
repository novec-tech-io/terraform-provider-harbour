data "harbour_ca_certificates" "this" {}

# Anchor trust on the root certificate — write it to a local trust-store file...
resource "local_file" "harbour_root_ca" {
  content         = data.harbour_ca_certificates.this.root_pem
  filename        = "/etc/pki/ca-trust/source/anchors/harbour-root-ca.pem"
  file_permission = "0644"
}

# ...or distribute it via SSM for instances to pull at boot.
resource "aws_ssm_parameter" "harbour_root_ca" {
  name  = "/pki/harbour/root-ca"
  type  = "String"
  value = data.harbour_ca_certificates.this.root_pem
}

output "chain_pem" {
  description = "Full CA chain (intermediate first, root last)"
  value       = data.harbour_ca_certificates.this.chain_pem
}
