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
  value       = var.aws_data_path
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
