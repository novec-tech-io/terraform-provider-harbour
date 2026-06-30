# terraform-provider-harbour

Terraform provider for [Harbour](https://harbour.novec.io) — a managed private PKI SaaS on AWS. Allows customers to issue and manage certificates directly from Terraform, composable with any AWS resource that accepts a certificate ARN.

---

## Requirements

- Terraform >= 1.0
- An active Harbour subscription with a provisioned deployment

---

## Installation

```hcl
terraform {
  required_providers {
    harbour = {
      source  = "novec-tech-io/harbour"
      version = "~> 0.1"
    }
  }
}
```

---

## Authentication

All API calls are signed with AWS SigV4 (`execute-api` service). The provider uses the standard AWS credential chain — environment variables, shared credentials file, instance profile, ECS task role, etc.

Your Harbour deployment comes with a scoped IAM role (`harbour-customer-{env}`) in the Harbour account. You need to assume this role to make API calls. There are two ways to configure this:

### Option A — provider assumes the role

Provide your own AWS credentials (any source in the credential chain) and let the provider assume the role:

```hcl
provider "harbour" {
  endpoint = "https://<api-id>.execute-api.eu-west-1.amazonaws.com"
  region   = "eu-west-1"
  role_arn = "arn:aws:iam::<harbour-account-id>:role/harbour-customer-prod"
}
```

This is the recommended approach for CI/CD pipelines — attach an IAM role to your runner with `sts:AssumeRole` permission on the `harbour-customer-{env}` role, and set `role_arn` in the provider.

### Option B — pre-assumed profile

If your AWS profile is already configured to assume the `harbour-customer-{env}` role (via `role_arn` in `~/.aws/config`), omit `role_arn` from the provider — setting it would cause a double-assumption error:

```hcl
provider "harbour" {
  endpoint = "https://<api-id>.execute-api.eu-west-1.amazonaws.com"
  region   = "eu-west-1"
  profile  = "my-harbour-profile"
}
```

### Provider arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `endpoint` | Yes | Harbour API endpoint URL — provided in your onboarding details |
| `region` | No | AWS region. Falls back to `AWS_REGION` / `AWS_DEFAULT_REGION` |
| `profile` | No | AWS profile name |
| `role_arn` | No | IAM role ARN to assume. Do not set if your profile already assumes the role |

---

## Resources

### `harbour_certificate`

Issues a certificate from your Harbour CA hierarchy. Destroying the resource revokes the certificate.

All arguments are immutable after issuance — any change forces replacement (revoke + re-issue).

```hcl
resource "harbour_certificate" "api" {
  common_name = "api.example.internal"
  ttl         = "90d"
  alt_names   = ["api-v2.example.internal"]
}
```

#### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `common_name` | Yes | Certificate CN |
| `ttl` | No | Certificate TTL, e.g. `90d`, `8760h`. Defaults to the tenant `default_cert_ttl` |
| `alt_names` | No | List of subject alternative names (SANs) |

#### Attributes

| Attribute | Description |
|-----------|-------------|
| `id` | Same as `request_id` |
| `request_id` | Harbour request ID |
| `serial_number` | Certificate serial number |
| `secret_arn` | Secrets Manager ARN containing the certificate material |
| `expiry_timestamp` | Certificate expiry as a Unix timestamp |
| `status` | Current status: `requested`, `issuing`, `issued`, `revoked`, `expired`, `failed` |

---

## Data Sources

### `harbour_certificate`

Reads an existing certificate by request ID. Useful for referencing a certificate managed outside of the current Terraform state.

```hcl
data "harbour_certificate" "existing" {
  request_id = "550e8400-e29b-41d4-a716-446655440000"
}

output "secret_arn" {
  value = data.harbour_certificate.existing.secret_arn
}
```

#### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `request_id` | Yes | Harbour request ID of the certificate to read |

Returns the same attributes as the `harbour_certificate` resource.

---

## How it works

Certificate issuance is asynchronous — `terraform apply` polls every 5 seconds (up to 5 minutes) until the certificate reaches `issued` status or fails.

`terraform destroy` revokes the certificate. A 404 or 409 response (already gone or already revoked) is treated as success — destroy is idempotent.

---

## Contributing

```bash
make build    # compile binary
make install  # compile + install to local plugin cache
make test     # run tests
make lint     # golangci-lint
make docs     # regenerate docs/
```
