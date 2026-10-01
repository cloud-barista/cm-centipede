output "vpc_no" {
  description = "ID of the created VPC"
  value       = ncloud_vpc.this.vpc_no
}

output "vpc_name" {
  value = ncloud_vpc.this.name
}

output "vpc_cidr" {
  value = ncloud_vpc.this.ipv4_cidr_block
}

output "default_network_acl_no" {
  value = ncloud_vpc.this.default_network_acl_no
}

output "subnet_no" {
  description = "ID of the PUBLIC subnet (the vm and database modules look it up by name)"
  value       = ncloud_subnet.public.subnet_no
}

output "subnet_name" {
  value = ncloud_subnet.public.name
}

output "subnet_cidr" {
  value = ncloud_subnet.public.subnet
}

output "subnet_type" {
  value = ncloud_subnet.public.subnet_type
}

output "zone" {
  value = ncloud_subnet.public.zone
}

output "server_acg_no" {
  description = "ID of the ACG used by servers (VMs)"
  value       = ncloud_access_control_group.server.id
}

output "server_acg_name" {
  value = ncloud_access_control_group.server.name
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.ncp_name_prefix
}
