variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-2"
}

variable "aws_db_instance_class" {
  description = "RDS instance class"
  type        = string
  default     = "db.t3.micro"
}

variable "aws_db_name" {
  description = "Initial database name"
  type        = string
  default     = "testdb"
}

variable "aws_db_username" {
  description = "DB master username (PostgreSQL rejects reserved names such as admin/rdsadmin, so dbadmin is recommended)"
  type        = string
  default     = "dbadmin"
}

variable "allowed_cidr" {
  description = "CIDR allowed for inbound DB ports and SSH; wide open by default"
  type        = string
  default     = "0.0.0.0/0"
}

variable "aws_name_prefix" {
  description = "Resource name prefix, short for centipede tofu; must match the vm module"
  type        = string
  default     = "cptf"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,19}$", var.aws_name_prefix))
    error_message = "Prefix must be 2-20 characters of lowercase letters, digits and hyphens, starting with a lowercase letter (an RDS identifier is capped at 63 and gets a -postgres suffix here)."
  }
}

variable "aws_db_allocated_storage" {
  description = "RDS allocated storage in GB"
  type        = number
  default     = 20
}

# DB engine versions, set through .env (TF_VAR_*).
# Defaults: mysql 8.0 / mariadb 10.6 / postgres 14.
# The versions RDS supports change over time; check with:
#   aws rds describe-db-engine-versions --engine <engine> --region <region>
variable "aws_mysql_version" {
  description = "RDS MySQL engine version. Supported by RDS: 5.7, 8.0, 8.4"
  type        = string
  default     = "8.0"
}

variable "aws_mariadb_version" {
  description = "RDS MariaDB engine version. Supported by RDS: 10.5, 10.6, 10.11, 11.4, 11.8 (11.8+ defaults to require_secure_transport=ON, i.e. TLS required)"
  type        = string
  default     = "10.6"
}

variable "aws_postgres_version" {
  description = "RDS PostgreSQL engine version. Supported by RDS: 11, 12, 13, 14, 15, 16, 17, 18"
  type        = string
  default     = "14"
}

# ---------------------------------------------------------------------------
# Plaintext client connections
#
# Why this exists: RDS turns TLS on by default for some engine versions, and a
# client that insists on plaintext then cannot connect at all.
#   mysql       never enforced by default
#   mariadb     require_secure_transport=ON from 11.8
#   postgres    rds.force_ssl=1 from 15
# With this module's default versions (8.0 / 10.6 / 14) none of the three
# enforce it, so the knob only starts to matter once a version is raised.
#
#   csp-default  leave the RDS default alone (default)
#   off          attach a parameter group that allows plaintext
#
# csp-default is the default because a PASS obtained with the parameter turned
# off does not answer "does this work against a stock managed instance". `off`
# is the escape hatch for seeing what changes when TLS is not available at all.
#
# This value is fixed at creation time: see the note on the parameter groups in
# main.tf. Changing it on a provisioned module means destroy + provision.
variable "aws_secure_transport" {
  description = "csp-default = leave the RDS default alone; off = attach a parameter group allowing plaintext"
  type        = string
  default     = "csp-default"

  validation {
    condition     = contains(["off", "csp-default"], var.aws_secure_transport)
    error_message = "aws_secure_transport must be off or csp-default."
  }
}
