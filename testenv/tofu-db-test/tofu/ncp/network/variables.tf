variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "ncp_zone" {
  description = "NCP zone code the subnet is placed in"
  type        = string
  default     = "KR-2"
}

variable "name_prefix" {
  description = "Resource name prefix, short for centipede tofu matrix"
  type        = string
  default     = "cptfm"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.name_prefix))
    error_message = "Prefix must be 2-10 characters of lowercase letters, digits and hyphens, starting with a lowercase letter (the managed MongoDB service_name is capped at 15)."
  }
}

variable "ncp_vpc_cidr" {
  description = "VPC IPv4 CIDR; a /16-/28 block inside a private range"
  type        = string
  default     = "10.20.0.0/16"
}
