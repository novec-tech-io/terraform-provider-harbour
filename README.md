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
      version = "~> 0.3"
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
| `import_to_acm` | No | Import the issued certificate into ACM in your AWS account. Requires ACM import to be configured for your tenant (see [ACM import](#acm-import) below). Defaults to `false`. Conflicts with `csr` |
| `csr` | No | PEM-encoded Certificate Signing Request — Harbour signs your public key instead of generating a private key server-side (see [CSR support](#csr-support) below). Conflicts with `import_to_acm` and `alt_names` |

#### Attributes

| Attribute | Description |
|-----------|-------------|
| `id` | Same as `request_id` |
| `request_id` | Harbour request ID |
| `serial_number` | Certificate serial number |
| `secret_arn` | Secrets Manager ARN containing the certificate material |
| `expiry_timestamp` | Certificate expiry as a Unix timestamp |
| `status` | Current status: `requested`, `issuing`, `issued`, `revoked`, `expired`, `failed` |
| `acm_certificate_arn` | ARN of the certificate imported into ACM in your account. Only set when `import_to_acm` is `true` |

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

## ACM import

Setting `import_to_acm = true` imports the issued certificate into ACM in your AWS account, exposing a usable `acm_certificate_arn` you can wire directly into AWS resources:

```hcl
resource "harbour_certificate" "api" {
  common_name   = "api.example.internal"
  ttl           = "90d"
  import_to_acm = true
}

resource "aws_lb_listener" "https" {
  certificate_arn = harbour_certificate.api.acm_certificate_arn
  # ...
}
```

This requires a one-time setup in your AWS account: an IAM role trusting Harbour's certificate-issuance **and** revocation Lambdas, granting `acm:ImportCertificate`, `acm:AddTagsToCertificate`, and `acm:DeleteCertificate`. Without this role configured for your tenant, `import_to_acm = true` fails with "ACM import is not configured for this tenant". Contact Novec to enable it.

On renewal, the certificate is re-imported onto the same ACM ARN, so listeners and other references never need to change. On revocation (including `terraform destroy`), Harbour also deletes the certificate from your ACM — best-effort: if the ACM certificate is still attached to a resource (e.g. a load balancer listener you haven't updated yet), the Harbour-side revoke still succeeds and the ACM cleanup is retried automatically until it succeeds. This detail isn't currently surfaced as a provider attribute (the revoke API response has an `acm_cleanup_status` field, but the provider doesn't read or expose it today) — if you need to confirm cleanup succeeded, check the certificate directly in ACM.

---

## CSR support

By default Harbour generates a private key server-side (RSA 2048) and stores it in Secrets Manager. If you'd rather your private key never leave your own environment, generate your own keypair and CSR and pass it in via `csr` — Harbour signs your public key and never generates or holds the private key:

```hcl
resource "harbour_certificate" "csr_example" {
  common_name = "csr-service.example.internal" # must match the CSR's subject CN
  csr         = file("${path.module}/csr-service.csr")
}
```

The certificate's actual CN and SANs always come from the CSR itself, not from `common_name`/`alt_names` — `common_name` is required and validated to match the CSR's subject CN, but is not otherwise authoritative once a CSR is set. This is also why `alt_names` conflicts with `csr`: put your SANs in the CSR's own SAN extension instead. Accepted key types: RSA ≥ 2048 bits, or EC P-256/P-384.

`csr` also conflicts with `import_to_acm` — ACM's `ImportCertificate` API requires the private key as an input, which Harbour never has for a CSR-issued certificate.

**Renewal:** a CSR-issued certificate can never be silently auto-renewed (Harbour has no private key to reissue from) — it always gets routed to Harbour's `certificate.expiring` SNS notification instead of a silent renewal attempt, regardless of `auto_renew`. Submit a fresh CSR (a new `harbour_certificate` resource, since `csr` forces replacement like every other input argument) before the current one expires.

This provider does not generate a keypair/CSR on your behalf — combine it with the community [`hashicorp/tls`](https://registry.terraform.io/providers/hashicorp/tls/latest) provider's `tls_private_key` + `tls_cert_request` resources if you want Terraform to manage CSR generation too, or supply a CSR generated entirely outside Terraform.

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
