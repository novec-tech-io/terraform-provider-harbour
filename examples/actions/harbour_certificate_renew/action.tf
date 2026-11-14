# Renew a certificate right now, on demand:
#
#   terraform apply -invoke=action.harbour_certificate_renew.api
#
# Keep action blocks in their own file (or module) with this version
# constraint: Terraform older than 1.14 rejects any configuration that
# contains an `action` block.
terraform {
  required_version = ">= 1.14"
}

resource "harbour_certificate" "api" {
  common_name   = "api.example.internal"
  ttl           = "90d"
  export_to_acm = true
}

action "harbour_certificate_renew" "api" {
  config {
    request_id = harbour_certificate.api.request_id
  }
}
