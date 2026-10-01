variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "name_prefix" {
  description = "Resource name prefix. Short on purpose - see the naming note in main.tf: a managed MongoDB service_name is capped at 15 characters"
  type        = string
  default     = "cptfm"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,5}$", var.name_prefix))
    error_message = "Prefix must be 2-6 characters of lowercase letters and digits, starting with a letter. Hyphens are left out so the generated instance name keeps exactly one, and 6 is the most that fits MongoDB's 15-character service_name."
  }
}

# One column of the matrix = one instance. These two are what changes between
# applies; each pair gets its own tofu workspace.
variable "engine" {
  description = "Managed engine: mysql, postgresql or mongodb. NCP has no managed MariaDB"
  type        = string

  validation {
    condition     = contains(["mysql", "postgresql", "mongodb"], var.engine)
    error_message = "engine must be one of mysql, postgresql, mongodb. NCP offers no managed MariaDB, so mariadb columns are AWS-only."
  }
}

# NCP needs the FULL version string. The provider normalizes state to the
# version the API reports, and engine_version_code forces replacement, so a
# partial version such as 8.0 makes every later plan schedule another ~30
# minute re-creation.
variable "engine_version" {
  description = "Full engine version string, e.g. 8.0.36 / 14.22 / 7.0.28"
  type        = string

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+(\\.[0-9]+)?$", var.engine_version))
    error_message = "Expected a version like 8.0.36. Run ./csp-support-versions.sh ncp to list what the region offers."
  }
}

variable "db_name" {
  description = "Initial database created with the instance; the matrix connects to it to create and drop each cell's target database. Ignored for MongoDB, which has no such argument"
  type        = string
  default     = "matrixadm"
}

variable "db_username" {
  description = "DB master username (4-16 characters, starts with a letter)"
  type        = string
  default     = "dbadmin"
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach the engine port; the matrix host has to be inside it"
  type        = string
  default     = "0.0.0.0/0"
}
