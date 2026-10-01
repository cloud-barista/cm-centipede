# Output names are shared with tofu/aws/rdbms so lib/tofu.sh reads one shape for
# both CSPs. host is the public domain and stays null until it is issued from
# the NCP console; check-matrix-ncp.sh waits for that, then refreshes.
output "host" {
  description = "Connection host = public domain (null until issued in the console)"
  value       = local.public_domain
}

output "port" {
  value = local.port
}

output "username" {
  value = var.db_username
}

output "database" {
  description = "Initial database. MongoDB has no such argument, so it is reported as empty there"
  value       = local.is_mongo ? "" : var.db_name
}

output "engine" {
  value = var.engine
}

output "engine_version" {
  value = var.engine_version
}

output "status" {
  value = try(local.master.server_status, "")
}

output "public_access" {
  description = "Whether a public domain exists; the matrix reads it the same way it reads publicAccess on AWS"
  value       = local.public_domain != null
}

output "csp_name" {
  value = local.instance_name
}

output "csp_id" {
  value = try(local.master.server_instance_no, "")
}

output "private_domain" {
  description = "Reachable only from inside the VPC; shown when the public domain is missing so the two are not confused"
  value       = try(local.master.private_domain, "")
}

output "public_subnet" {
  description = "Whether the server sits in a PUBLIC subnet, a prerequisite for requesting a public domain"
  value       = local.public_subnet
}

output "acg_no" {
  description = "The ACG the inbound rule was attached to"
  value       = local.acg_no
}

# Reported so an unreachable endpoint can be told apart from a rule attached to
# the wrong group. The apply's precondition already refuses to continue when
# there is more than one, but the numbers are worth having in the log either way.
output "acg_no_list" {
  description = "Every ACG the instance is associated with"
  value       = local.acg_no_list
}

output "secure_transport" {
  description = "Reported for symmetry with the AWS module, which can turn the CSP default off; on NCP the default is left as it is"
  value       = "csp-default"
}

# The DB-service instance number, which is NOT the server instance number in
# csp_id. NCP's per-database APIs (ncloud_mysql_databases,
# ncloud_postgresql_databases) address the service, and the matrix needs it to
# create a cell's target database - on NCP the master account cannot run
# CREATE DATABASE itself, so tofu/ncp-target goes through the CSP API instead.
output "instance_no" {
  description = "The managed-DB service instance number, for the per-database CSP APIs"
  value = (
    local.is_mysql ? try(ncloud_mysql.this[0].id, "") : (
      local.is_postgres ? try(ncloud_postgresql.this[0].id, "") :
      try(ncloud_mongodb.this[0].id, "")
    )
  )
}
