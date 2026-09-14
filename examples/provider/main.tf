terraform {
  required_providers {
    harbour = {
      source  = "novec-tech-io/harbour"
      version = "~> 0.8"
    }
  }
}

provider "harbour" {
  # eu-west-1 is Harbour's default region, but any AWS region is supported —
  # substitute your deployment's actual region below.
  endpoint = "https://<api-id>.execute-api.eu-west-1.amazonaws.com"
  region   = "eu-west-1"
  role_arn = "arn:aws:iam::<account-id>:role/harbour-customer-prod"
}
