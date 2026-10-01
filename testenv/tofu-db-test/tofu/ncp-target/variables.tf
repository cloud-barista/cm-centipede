variable "ncp_region" {
  type    = string
  default = "KR"
}

variable "engine" {
  description = "mysql or postgresql. MongoDB never reaches this module - it has no CREATE DATABASE"
  type        = string

  validation {
    condition     = contains(["mysql", "postgresql"], var.engine)
    error_message = "engine must be mysql or postgresql."
  }
}

variable "instance_no" {
  description = "The managed-DB SERVICE instance number, from the column's instance_no output. Not the server instance number"
  type        = string

  validation {
    condition     = length(var.instance_no) > 0
    error_message = "instance_no is empty: the column's instance has no service number yet."
  }
}

variable "databases" {
  description = "The cell's target databases: created empty here, dropped when the cell ends"
  type        = list(string)

  validation {
    condition     = length(var.databases) > 0
    error_message = "databases must name at least one database."
  }
}

# PostgreSQL only. The CSP API takes the owner at creation time, which is the
# whole reason this path is better than a wire-protocol CREATE DATABASE on NCP:
# a database owned by the master account carries no schema-permission problem to
# work around afterwards.
variable "owner" {
  description = "Owner of each PostgreSQL database; ignored for MySQL"
  type        = string
  default     = ""
}
