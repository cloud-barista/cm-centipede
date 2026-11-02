// Package base holds the vocabulary every dbmsx driver is written against:
// the DBDriver interface, the DBMSLocation and option types the interface
// speaks in, and the helpers each engine reuses rather than reimplements —
// identifier and database-name validation, charset and collation lookup,
// server-version parsing, TLS and SSH configuration, statement splitting and
// DEFINER rewriting.
//
// It contains no engine-specific logic. The per-engine packages (mysql,
// mariadb, postgresql, mongodb) implement DBDriver on top of it, and the
// driver facade re-exports the result.
package base
