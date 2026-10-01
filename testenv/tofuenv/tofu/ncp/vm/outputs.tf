output "instance_no" {
  description = "Server instance ID"
  value       = ncloud_server.vm.instance_no
}

output "server_name" {
  value = ncloud_server.vm.name
}

output "public_ip" {
  description = "Public IP of the VM (gendata uses it as the SFTP target)"
  value       = ncloud_public_ip.vm.public_ip
}

output "private_ip" {
  value = ncloud_server.vm.private_ip
}

output "ssh_user" {
  description = "SSH account (NCP-provided images use root)"
  value       = local.ssh_user
}

output "key_file" {
  description = "SSH private key path, relative to the tofuenv directory"
  value       = local.host_key_path
}

# The name carries a random suffix, so this is the only way to tell which key in the
# NCP console belongs to this VM.
output "login_key_name" {
  description = "NCP login key name"
  value       = ncloud_login_key.vm.key_name
}

output "ssh_command" {
  description = "Ready-to-use SSH command"
  value       = "ssh -i ${local.host_key_path} ${local.ssh_user}@${ncloud_public_ip.vm.public_ip}"
}

output "data_path" {
  description = "Test data path (gendata filesystem basePath)"
  value       = var.ncp_data_path
}

output "server_image_number" {
  value = local.image_number
}

output "server_spec_code" {
  value = ncloud_server.vm.server_spec_code
}

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
