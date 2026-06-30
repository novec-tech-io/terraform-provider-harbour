terraform {
  required_providers {
    harbour = {
      source  = "novec-tech-io/harbour"
      version = "~> 0.1"
    }
  }
}

provider "harbour" {
  endpoint = "https://<api-id>.execute-api.eu-west-1.amazonaws.com"
  region   = "eu-west-1"
  role_arn = "arn:aws:iam::<account-id>:role/harbour-customer-prod"
}
