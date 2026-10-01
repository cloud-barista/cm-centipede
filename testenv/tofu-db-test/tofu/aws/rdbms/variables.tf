variable "aws_region" {
  description = "AWS region the managed instance is created in"
  type        = string
  default     = "ap-northeast-2"
}

variable "name_prefix" {
  description = "Resource name prefix, short for centipede tofu matrix"
  type        = string
  default     = "cptfm"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.name_prefix))
    error_message = "Prefix must be 2-10 characters of lowercase letters, digits and hyphens, starting with a lowercase letter."
  }
}

# One column of the matrix = one instance, so engine and version are the two
# variables that change between applies. Each value pair gets its own tofu
# workspace, which keeps the state of a column separate from its neighbours.
variable "engine" {
  description = "RDS engine: mysql, mariadb or postgres (transx-ex calls the last one postgresql)"
  type        = string

  validation {
    condition     = contains(["mysql", "mariadb", "postgres"], var.engine)
    error_message = "engine must be one of mysql, mariadb, postgres."
  }
}

variable "engine_version" {
  description = "Engine version. RDS accepts both a prefix (8.0) and a full version (8.0.42)"
  type        = string
}

variable "db_name" {
  description = "Initial database created with the instance; the matrix connects to it to create and drop each cell's target database"
  type        = string
  default     = "matrixadm"
}

variable "db_username" {
  description = "Master username. PostgreSQL rejects reserved names such as admin and rdsadmin"
  type        = string
  default     = "dbadmin"
}

variable "db_instance_class" {
  description = "RDS instance class"
  type        = string
  default     = "db.t3.micro"
}

variable "db_allocated_storage" {
  description = "RDS allocated storage in GB"
  type        = number
  default     = 20
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach the engine port; the matrix host has to be inside it"
  type        = string
  default     = "0.0.0.0/0"
}

# Why this exists at all: RDS turns TLS on by default for some engine versions -
# MariaDB 11.8 and later set require_secure_transport=ON, and PostgreSQL sets
# rds.force_ssl=1 - so a client that insists on plaintext cannot connect.
#
#   csp-default  leave the CSP default alone (default)
#   off          attach a parameter group that allows plaintext
#
# csp-default is the default because transx-ex now selects transport security
# per connection (DirectConfig.TLSMode), and the matrix's own clients follow the
# same setting. At TARGET_TLS_MODE=prefer, its default, a column runs whether or
# not the instance demands TLS, so there is nothing left to turn off.
#
# What `off` is still for: seeing what happens when TLS is not available at all.
# The matrix records which mode a run used, so a PASS obtained with the
# parameter turned off is not mistaken for one against CSP defaults.
variable "secure_transport" {
  description = "csp-default = leave the CSP default alone; off = attach a parameter group allowing plaintext"
  type        = string
  default     = "csp-default"

  validation {
    condition     = contains(["off", "csp-default"], var.secure_transport)
    error_message = "secure_transport must be off or csp-default."
  }
}
