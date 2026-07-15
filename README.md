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
      version = "~> 0.6"
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
| `export_to_acm` | No | Export the issued certificate to ACM in your AWS account. Requires ACM export to be configured for your tenant (see [ACM export](#acm-export) below). Defaults to `false`. Conflicts with `csr` |
| `csr` | No | PEM-encoded Certificate Signing Request — Harbour signs your public key instead of generating a private key server-side (see [CSR support](#csr-support) below). Conflicts with `export_to_acm` and `alt_names` |

#### Attributes

| Attribute | Description |
|-----------|-------------|
| `id` | Same as `request_id` |
| `request_id` | Harbour request ID |
| `serial_number` | Certificate serial number |
| `secret_arn` | Secrets Manager ARN containing the certificate material |
| `expiry_timestamp` | Certificate expiry as a Unix timestamp |
| `status` | Current status: `requested`, `issuing`, `issued`, `revoked`, `expired`, `failed` |
| `acm_certificate_arn` | ARN of the certificate exported to ACM in your account. Only set when `export_to_acm` is `true` |

---

### `harbour_certificate_import`

Registers a certificate that already lives in ACM (however it got there — including a previous `harbour_certificate` with `export_to_acm = true`, or something imported entirely outside Harbour) for Harbour lifecycle tracking, without submitting a PEM or Harbour ever holding the private key. See [ACM import](#acm-import) below.

All arguments are immutable — any change forces replacement.

```hcl
resource "harbour_certificate_import" "existing" {
  acm_certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/abc-123"
}
```

#### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `acm_certificate_arn` | Yes | ARN of an existing ACM certificate in your account. Its ACM `Type` must be `IMPORTED` — an `AMAZON_ISSUED` cert can't be registered this way |
| `auto_renew` | No | Whether Harbour proactively renews this certificate ahead of expiry. Defaults to the tenant `default_auto_renew` config value |

#### Attributes

| Attribute | Description |
|-----------|-------------|
| `id` | Same as `request_id` |
| `request_id` | Harbour request ID |
| `cn` | Common name, read from the ACM certificate's `DomainName` at registration time |
| `sans` | Subject alternative names, read from the ACM certificate |
| `serial_number` | `null` until this certificate's first Harbour-managed renewal — Harbour never signed the originally-imported material |
| `secret_arn` | `null` until the first renewal, same reasoning as `serial_number` |
| `expiry_timestamp` | Certificate expiry as a Unix timestamp, read from ACM at registration time |
| `status` | Current status: `issued`, `renewing`, `revoked`, `expired` |
| `issuance_method` | Always `"imported"` |

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

### `harbour_ca_certificates`

Reads your Harbour deployment's CA certificates, for distributing the trust anchor to wherever your certificates are validated. Takes no arguments.

```hcl
data "harbour_ca_certificates" "this" {}

resource "local_file" "harbour_root_ca" {
  content  = data.harbour_ca_certificates.this.root_pem
  filename = "/etc/pki/ca-trust/source/anchors/harbour-root-ca.pem"
}
```

#### Attributes

| Attribute | Description |
|-----------|-------------|
| `roots` | List of PEM-encoded root CA certificates — install these in trust stores |
| `intermediates` | List of PEM-encoded intermediate CA certificates |
| `root_pem` | First element of `roots` — convenience scalar for the common single-root case |
| `intermediate_pem` | First element of `intermediates` |
| `chain_pem` | Full CA chain as concatenated PEM, intermediate first, root last |

**Anchor trust on the root, not the intermediate.** The list attributes hold a single element today, but stay lists deliberately: during a CA rotation or bring-your-own-root migration window Harbour may publish two overlapping anchors, and a trust store built from `roots` picks both up without a schema change.

---

## ACM export

Setting `export_to_acm = true` exports the issued certificate to ACM in your AWS account, exposing a usable `acm_certificate_arn` you can wire directly into AWS resources:

```hcl
resource "harbour_certificate" "api" {
  common_name   = "api.example.internal"
  ttl           = "90d"
  export_to_acm = true
}

resource "aws_lb_listener" "https" {
  certificate_arn = harbour_certificate.api.acm_certificate_arn
  # ...
}
```

This requires a one-time setup in your AWS account: an IAM role trusting Harbour's certificate-issuance **and** revocation Lambdas, granting `acm:ImportCertificate`, `acm:AddTagsToCertificate`, and `acm:DeleteCertificate`. Without this role configured for your tenant, `export_to_acm = true` fails with "ACM export is not configured for this tenant". Contact Novec to enable it.

On renewal, the certificate is re-exported onto the same ACM ARN, so listeners and other references never need to change. On revocation (including `terraform destroy`), Harbour also deletes the certificate from your ACM — best-effort: if the ACM certificate is still attached to a resource (e.g. a load balancer listener you haven't updated yet), the Harbour-side revoke still succeeds and the ACM cleanup is retried automatically until it succeeds. This detail isn't currently surfaced as a provider attribute (the revoke API response has an `acm_cleanup_status` field, but the provider doesn't read or expose it today) — if you need to confirm cleanup succeeded, check the certificate directly in ACM.

---

## ACM import

The reverse direction of ACM export: if you already have a certificate sitting in ACM — imported by some earlier process, a migration off another CA, or a previous `harbour_certificate` with `export_to_acm = true` — `harbour_certificate_import` registers it for Harbour lifecycle tracking without submitting a PEM or handing over a private key (Harbour never gets one — ACM's own API never returns it either, for `AMAZON_ISSUED` or `IMPORTED` certs alike).

```hcl
resource "harbour_certificate_import" "existing" {
  acm_certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/abc-123"
}
```

**No rotation at registration time.** The ACM object is left completely untouched until your tenant's ordinary renewal window comes around, at which point Harbour reissues and re-exports onto the *same* ARN — the identical mechanism `export_to_acm` renewal already uses. This gives you the full renewal window to get Harbour's CA chain trusted wherever the certificate is validated, before anything about the actual material changes.

Requires the same one-time cross-account IAM role as ACM export (see above), with two additional read-only permissions: `acm:DescribeCertificate` and `acm:GetCertificate`.

**Revocation before the first renewal can only delete the ACM object** — Harbour never signed this certificate, so there's no CA-side serial to revoke via the certificate authority. `terraform destroy` (or removing the resource) still works and still deletes the ACM certificate; there's just no corresponding CA revocation until after the first renewal makes it a normal Harbour-issued certificate.

**An ARN can only be registered once.** If it's already tracked by another active Harbour record — most likely a `harbour_certificate` with `export_to_acm = true` that issued it in the first place — `apply` fails with a `409` from the API. Revoke or wait for that other record to renew first.

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

`csr` also conflicts with `export_to_acm` — ACM's `ImportCertificate` API requires the private key as an input, which Harbour never has for a CSR-issued certificate.

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
