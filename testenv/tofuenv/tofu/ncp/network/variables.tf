variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "ncp_zone" {
  description = "NCP zone code (the zone the subnet is physically placed in)"
  type        = string
  default     = "KR-2"
}

variable "ncp_name_prefix" {
  description = "Resource name prefix, short for centipede tofu. Kept short because the managed MongoDB service_name is limited to 15 characters"
  type        = string
  default     = "cptf"
}

variable "ncp_vpc_cidr" {
  description = "VPC IPv4 CIDR. Must be a /16-/28 block inside a private range (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)"
  type        = string
  default     = "10.10.0.0/16"
}

variable "allowed_cidr" {
  description = "CIDR allowed for ACG inbound rules; wide open by default"
  type        = string
  default     = "0.0.0.0/0"
}
