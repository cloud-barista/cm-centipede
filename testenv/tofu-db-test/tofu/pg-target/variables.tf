variable "host" {
  description = "Target instance host. On NCP this is the console-issued public domain"
  type        = string
}

variable "port" {
  type    = number
  default = 5432
}

variable "username" {
  description = "Master user. It must hold CREATEDB; the matrix checks that before the first cell"
  type        = string
}

variable "password" {
  type      = string
  sensitive = true
}

variable "admin_database" {
  description = "Database to connect to in order to create the cell's target database. It cannot be the one being created"
  type        = string
  default     = "matrixadm"
}

variable "databases" {
  description = "The cell's target databases: created empty here, dropped when the cell ends. One entry per source database the cell migrates"
  type        = list(string)

  validation {
    condition     = length(var.databases) > 0
    error_message = "databases must name at least one database."
  }
}

# Which template the cell's database is copied from, and why it is a knob.
#
#   A new database inherits its public schema and that schema's ACL from the
#   template, so this decides whether the master user can create in public - the
#   exact thing the privilege pre-flight measures.
#
#   On NCP the pre-flight found public revoked (`postgres=UC/postgres`) on
#   matrixadm, a database NCP's own API created. Whether that revocation lives in
#   the templates or is applied per database NCP creates is unverified, and the
#   two templates can differ:
#
#     template0  pristine and not meant to be modified, so it still carries the
#                stock defaults - which on PostgreSQL 14 and earlier means public
#                grants CREATE to PUBLIC
#     template1  what CREATE DATABASE uses by default; an operator or the CSP can
#                have changed it, so it is where a revocation would spread from
#
#   template0 is the default here, and it is also what this provider defaults to,
#   so stating it changes no behaviour - it just puts the choice where the reason
#   for it is written. Set template1 to compare the two.
variable "template" {
  description = "Template database the cell's target is copied from; decides the inherited public schema ACL"
  type        = string
  default     = "template0"

  validation {
    condition     = contains(["template0", "template1"], var.template)
    error_message = "template must be template0 or template1."
  }
}

# Transport is not an input, for the reason set out in tofu/my-target/provider.tf:
# TARGET_TLS_MODE is a condition of the migration, and this connection only
# creates the database the migration needs. Applying the experiment's condition
# to the setup turns a server with no TLS into a SKIPped column rather than a
# migration result.
#
# So provider.tf pins sslmode to prefer - encrypt when the server offers it,
# plaintext when it does not. libpq implements that itself, so unlike the MySQL
# provider there is nothing to work around here.

# Why this is a switch and not just always on.
#
#   GRANT on schema public only succeeds when the role running it owns the
#   schema or is a member of the owner. On a stock PostgreSQL 13/14 the schema
#   still carries its default grant to PUBLIC, so no GRANT is needed - and
#   issuing one anyway fails with "must be owner of schema public", which would
#   break columns that work today.
#
#   NCP revokes that PUBLIC grant (public ends up as {postgres=UC/postgres}),
#   which is what makes the GRANT necessary there.
#
#   lib/tofu.sh decides: it asks the instance once per column whether the user
#   can already create in public and whether it could grant, and sets this
#   accordingly.
variable "grant_public" {
  description = "Grant USAGE and CREATE on schema public to the master user"
  type        = bool
  default     = false
}
