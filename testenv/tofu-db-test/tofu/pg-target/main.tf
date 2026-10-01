# ---------------------------------------------------------------------------
# The target databases for one PostgreSQL cell.
#
#   A cell migrates every database named in var.databases, and each one has to
#   exist and be empty before the migration starts - centipede creates a target
#   database only for providerName "onprem", and a managed instance is aws or
#   ncp. So the matrix creates them here, and one destroy removes the lot.
#
#   A fresh database copies a template, including its public schema and that
#   schema's ACL. On a server that revoked the PUBLIC grant the GRANT therefore
#   has to be repeated for every database of every cell, not once per instance -
#   which is why this module is applied per cell rather than per column.
# ---------------------------------------------------------------------------

resource "postgresql_database" "target" {
  for_each = toset(var.databases)

  name  = each.value
  owner = var.username

  # Which template is used decides what the new database's public schema ACL
  # looks like, which is the whole subject of the privilege pre-flight - so it is
  # stated here rather than left to a provider default. See var.template.
  template = var.template

  # Encoding and collation are left at the server defaults on purpose. The source
  # database's are the source axis of the matrix; forcing them here would hide a
  # mismatch that the run is meant to reveal.
}

resource "postgresql_grant" "public_schema" {
  for_each = var.grant_public ? toset(var.databases) : toset([])

  role        = var.username
  database    = postgresql_database.target[each.value].name
  schema      = "public"
  object_type = "schema"
  privileges  = ["USAGE", "CREATE"]
}
