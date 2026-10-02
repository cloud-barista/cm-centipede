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

# Which engines this module runs. Not set in .env: provision.sh and deprovision.sh
# work it out from --engine and from what the state already holds, and export it
# for each apply. The default - every engine - is what a plain apply gets.
variable "ncp_db_engines" {
  description = "Managed DB engines to run: any of mysql, postgres, mongodb"
  type        = list(string)
  default     = ["mysql", "postgres", "mongodb"]

  validation {
    condition     = alltrue([for e in var.ncp_db_engines : contains(["mysql", "postgres", "mongodb"], e)])
    error_message = "ncp_db_engines may only contain mysql, postgres and mongodb."
  }
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

# ---------------------------------------------------------------------------
# DB server specs (product codes)
#   Empty - the default - leaves the choice to NCP's default spec for the engine,
#   as before these existed. A code is a long dotted string (SVR.VDBAS....);
#   copy it verbatim from
#   ./scripts/ncp-db-versions.sh <engine>, which lists the specs available for
#   the engine version set above.
#   Like every managed DB attribute it is RequiresReplace: setting or changing it
#   on a provisioned engine re-creates that DB (about 30 minutes, data lost).
# ---------------------------------------------------------------------------
variable "ncp_mysql_product_code" {
  description = "Managed MySQL server spec (product code); empty = NCP default"
  type        = string
  default     = ""
}

variable "ncp_postgres_product_code" {
  description = "Managed PostgreSQL server spec (product code); empty = NCP default"
  type        = string
  default     = ""
}

variable "ncp_mongodb_product_code" {
  description = "Managed MongoDB member server spec (product code); empty = NCP default"
  type        = string
  default     = ""
}

# ---------------------------------------------------------------------------
# DB images (image product codes)
#   Empty - the default - lets NCP pick the image for the engine version, as
#   before these existed. One version can come as several images (generations,
#   G2 / G3), and a spec only works with its own image, so pin the image whenever
#   a product code above is set: copy the [image ...] code the spec is listed
#   under in ./scripts/ncp-db-versions.sh <engine>. The plan refuses an image that
#   is not one of the engine version's. RequiresReplace like the rest.
# ---------------------------------------------------------------------------
variable "ncp_mysql_image_product_code" {
  description = "Managed MySQL image (image product code) for ncp_mysql_version; empty = NCP's choice"
  type        = string
  default     = ""
}

variable "ncp_postgres_image_product_code" {
  description = "Managed PostgreSQL image (image product code) for ncp_postgres_version; empty = NCP's choice"
  type        = string
  default     = ""
}

variable "ncp_mongodb_image_product_code" {
  description = "Managed MongoDB image (image product code) for ncp_mongodb_version; empty = NCP's choice"
  type        = string
  default     = ""
}
