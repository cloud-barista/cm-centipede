# Engine version -> image product code(s), comma separated when the catalog
# lists more than one product for the same version.
# Copy the key on the left verbatim into TF_VAR_ncp_*_version in .env.
output "mysql_versions" {
  description = "MySQL engine version -> image product code"
  value       = local.mysql_versions
}

output "postgresql_versions" {
  description = "PostgreSQL engine version -> image product code"
  value       = local.postgresql_versions
}

output "mongodb_versions" {
  description = "MongoDB engine version -> image product code"
  value       = local.mongodb_versions
}

# Server images: "<name> (<hypervisor>)" -> image number.
#   TF_VAR_ncp_server_image_name takes only the name before the parenthesis,
#   e.g. ubuntu-22.04-base.
output "server_images" {
  description = "Server image name with hypervisor -> image number"
  value       = local.server_images
}

# Server specs: TF_VAR_ncp_server_spec_code takes only the leading code, e.g. s2-g3.
output "server_specs" {
  description = "Server spec list (code, hypervisor, vCPU, memory)"
  value = [
    for s in data.ncloud_server_specs.all.server_spec_list :
    format("%s (%s, %d vCPU, %d GB)",
      s.server_spec_code,
      s.hypervisor_type,
      s.cpu_count,
      floor(s.memory_size / 1073741824)
    )
  ]
}
