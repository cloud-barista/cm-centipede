output "databases" {
  value = [for d in postgresql_database.target : d.name]
}

output "owner" {
  value = var.username
}

output "granted_public" {
  description = "Whether this cell had to grant USAGE and CREATE on schema public; recorded so a PASS is read against the right conditions"
  value       = var.grant_public
}
