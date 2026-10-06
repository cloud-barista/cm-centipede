variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "ncp_name_prefix" {
  description = "Resource name prefix, short for centipede tofu. Must match the network module, since the VPC, subnet and ACG are looked up by these names"
  type        = string
  default     = "cptf"
}

variable "ncp_server_image_name" {
  description = "Server image name (KVM). List them with ./scripts/ncp-db-versions.sh server"
  type        = string
  default     = "ubuntu-22.04-base"
}

variable "ncp_server_spec_code" {
  description = "Server spec code. List them with ./scripts/ncp-db-versions.sh server"
  type        = string
  default     = "s2-g3"
}

# The boot disk is the only disk the VM has, and the filesystem test data lands on
# it, so it has to hold the whole dummy total. NCP's own default is 10 GB.
variable "ncp_vm_volume_size" {
  description = "Boot disk size in GB (NCP default: 10). Fixed at creation: changing it on a provisioned VM re-creates the VM"
  type        = number
  default     = 50

  validation {
    condition     = var.ncp_vm_volume_size >= 10 && floor(var.ncp_vm_volume_size) == var.ncp_vm_volume_size
    error_message = "ncp_vm_volume_size must be a whole number of GB, 10 or more."
  }
}

variable "ssh_key_dir" {
  description = "Directory where the generated SSH private key (pem) is stored (container path)"
  type        = string
  default     = "/work/ssh_keys"
}

variable "ncp_data_path" {
  description = "Path for the filesystem migration test data (gendata basePath). NCP images log in as root, so it lives under /root"
  type        = string
  default     = "/root/testdata"
}
