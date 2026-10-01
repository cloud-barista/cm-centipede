terraform {
  required_version = ">= 1.6"
  required_providers {
    ncloud = { source = "NaverCloudPlatform/ncloud", version = "~> 4.0" }
    vault  = { source = "hashicorp/vault", version = "~> 4.0" }
  }
}

provider "vault" {}

# NCP credentials (secret/csp/ncp)
data "vault_kv_secret_v2" "ncp" {
  mount = "secret"
  name  = "csp/ncp"
}

# DB master password (secret/db/ncp)
data "vault_kv_secret_v2" "db" {
  mount = "secret"
  name  = "db/ncp"
}

provider "ncloud" {
  access_key  = data.vault_kv_secret_v2.ncp.data["NCP_ACCESS_KEY"]
  secret_key  = data.vault_kv_secret_v2.ncp.data["NCP_SECRET_KEY"]
  region      = var.ncp_region
  site        = "public"
  support_vpc = true
}
