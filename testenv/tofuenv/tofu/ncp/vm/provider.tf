terraform {
  required_version = ">= 1.6"
  required_providers {
    ncloud = { source = "NaverCloudPlatform/ncloud", version = "~> 4.0" }
    vault  = { source = "hashicorp/vault", version = "~> 4.0" }
    tls    = { source = "hashicorp/tls", version = "~> 4.0" }
    local  = { source = "hashicorp/local", version = "~> 2.4" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}

provider "vault" {}

data "vault_kv_secret_v2" "ncp" {
  mount = "secret"
  name  = "csp/ncp"
}

provider "ncloud" {
  access_key  = data.vault_kv_secret_v2.ncp.data["NCP_ACCESS_KEY"]
  secret_key  = data.vault_kv_secret_v2.ncp.data["NCP_SECRET_KEY"]
  region      = var.ncp_region
  site        = "public"
  support_vpc = true
}
