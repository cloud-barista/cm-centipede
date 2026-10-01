variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-2"
}

variable "aws_instance_type" {
  description = "EC2 instance type"
  type        = string
  default     = "t3.micro"
}

variable "aws_vm_volume_size" {
  description = "VM root volume size in GB"
  type        = number
  default     = 20
}

variable "allowed_cidr" {
  description = "CIDR allowed for inbound SSH (22); wide open by default"
  type        = string
  default     = "0.0.0.0/0"
}

variable "aws_name_prefix" {
  description = "Resource name prefix, short for centipede tofu"
  type        = string
  default     = "cptf"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,19}$", var.aws_name_prefix))
    error_message = "Prefix must be 2-20 characters of lowercase letters, digits and hyphens, starting with a lowercase letter."
  }
}

variable "ssh_key_dir" {
  description = "Directory where the generated SSH private key (pem) is stored (container path)"
  type        = string
  default     = "/work/ssh_keys"
}

variable "aws_data_path" {
  description = "Path for the filesystem migration test data (gendata basePath). AWS images log in as ubuntu, so it lives under /home/ubuntu"
  type        = string
  default     = "/home/ubuntu/testdata"
}

variable "aws_nfs_enabled" {
  description = "Create an EFS file system and mount it on the VM over NFS at aws_nfs_mount_path"
  type        = bool
  default     = false
}

variable "aws_nfs_mount_path" {
  description = "Where the EFS file system is mounted on the VM. Independent of aws_data_path; ignored unless aws_nfs_enabled"
  type        = string
  default     = "/home/ubuntu/testdata"

  # Mounting over /home/ubuntu hides .ssh/authorized_keys and locks every SSH client
  # out, and mounting over a system directory breaks the OS, so both are refused.
  validation {
    condition = (
      can(regex("^(/[A-Za-z0-9._-]+)+$", var.aws_nfs_mount_path))
      && !can(regex("(^|/)\\.\\.?(/|$)", var.aws_nfs_mount_path))
      && !contains(["/home", "/home/ubuntu"], var.aws_nfs_mount_path)
      && !can(regex("^/home/ubuntu/\\.ssh(/|$)", var.aws_nfs_mount_path))
      && !can(regex("^/(bin|boot|dev|etc|lib|lib32|lib64|libx32|proc|root|run|sbin|snap|sys|usr|var)(/|$)", var.aws_nfs_mount_path))
    )
    error_message = "aws_nfs_mount_path must be an absolute path without . or .. segments or a trailing slash, and must not be /, /home, /home/ubuntu, /home/ubuntu/.ssh or a system directory (/etc, /usr, /var, ...)."
  }
}
