output "instance_id" {
  value = aws_instance.vm.id
}

output "public_ip" {
  description = "Public IP of the VM"
  value       = aws_instance.vm.public_ip
}

output "ssh_user" {
  value = local.ssh_user
}

output "key_file" {
  description = "SSH private key path, relative to the tofuenv directory"
  value       = local.host_key_path
}

output "ssh_command" {
  description = "Ready-to-use SSH command"
  value       = "ssh -i ${local.host_key_path} ${local.ssh_user}@${aws_instance.vm.public_ip}"
}

output "data_path" {
  description = "Test data path (gendata filesystem basePath)"
  value       = local.data_path
}

output "vm_os" {
  description = "ubuntu or windows"
  value       = var.aws_vm_os
}

output "admin_password" {
  description = "windows: the Administrator password, for RDP (read with: tofu output -raw admin_password); null on ubuntu"
  value       = local.is_windows ? rsadecrypt(aws_instance.vm.password_data, tls_private_key.vm.private_key_pem) : null
  sensitive   = true
}

output "rdp_address" {
  description = "windows: host:port for an RDP client, login Administrator; null on ubuntu"
  value       = local.is_windows ? "${aws_instance.vm.public_ip}:3389" : null
}

# --- Servers installed on the VM ---
# Named like the database module's outputs, so gen-data.sh reads both the same way.
# A server left out of aws_vm_servers has null outputs, which reads as "not there".

output "vm_servers" {
  description = "Servers installed on the VM"
  value       = local.servers
}

output "server_username" {
  description = "Account on every installed server (also the MinIO root user); null without servers"
  value       = length(local.servers) > 0 ? var.aws_vm_server_username : null
}

output "server_password" {
  description = "Its password (read with: tofu output -raw server_password)"
  value       = length(local.servers) > 0 ? local.server_password : null
  sensitive   = true
}

output "db_name" {
  description = "Database on the installed engines: created on mysql, mariadb and postgresql; mongodb creates it on first write"
  value       = length(setintersection(local.servers, ["mysql", "mariadb", "postgresql", "mongodb"])) > 0 ? var.aws_vm_db_name : null
}

output "mysql_host" {
  value = contains(local.servers, "mysql") ? aws_instance.vm.public_ip : null
}
output "mysql_port" {
  value = contains(local.servers, "mysql") ? 3306 : null
}

output "mariadb_host" {
  value = contains(local.servers, "mariadb") ? aws_instance.vm.public_ip : null
}
output "mariadb_port" {
  value = contains(local.servers, "mariadb") ? 3307 : null
}

output "postgres_host" {
  value = contains(local.servers, "postgresql") ? aws_instance.vm.public_ip : null
}
output "postgres_port" {
  value = contains(local.servers, "postgresql") ? 5432 : null
}

output "mongodb_host" {
  value = contains(local.servers, "mongodb") ? aws_instance.vm.public_ip : null
}
output "mongodb_port" {
  value = contains(local.servers, "mongodb") ? 27017 : null
}

output "minio_endpoint" {
  description = "MinIO S3 API (plain http)"
  value       = contains(local.servers, "minio") ? "http://${aws_instance.vm.public_ip}:9000" : null
}
output "minio_console_url" {
  value = contains(local.servers, "minio") ? "http://${aws_instance.vm.public_ip}:9001" : null
}
output "minio_bucket" {
  value = contains(local.servers, "minio") ? var.aws_vm_minio_bucket : null
}

output "nfs_enabled" {
  description = "Whether an EFS file system is mounted on the VM"
  value       = var.aws_nfs_enabled
}

output "nfs_file_system_id" {
  description = "EFS file system ID; null without NFS"
  value       = one(aws_efs_file_system.nfs[*].id)
}

output "nfs_dns_name" {
  description = "EFS DNS name, resolvable inside the VPC only; null without NFS"
  value       = one(aws_efs_file_system.nfs[*].dns_name)
}

output "nfs_mount_path" {
  description = "Where the EFS file system is mounted on the VM; null without NFS"
  value       = var.aws_nfs_enabled ? var.aws_nfs_mount_path : null
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.aws_name_prefix
}
