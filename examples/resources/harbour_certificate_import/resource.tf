# Registers a certificate that already lives in ACM (however it got there —
# a previous harbour_certificate with export_to_acm = true, a migration off
# another CA, or anything else) for Harbour lifecycle tracking. Requires the
# same cross-account IAM role as ACM export, plus acm:DescribeCertificate and
# acm:GetCertificate. Its ACM Type must be IMPORTED — an AMAZON_ISSUED cert
# can't be registered this way. No cert material changes at registration —
# the ARN is untouched until Harbour's normal renewal window reissues it.
resource "harbour_certificate_import" "existing" {
  acm_certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/abc-123"
  auto_renew          = true
}

output "cn" {
  description = "Common name read from the existing ACM certificate"
  value       = harbour_certificate_import.existing.cn
}
