output "databases" {
  value = var.databases
}

output "created_via" {
  description = "How the databases were created; recorded because it decides what privileges the master account then holds"
  value       = "ncp csp api (${var.engine})"
}

output "owner" {
  description = "PostgreSQL only: the owner the CSP set at creation time"
  value       = local.is_postgres ? var.owner : ""
}
