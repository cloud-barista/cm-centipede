variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "ncp_name_prefix" {
  description = "Resource name prefix, short for centipede tofu; must match the network module. Kept short because the managed MongoDB service_name is limited to 15 characters"
  type        = string
  default     = "cptf"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.ncp_name_prefix))
    error_message = "Prefix must be 2-10 characters of lowercase letters, digits and hyphens, starting with a lowercase letter (to stay within the 15-character MongoDB name limit)."
  }
}

variable "ncp_db_name" {
  description = "Initial database name (starts with a letter, 2-30 characters)"
  type        = string
  default     = "testdb"
}

variable "ncp_db_username" {
  description = "DB master username (4-16 characters, starts with a letter)"
  type        = string
  default     = "dbadmin"
}

variable "allowed_cidr" {
  description = "CIDR allowed for inbound DB ports and SSH; wide open by default"
  type        = string
  default     = "0.0.0.0/0"
}

# ---------------------------------------------------------------------------
# DB engine versions
#   Always use the FULL version string, e.g. 8.0.36.
#     When refreshing state the provider normalizes the version reported by the API
#     with the regex \d+\.\d+(\.\d+)? , and engine_version_code is RequiresReplace.
#     A partial version such as 8.0 therefore makes every plan schedule a DB
#     re-creation that takes about 30 minutes.
#   List the supported versions with: ./scripts/ncp-db-versions.sh
# ---------------------------------------------------------------------------
variable "ncp_mysql_version" {
  description = "Managed MySQL engine version (full version string), e.g. 8.0.36, 8.0.40, 8.0.42, 8.0.45, 8.4.6, 8.4.8"
  type        = string
  default     = "8.0.36"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+(\\.[0-9]+)?$", var.ncp_mysql_version))
    error_message = "Invalid engine version format; expected something like 8.0.36. Run ./scripts/ncp-db-versions.sh to list the supported versions."
  }
}

variable "ncp_postgres_version" {
  description = "Managed PostgreSQL engine version (full version string), e.g. 13.21, 14.20, 14.22, 15.15, 15.17"
  type        = string
  default     = "14.22"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+(\\.[0-9]+)?$", var.ncp_postgres_version))
    error_message = "Invalid engine version format; expected something like 14.22. Run ./scripts/ncp-db-versions.sh to list the supported versions."
  }
}

variable "ncp_mongodb_version" {
  description = "Managed MongoDB engine version (full version string), e.g. 6.0.27, 7.0.24, 7.0.28, 8.0.19"
  type        = string
  default     = "7.0.28"

  validation {
    condition     = can(regex("^[0-9]+\\.[0-9]+(\\.[0-9]+)?$", var.ncp_mongodb_version))
    error_message = "Invalid engine version format; expected something like 7.0.28. Run ./scripts/ncp-db-versions.sh to list the supported versions."
  }
}

