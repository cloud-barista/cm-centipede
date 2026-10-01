terraform {
  required_version = ">= 1.6"
  required_providers {
    aws   = { source = "hashicorp/aws", version = "~> 5.0" }
    vault = { source = "hashicorp/vault", version = "~> 4.0" }
  }
}

provider "vault" {}

# CSP credentials (secret/csp/aws) and the managed-DB master password
# (secret/db/aws). Nothing sensitive is passed in as a variable: the matrix
# never has to hold a key to run an apply.
data "vault_kv_secret_v2" "aws" {
  mount = "secret"
  name  = "csp/aws"
}

data "vault_kv_secret_v2" "db" {
  mount = "secret"
  name  = "db/aws"
}

provider "aws" {
  region     = var.aws_region
  access_key = data.vault_kv_secret_v2.aws.data["AWS_ACCESS_KEY_ID"]
  secret_key = data.vault_kv_secret_v2.aws.data["AWS_SECRET_ACCESS_KEY"]
}
