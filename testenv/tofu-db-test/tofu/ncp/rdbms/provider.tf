terraform {
  required_version = ">= 1.6"
  required_providers {
    ncloud = { source = "NaverCloudPlatform/ncloud", version = "~> 4.0" }
    vault  = { source = "hashicorp/vault", version = "~> 4.0" }
  }
}

# The vault provider reads VAULT_ADDR / VAULT_TOKEN from the environment.
provider "vault" {}

data "vault_kv_secret_v2" "ncp" {
  mount = "secret"
  name  = "csp/ncp"
}

# The managed-DB master password. Nothing sensitive is passed in as a variable:
# the matrix never has to hold it to run an apply.
data "vault_kv_secret_v2" "db" {
  mount = "secret"
  name  = "db/ncp"
}

provider "ncloud" {
  access_key  = data.vault_kv_secret_v2.ncp.data["NCP_ACCESS_KEY"]
  secret_key  = data.vault_kv_secret_v2.ncp.data["NCP_SECRET_KEY"]
  region      = var.ncp_region
  site        = "public"
  support_vpc = true # Required: the provider only supports the VPC environment.
}
