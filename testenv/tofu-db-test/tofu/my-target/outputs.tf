output "databases" {
  value = [for d in mysql_database.target : d.name]
}

output "transport" {
  description = "How this module connected, which is fixed and independent of TARGET_TLS_MODE - see provider.tf"
  value       = "best available (TLS unverified when the server offers it, plaintext otherwise)"
}
