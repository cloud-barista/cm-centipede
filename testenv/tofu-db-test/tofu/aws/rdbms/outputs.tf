# Output names are shared with tofu/ncp/rdbms so lib/tofu.sh reads one shape
# for both CSPs.
output "host" {
  value = aws_db_instance.this.address
}

output "port" {
  value = aws_db_instance.this.port
}

output "username" {
  value = var.db_username
}

output "database" {
  description = "Initial database; each cell's target database is created inside the instance separately"
  value       = var.db_name
}

output "engine" {
  value = aws_db_instance.this.engine
}

output "engine_version" {
  description = "The version RDS actually resolved, which for a prefix such as 8.0 is the current minor release"
  value       = aws_db_instance.this.engine_version_actual
}

output "status" {
  value = aws_db_instance.this.status
}

output "public_access" {
  value = aws_db_instance.this.publicly_accessible
}

# What to search for in the console when a destroy fails and the instance has
# to be removed by hand.
output "csp_name" {
  value = aws_db_instance.this.identifier
}

output "csp_id" {
  value = aws_db_instance.this.resource_id
}

output "secure_transport" {
  description = "Which mode the column ran in, recorded so a PASS is read against the right conditions"
  value       = var.secure_transport
}

output "parameter_group" {
  value = length(aws_db_parameter_group.this) > 0 ? aws_db_parameter_group.this[0].name : ""
}
