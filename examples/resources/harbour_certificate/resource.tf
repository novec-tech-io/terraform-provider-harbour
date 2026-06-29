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
