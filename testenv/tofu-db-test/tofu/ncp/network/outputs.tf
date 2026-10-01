output "vpc_no" {
  value = ncloud_vpc.this.vpc_no
}

output "vpc_name" {
  description = "Read by lib/tofu.sh to tell whether the network was created under the current name prefix"
  value       = ncloud_vpc.this.name
}

output "subnet_no" {
  value = ncloud_subnet.public.subnet_no
}

output "subnet_name" {
  value = ncloud_subnet.public.name
}

output "zone" {
  value = ncloud_subnet.public.zone
}
