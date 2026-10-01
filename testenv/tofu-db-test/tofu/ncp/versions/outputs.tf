output "region" {
  value = var.ncp_region
}

# version -> image product code(s)
output "mysql_versions" {
  value = local.mysql_versions
}

output "postgresql_versions" {
  value = local.postgresql_versions
}

output "mongodb_versions" {
  value = local.mongodb_versions
}

# The bare version lists, keyed by the engine name the matrix uses, so
# csp-support-versions.sh --write can put them straight into check-matrix.env.
output "suggested_versions" {
  value = {
    mysql      = sort(keys(local.mysql_versions))
    postgresql = sort(keys(local.postgresql_versions))
    mongodb    = sort(keys(local.mongodb_versions))
  }
}
