# ---------------------------------------------------------------------------
# The target databases for one NCP cell, created through the CSP API.
#
# ── Why this module exists at all ───────────────────────────────────────────
# tofu/my-target and tofu/pg-target create a cell's databases by connecting to
# the instance and running CREATE DATABASE. That works on RDS, where the master
# account is a real administrator. On NCP it does not:
#
#   Error 1044 (42000): Access denied for user 'dbadmin'@'%' to database 'cptfm_probe'
#
# NCP's managed databases are administered through the CSP, not through the
# wire: the master account is granted rights on the databases the service knows
# about, and creating a new one is an API call. So on NCP the matrix makes that
# call instead, and one destroy removes the cell's databases again.
#
# ── PostgreSQL gets its owner for free ──────────────────────────────────────
# The PostgreSQL API takes an owner at creation time. A database owned by the
# master account needs no GRANT on schema public afterwards, which is the
# problem tofu/pg-target has to work around on NCP (the PUBLIC grant is revoked
# there, leaving public as {postgres=UC/postgres}). Creating it owned is the
# fix that comment always pointed at.
#
# ── MongoDB is not here ─────────────────────────────────────────────────────
# MongoDB has no CREATE DATABASE - a database begins to exist when its first
# collection is written - so there is nothing to create in advance, and
# lib/source.sh drops the previous cell's database with mongosh instead.
# ---------------------------------------------------------------------------

locals {
  is_mysql    = var.engine == "mysql"
  is_postgres = var.engine == "postgresql"
}

resource "ncloud_mysql_databases" "target" {
  count = local.is_mysql ? 1 : 0

  mysql_instance_no = var.instance_no

  mysql_database_list = [
    for name in var.databases : { name = name }
  ]
}

resource "ncloud_postgresql_databases" "target" {
  count = local.is_postgres ? 1 : 0

  # This resource spells the service instance number `id`, where the MySQL one
  # spells it `mysql_instance_no`. Same value, different argument name.
  id = var.instance_no

  postgresql_database_list = [
    for name in var.databases : { name = name, owner = var.owner }
  ]

  lifecycle {
    precondition {
      condition     = length(var.owner) > 0
      error_message = "owner is required for PostgreSQL: the CSP API takes it at creation time."
    }
  }
}
