variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

# The engine versions .env sets, read here only to list the specs each one
# offers. Defaults match tofu/ncp/database.
variable "ncp_mysql_version" {
  type    = string
  default = "8.0.36"
}

variable "ncp_postgres_version" {
  type    = string
  default = "14.22"
}

variable "ncp_mongodb_version" {
  type    = string
  default = "7.0.28"
}
