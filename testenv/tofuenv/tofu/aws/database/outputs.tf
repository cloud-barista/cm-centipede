output "db_name" {
  value = var.aws_db_name
}

output "db_username" {
  value = var.aws_db_username
}

output "db_password" {
  description = "DB master password (read with: tofu output -raw db_password)"
  value       = local.db_password
  sensitive   = true
}

# An engine left out of var.aws_db_engines has no instance, and its outputs are
# null. gen-data.sh and conn-info.sh read a null host as "not provisioned".

# --- MySQL ---
output "mysql_host" {
  value = one(aws_db_instance.mysql[*].address)
}
output "mysql_port" {
  value = one(aws_db_instance.mysql[*].port)
}
output "mysql_connection_uri" {
  value = one([for i in aws_db_instance.mysql :
    "mysql://${var.aws_db_username}:${local.db_password}@${i.address}:${i.port}/${var.aws_db_name}"
  ])
  sensitive = true
}

# --- MariaDB ---
output "mariadb_host" {
  value = one(aws_db_instance.mariadb[*].address)
}
output "mariadb_port" {
  value = one(aws_db_instance.mariadb[*].port)
}
output "mariadb_connection_uri" {
  value = one([for i in aws_db_instance.mariadb :
    "mysql://${var.aws_db_username}:${local.db_password}@${i.address}:${i.port}/${var.aws_db_name}"
  ])
  sensitive = true
}

# --- PostgreSQL ---
output "postgres_host" {
  value = one(aws_db_instance.postgres[*].address)
}
output "postgres_port" {
  value = one(aws_db_instance.postgres[*].port)
}
output "postgres_connection_uri" {
  value = one([for i in aws_db_instance.postgres :
    "postgresql://${var.aws_db_username}:${local.db_password}@${i.address}:${i.port}/${var.aws_db_name}"
  ])
  sensitive = true
}

# --- Transport security ---
output "secure_transport" {
  description = "Which mode the module ran in, so a working connection is read against the right conditions"
  value       = var.aws_secure_transport
}

output "parameter_groups" {
  description = "The plaintext parameter groups, empty when running against RDS defaults"
  value = { for engine, groups in {
    mysql    = aws_db_parameter_group.mysql
    mariadb  = aws_db_parameter_group.mariadb
    postgres = aws_db_parameter_group.postgres
  } : engine => groups[0].name if length(groups) > 0 }
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.aws_name_prefix
}
