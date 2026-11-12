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
  description = "Path for the filesystem migration test data (gendata basePath). Empty picks the OS default: /home/ubuntu/testdata on ubuntu (its login account), C:/testdata on windows"
  type        = string
  default     = ""
}

# --- OS and the servers installed on it ---------------------------------------

variable "aws_vm_os" {
  description = "VM operating system: ubuntu (Ubuntu 22.04) or windows (Windows Server 2022)"
  type        = string
  default     = "ubuntu"

  validation {
    condition     = contains(["ubuntu", "windows"], var.aws_vm_os)
    error_message = "aws_vm_os must be ubuntu or windows."
  }
}

variable "aws_vm_servers" {
  description = "Servers installed natively on the VM (no Docker), each reachable from allowed_cidr: minio, mysql, mariadb, postgresql, mongodb"
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for s in var.aws_vm_servers : contains(["minio", "mysql", "mariadb", "postgresql", "mongodb"], s)])
    error_message = "aws_vm_servers may only hold minio, mysql, mariadb, postgresql and mongodb."
  }
}

# The one account every installed server is given: a remote user with full rights
# on mysql/mariadb, a SUPERUSER role on postgresql, a root user in mongodb's admin
# database, and the MinIO root user.
variable "aws_vm_server_username" {
  description = "Account created on every installed server (also the MinIO root user)"
  type        = string
  default     = "dbadmin"

  validation {
    condition = (
      can(regex("^[a-z][a-z0-9_]{2,31}$", var.aws_vm_server_username))
      && !contains(["admin", "root", "postgres", "mysql", "mariadb", "minio"], var.aws_vm_server_username)
    )
    error_message = "aws_vm_server_username must be 3-32 lowercase letters, digits or _, start with a letter, and not be admin, root, postgres, mysql, mariadb or minio."
  }
}

# It is written into SQL, bash, PowerShell, YAML and XML on the VM, so the
# characters that would need quoting in any of them are refused rather than
# escaped five different ways.
#
# Normally empty here: register-creds.sh moves it from .env into OpenBao
# (secret/vm/aws) and blanks it, and main.tf reads it from there. A value that
# does arrive (--keep-env) wins over the stored one.
variable "aws_vm_server_password" {
  description = "Password of aws_vm_server_username on every installed server. Empty reads it from OpenBao secret/vm/aws"
  type        = string
  default     = ""
  sensitive   = true

  validation {
    condition = var.aws_vm_server_password == "" || (
      length(var.aws_vm_server_password) >= 8
      && !can(regex("['\"`\\\\$\\s]", var.aws_vm_server_password))
      && lower(trimspace(var.aws_vm_server_password)) != "changeme"
    )
    error_message = "aws_vm_server_password must not be ChangeMe, must be at least 8 characters (MinIO's minimum) and contain none of ' \" ` \\ $ or whitespace."
  }
}

variable "aws_vm_db_name" {
  description = "Database created on mysql, mariadb and postgresql (mongodb creates it on first write)"
  type        = string
  default     = "testdb"

  validation {
    condition     = can(regex("^[a-z][a-z0-9_]{0,62}$", var.aws_vm_db_name))
    error_message = "aws_vm_db_name must be lowercase letters, digits or _, starting with a letter, at most 63 characters."
  }
}

variable "aws_vm_minio_bucket" {
  description = "Bucket created on MinIO"
  type        = string
  default     = "testbucket"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.aws_vm_minio_bucket))
    error_message = "aws_vm_minio_bucket must be 3-63 lowercase letters, digits or hyphens, starting and ending with a letter or digit."
  }
}

# --- Server versions -------------------------------------------------------------
# Full versions: they name the download files. Every default was checked to be
# downloadable.

variable "aws_vm_mysql_version" {
  description = "MySQL version (official tarball on ubuntu, ZIP on windows)"
  type        = string
  default     = "8.4.6"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+$", var.aws_vm_mysql_version))
    error_message = "aws_vm_mysql_version must be a full version such as 8.4.6."
  }
}

variable "aws_vm_mariadb_version" {
  description = "MariaDB version (official tarball on ubuntu, ZIP on windows)"
  type        = string
  default     = "10.11.13"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+$", var.aws_vm_mariadb_version))
    error_message = "aws_vm_mariadb_version must be a full version such as 10.11.13."
  }
}

variable "aws_vm_postgres_version" {
  description = "PostgreSQL version. ubuntu installs the latest minor of its major from the PGDG apt repository; windows installs exactly this EDB build"
  type        = string
  default     = "14.19"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+$", var.aws_vm_postgres_version))
    error_message = "aws_vm_postgres_version must be major.minor, such as 14.19."
  }
}

variable "aws_vm_mongodb_version" {
  description = "MongoDB version. ubuntu installs the latest of its major.minor series from the official apt repository; windows installs exactly this MSI"
  type        = string
  default     = "7.0.24"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+\\.[0-9]+$", var.aws_vm_mongodb_version))
    error_message = "aws_vm_mongodb_version must be a full version such as 7.0.24."
  }
}

variable "aws_vm_minio_release" {
  description = "MinIO server release, downloaded from its GitHub releases. RELEASE.2025-09-07T16-13-09Z is the last community release that ships binaries"
  type        = string
  default     = "RELEASE.2025-09-07T16-13-09Z"

  validation {
    condition     = can(regex("^RELEASE\\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}Z$", var.aws_vm_minio_release))
    error_message = "aws_vm_minio_release must look like RELEASE.2025-09-07T16-13-09Z."
  }
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
