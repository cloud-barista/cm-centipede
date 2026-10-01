terraform {
  required_version = ">= 1.6"
  required_providers {
    mysql = { source = "petoju/mysql", version = "~> 3.0" }
  }
}

# Like tofu/pg-target, this provider speaks the database's own wire protocol
# rather than a CSP API, which is why the module sits at the top of tofu/ and not
# under aws/ or ncp/: one module creates a cell's databases on an RDS instance
# and on an NCP managed instance alike.
#
# The connection details arrive as variables rather than from OpenBao: the host
# is only known once the column's instance exists, and the password has already
# been read for this cell by lib/tofu.sh.
# ── Transport: the strongest the server actually offers ─────────────────────
# This connection is not part of what the matrix measures. TARGET_TLS_MODE is a
# condition of the migration, applied to the connection centipede makes; this one
# only runs CREATE DATABASE so the cell has somewhere to migrate into. Forcing
# the experiment's condition onto the setup was a mistake: NCP Cloud DB for MySQL
# offers no TLS at all, so demanding it failed the probe and SKIPped columns
# whose migration would have run fine - transx-ex implements a real prefer and
# falls back to plaintext there. A false negative, and about setup rather than
# about migrating.
#
# So it connects the best way it can, always, whatever TARGET_TLS_MODE says:
#
#   tls = skip-verify           use TLS, do not verify the certificate. Verifying
#                               is not possible anyway - RDS and NCP sign with
#                               their own CAs and this environment has no bundle.
#   allowFallbackToPlaintext    ...unless the server offers no TLS, then plaintext.
#
# Together those are libpq's prefer, which the provider has no setting for: it
# validates tls against exactly true, false and skip-verify. The parameter goes
# into the DSN through conn_params, where go-sql-driver reads it.
#
# Nothing is weakened by the fallback. It is taken only against a server with no
# TLS to offer, and against such a server the migration cannot use TLS either.
provider "mysql" {
  endpoint = "${var.host}:${var.port}"
  username = var.username
  password = var.password

  tls         = "skip-verify"
  conn_params = { allowFallbackToPlaintext = "true" }
}
