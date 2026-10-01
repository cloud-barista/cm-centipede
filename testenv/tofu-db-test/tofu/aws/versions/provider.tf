terraform {
  required_version = ">= 1.6"
  required_providers {
    aws   = { source = "hashicorp/aws", version = "~> 5.0" }
    vault = { source = "hashicorp/vault", version = "~> 4.0" }
  }
}

provider "vault" {}

data "vault_kv_secret_v2" "aws" {
  mount = "secret"
  name  = "csp/aws"
}

provider "aws" {
  region     = var.aws_region
  access_key = data.vault_kv_secret_v2.aws.data["AWS_ACCESS_KEY_ID"]
  secret_key = data.vault_kv_secret_v2.aws.data["AWS_SECRET_ACCESS_KEY"]
}
