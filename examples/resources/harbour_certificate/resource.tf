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

# import_to_acm requires ACM import to be configured for this tenant
# (a cross-account IAM role granting harbour-core sts:AssumeRole + acm:ImportCertificate).
# The resulting ARN can be wired directly into AWS resources that expect an ACM cert.
resource "harbour_certificate" "api" {
  common_name   = "api.example.internal"
  ttl           = "90d"
  import_to_acm = true
}

resource "aws_lb_listener" "https" {
  certificate_arn = harbour_certificate.api.acm_certificate_arn
  # ...
}
