data "harbour_certificate" "example" {
  request_id = "550e8400-e29b-41d4-a716-446655440000"
}

output "secret_arn" {
  description = "Secrets Manager ARN for the certificate material"
  value       = data.harbour_certificate.example.secret_arn
}

output "expiry_timestamp" {
  description = "Unix timestamp of certificate expiry"
  value       = data.harbour_certificate.example.expiry_timestamp
}
