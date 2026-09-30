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

# --- MySQL ---
output "mysql_host" {
  value = aws_db_instance.mysql.address
}
output "mysql_port" {
  value = aws_db_instance.mysql.port
}
output "mysql_connection_uri" {
  value     = "mysql://${var.aws_db_username}:${local.db_password}@${aws_db_instance.mysql.address}:${aws_db_instance.mysql.port}/${var.aws_db_name}"
  sensitive = true
}

# --- MariaDB ---
output "mariadb_host" {
  value = aws_db_instance.mariadb.address
}
output "mariadb_port" {
  value = aws_db_instance.mariadb.port
}
output "mariadb_connection_uri" {
  value     = "mysql://${var.aws_db_username}:${local.db_password}@${aws_db_instance.mariadb.address}:${aws_db_instance.mariadb.port}/${var.aws_db_name}"
  sensitive = true
}

# --- PostgreSQL ---
output "postgres_host" {
  value = aws_db_instance.postgres.address
}
output "postgres_port" {
  value = aws_db_instance.postgres.port
}
output "postgres_connection_uri" {
  value     = "postgresql://${var.aws_db_username}:${local.db_password}@${aws_db_instance.postgres.address}:${aws_db_instance.postgres.port}/${var.aws_db_name}"
  sensitive = true
}

# --- Transport security ---
output "secure_transport" {
  description = "Which mode the module ran in, so a working connection is read against the right conditions"
  value       = var.aws_secure_transport
}

output "parameter_groups" {
  description = "The plaintext parameter groups, empty when running against RDS defaults"
  value = local.want_parameter_group ? {
    mysql    = aws_db_parameter_group.mysql[0].name
    mariadb  = aws_db_parameter_group.mariadb[0].name
    postgres = aws_db_parameter_group.postgres[0].name
  } : {}
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.aws_name_prefix
}
