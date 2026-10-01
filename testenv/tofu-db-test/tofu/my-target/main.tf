# ---------------------------------------------------------------------------
# The target databases for one MySQL or MariaDB cell.
#
#   A cell migrates every database named in var.databases, and each one has to
#   exist and be empty before the migration starts - centipede creates a target
#   database only for providerName "onprem", and a managed instance is aws or
#   ncp. So the matrix creates them here, and one destroy removes the lot.
#
#   There is no counterpart to tofu/pg-target's GRANT: on MySQL and MariaDB the
#   master account already holds every privilege on a database it created, so
#   creating it is the whole job.
# ---------------------------------------------------------------------------

resource "mysql_database" "target" {
  for_each = toset(var.databases)

  name = each.value

  # Character set and collation are left at the instance defaults on purpose.
  # A managed target follows its own server defaults, and that is the real
  # migration condition; forcing the source's values here would hide a mismatch
  # the run is meant to reveal. Table character sets travel inside the dump's
  # CREATE TABLE statements either way.
}
