package base

import (
	"sort"
	"strings"
)

// =============================================================================
// System Databases
// =============================================================================

// systemDatabases lists the databases every server of an engine ships with:
// catalogs, templates and internal bookkeeping. They are never migration
// sources or targets, so ListDatabases hides them.
var systemDatabases = map[string][]string{
	DBMSTypeMySQL:      {"information_schema", "mysql", "performance_schema", "sys"},
	DBMSTypeMariaDB:    {"information_schema", "mysql", "performance_schema", "sys"},
	DBMSTypePostgreSQL: {"postgres", "template0", "template1"},
	DBMSTypeMongoDB:    {"admin", "config", "local"},
}

// managedDatabases lists the databases a managed service adds on top of what
// the engine itself ships with. They belong to the provider's control plane,
// not to the user: RDS keeps "rdsadmin" on PostgreSQL and MariaDB (plus an
// "innodb" database on MariaDB), Cloud SQL keeps "cloudsqladmin", and Azure
// keeps "azure_maintenance" and "azure_sys". They enumerate like any other
// database, yet connecting to one is refused even for the master user, so they
// are hidden with the built-in ones.
//
// A self-managed server may legitimately own one of these names. That costs a
// user database its visibility, and blocks CreateDatabase/DropDatabase on it,
// which is the safer side of the trade: the alternative offers a database that
// cannot be read as a migration source.
var managedDatabases = map[string][]string{
	DBMSTypeMySQL:      {"innodb", "rdsadmin"},
	DBMSTypeMariaDB:    {"innodb", "rdsadmin"},
	DBMSTypePostgreSQL: {"azure_maintenance", "azure_sys", "cloudsqladmin", "rdsadmin"},
}

// IsSystemDatabase reports whether name is one of dbmsType's built-in databases
// or one a managed service adds to them. An unknown dbmsType has no known
// system databases, so nothing is reported.
func IsSystemDatabase(dbmsType, name string) bool {
	for _, sys := range systemDatabases[dbmsType] {
		if name == sys {
			return true
		}
	}
	for _, managed := range managedDatabases[dbmsType] {
		if name == managed {
			return true
		}
	}
	return false
}

// UserDatabases returns names with dbmsType's system databases removed and the
// rest sorted by name. Every driver ends its ListDatabases with this call, so
// the result does not depend on the order the engine or its CLI happened to
// return — notably psql's json_agg transport, which does not preserve ORDER BY.
// The result is nil when no user database remains.
func UserDatabases(dbmsType string, names []string) []string {
	var out []string
	for _, name := range names {
		if name == "" || IsSystemDatabase(dbmsType, name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// =============================================================================
// System Schemas
// =============================================================================
//
// Schemas divide a database the way databases divide a server, and the two need
// the same pair of helpers — hide what the engine ships with, sort the rest.
// Only PostgreSQL has schemas distinct from databases: MySQL and MariaDB treat
// the two words as synonyms and MongoDB has no equivalent, so for those engines
// nothing is a system schema and ListSchemas returns nothing to filter.

// IsSystemSchema reports whether name is one of dbmsType's built-in schemas.
//
// PostgreSQL reserves the whole "pg_" prefix, not just the fixed names:
// pg_catalog and pg_toast always exist, and a session's temporary schemas
// appear as pg_temp_N / pg_toast_temp_N for as long as that session lives. The
// prefix is matched rather than enumerated so a temporary schema belonging to
// somebody else's session can never be picked up as a migration source.
func IsSystemSchema(dbmsType, name string) bool {
	if dbmsType != DBMSTypePostgreSQL {
		return false
	}
	return strings.HasPrefix(name, "pg_") || name == "information_schema"
}

// UserSchemas returns names with dbmsType's system schemas removed and the rest
// sorted by name — the schema-level counterpart of UserDatabases, and used the
// same way: every driver ends its ListSchemas with this call so the order does
// not depend on the transport. The result is nil when no user schema remains.
func UserSchemas(dbmsType string, names []string) []string {
	var out []string
	for _, name := range names {
		if name == "" || IsSystemSchema(dbmsType, name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
