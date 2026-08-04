resource "harbour_certificate" "example" {
  common_name = "api.example.internal"
  ttl         = "90d"
  alt_names   = ["api-v2.example.internal"]
}

output "secret_arn" {
  description = "Secrets Manager ARN for the certificate material"
  value       = harbour_certificate.example.secret_arn
}

output "expiry_timestamp" {
  description = "Unix timestamp of certificate expiry"
  value       = harbour_certificate.example.expiry_timestamp
}

# export_to_acm requires ACM export to be configured for this tenant
# (a cross-account IAM role granting harbour-core sts:AssumeRole + acm:ImportCertificate).
# The resulting ARN can be wired directly into AWS resources that expect an ACM cert.
resource "harbour_certificate" "api" {
  common_name   = "api.example.internal"
  ttl           = "90d"
  export_to_acm = true
}

resource "aws_lb_listener" "https" {
  certificate_arn = harbour_certificate.api.acm_certificate_arn
  # ...
}

# csr lets you supply your own Certificate Signing Request — Harbour signs
# your public key and never generates or holds the private key. The
# certificate's CN/SANs come from the CSR itself, not from common_name/
# alt_names. Conflicts with export_to_acm (which needs the private key)
# and alt_names (SANs must be in the CSR's own extension instead).
resource "harbour_certificate" "csr_example" {
  common_name = "csr-service.example.internal" # must match the CSR's subject CN
  csr         = file("${path.module}/csr-service.csr")
}

# delivery_account_id steers export_to_acm at multi-account tenants — which
# AWS account the certificate lands in. The account must already be
# registered in delivery_account_ids (PUT /config) and have the
# harbour-managed-access IAM role applied. Omit it to use the tenant's
# default_delivery_account_id instead.
resource "harbour_certificate" "multi_account" {
  common_name         = "api.example.internal"
  ttl                 = "90d"
  export_to_acm       = true
  delivery_account_id = "222222222222"
}
