// Package dbmsx provides DBMS data migration capabilities for MySQL, MariaDB,
// PostgreSQL, and MongoDB over both direct connections and SSH tunnels.
//
// A DBMSMigrationModel names a source and a target DBMSLocation plus the
// databases to move. Plan turns that into a core.Pipeline and Handle runs it
// asynchronously, reporting progress and a terminal MigrationStatus.
//
// Plan routes each migration down one of two paths. When both ends are reached
// over an SSH tunnel, a direct pipe streams the source dump straight into the
// target restore in a single step, with nothing landing on disk. Every other
// combination of access types takes the two-step staging transfer: dump to a
// local staging file, then restore from it.
//
// # Layout
//
//	driver/     per-engine implementations over a shared base package
//	executor/   dump, restore and direct-pipe executors
//	filter/     table, row and schema-object exclusion
//
// Shared machinery — SSH, the pipeline skeleton, the handle and the error
// vocabulary — lives in core and is re-exported here, so callers never import
// core themselves.
package dbmsx
