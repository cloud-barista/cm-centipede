# ---------------------------------------------------------------------------
# Common
#   Output names match the AWS module so gendata can read them CSP-agnostically.
#   For the three managed engines, *_host is the public domain, which is null until
#   it is issued from the console. Run ./scripts/ncp-db-domain.sh afterwards to
#   refresh state and fill these in.
# ---------------------------------------------------------------------------
output "db_name" {
  value = var.ncp_db_name
}

output "db_username" {
  value = var.ncp_db_username
}

output "db_password" {
  description = "DB master password (read with: tofu output -raw db_password)"
  value       = local.db_password
  sensitive   = true
}

# --- MySQL (managed) ---
output "mysql_host" {
  description = "MySQL connection host = public domain (null until issued)"
  value       = local.mysql_public_domain
}
output "mysql_public_domain" {
  value = local.mysql_public_domain
}
output "mysql_private_domain" {
  description = "Private domain for access from inside the VPC (unreachable from outside)"
  value       = try(local.mysql_master.private_domain, null)
}
output "mysql_port" {
  value = ncloud_mysql.this.port
}
output "mysql_public_subnet" {
  description = "Whether the server sits in a PUBLIC subnet, a prerequisite for requesting a public domain"
  value       = try(local.mysql_master.is_public_subnet, null)
}
output "mysql_acg_no" {
  value = ncloud_mysql.this.access_control_group_no_list[0]
}
output "mysql_connection_uri" {
  value = local.mysql_public_domain == null ? null : format("mysql://%s:%s@%s:%s/%s",
    var.ncp_db_username, local.db_password, local.mysql_public_domain, ncloud_mysql.this.port, var.ncp_db_name
  )
  sensitive = true
}

# --- PostgreSQL (managed) ---
output "postgres_host" {
  description = "PostgreSQL connection host = public domain (null until issued)"
  value       = local.postgres_public_domain
}
output "postgres_public_domain" {
  value = local.postgres_public_domain
}
output "postgres_private_domain" {
  value = try(local.postgres_primary.private_domain, null)
}
output "postgres_port" {
  value = ncloud_postgresql.this.port
}
output "postgres_public_subnet" {
  description = "Whether the server sits in a PUBLIC subnet (MySQL names it is_public_subnet, PostgreSQL public_subnet)"
  value       = try(local.postgres_primary.public_subnet, null)
}
output "postgres_acg_no" {
  value = ncloud_postgresql.this.access_control_group_no_list[0]
}
output "postgres_connection_uri" {
  value = local.postgres_public_domain == null ? null : format("postgresql://%s:%s@%s:%s/%s",
    var.ncp_db_username, local.db_password, local.postgres_public_domain, ncloud_postgresql.this.port, var.ncp_db_name
  )
  sensitive = true
}

# --- MongoDB (managed, STAND_ALONE) ---
output "mongodb_host" {
  description = "MongoDB connection host = public domain (null until issued)"
  value       = local.mongodb_public_domain
}
output "mongodb_public_domain" {
  value = local.mongodb_public_domain
}
output "mongodb_private_domain" {
  value = try(local.mongodb_server.private_domain, null)
}
output "mongodb_port" {
  value = ncloud_mongodb.this.member_port
}
output "mongodb_acg_no" {
  value = ncloud_mongodb.this.access_control_group_no_list[0]
}
output "mongodb_connection_uri" {
  value = local.mongodb_public_domain == null ? null : format("mongodb://%s:%s@%s:%s/%s?authSource=admin",
    var.ncp_db_username, local.db_password, local.mongodb_public_domain, ncloud_mongodb.this.member_port, var.ncp_db_name
  )
  sensitive = true
}

# --- Network ---
output "vpc_no" {
  value = local.vpc_no
}
output "subnet_no" {
  value = local.subnet_no
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.ncp_name_prefix
}
