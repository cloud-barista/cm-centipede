terraform {
  required_version = ">= 1.6"
  required_providers {
    postgresql = { source = "cyrilgdn/postgresql", version = "~> 1.22" }
  }
}

# This provider speaks the PostgreSQL wire protocol rather than a CSP API, so it
# is the only one in the matrix that connects to a database directly.
#
#   superuser = false is required, not optional. A managed master account is
#   never a superuser, and with the default the provider takes paths reserved
#   for one and fails.
#
#   The connection details arrive as variables rather than from OpenBao: the
#   host is only known after the column's instance exists, and the password has
#   already been read once by lib/tofu.sh for the same cell.
provider "postgresql" {
  host      = var.host
  port      = var.port
  username  = var.username
  password  = var.password
  database  = var.admin_database
  sslmode   = "prefer"
  superuser = false

  connect_timeout = 30
}
