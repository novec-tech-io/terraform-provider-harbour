# terraform-provider-harbour

Terraform provider for [Harbour](https://harbour.novec.io) — a managed private PKI SaaS on AWS. Allows customers to issue and manage certificates in their Harbour deployment directly from Terraform, composable with any AWS resource that accepts a certificate ARN.

---

## Requirements

- Terraform >= 1.0
- Go >= 1.22 (to build from source)
- AWS credentials with permission to assume the `harbour-customer-{env}` role in your Harbour account

---

## Installation

### Terraform Registry (once published)

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

### Local build (dev_overrides)

The provider is not yet published to the registry. For local development, `dev_overrides` in a `.terraformrc` file points Terraform at the compiled binary directly — no registry lookup, no `terraform init` required for the provider.

```bash
make build        # compile binary to repo root
cd dev
make setup        # generate dev/.terraformrc with correct local path (gitignored)
make tf-apply     # AWS_PROFILE=<your-profile> by default
```

---

## Provider Configuration

```hcl
provider "harbour" {
  endpoint = "https://api.harbour.example"   # PHZ — resolvable from within the customer VPC only
  # endpoint = "https://<id>.execute-api.eu-west-1.amazonaws.com"  # raw URL for CI outside the VPC
  region   = "eu-west-1"                      # AWS region (or set AWS_REGION)
  role_arn = "arn:aws:iam::ACCOUNT_ID:role/harbour-customer-prod"
}
```

| Argument | Required | Description |
|----------|----------|-------------|
| `endpoint` | Yes | Harbour API endpoint URL |
| `region` | No | AWS region for SigV4 signing. Falls back to `AWS_REGION` / `AWS_DEFAULT_REGION` |
| `profile` | No | AWS profile name |
| `role_arn` | No | IAM role ARN to assume (typically the `harbour-customer-{env}` role) |

All API calls are authenticated via AWS SigV4 (`execute-api` service). The provider uses the standard AWS credential chain — environment variables, shared credentials file, instance profile, etc.

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

## Building

```bash
make build    # compile binary
make install  # compile + install to local plugin cache
make test     # run tests
make lint     # golangci-lint
```

---

## How it works

The provider signs all requests with AWS SigV4 (`execute-api` service) using the configured credentials. Certificate issuance is asynchronous — `terraform apply` polls every 5 seconds (up to 5 minutes) until the certificate reaches `issued` status or fails.

`terraform destroy` calls `POST /certificates/revoke`. A 404 or 409 response (cert already gone or already revoked) is treated as success — destroy is idempotent.

The underlying API is the Harbour HTTP API Gateway, which requires the caller to hold `execute-api:Invoke` on the API resource via IAM.
