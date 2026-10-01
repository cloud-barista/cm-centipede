// Package plan derives a migration's target model from its source model.
//
// BuildTargetDataMigrationModel takes what was discovered about the source —
// filesystems, buckets, databases — together with the destination connections
// and any TargetMapping the caller supplied, and produces the
// TargetDataMigrationModel the migration executes. Where the caller named
// nothing, it fills in defaults: destination paths, bucket and database names,
// and PostgreSQL schema mappings.
//
// Building a plan also validates it. A request that cannot describe a workable
// migration — mismatched source and target kinds, an incompatible engine pair,
// a target database that neither exists nor can be created — yields a
// *ValidationError, which the controller reports as 400. An unreachable
// cm-honeybee or cm-beetle is a plain error instead, reported as 500.
//
// Connections are resolved through pkg/core/migration rather than by
// duplicating that logic, so a plan describes exactly what the migration
// will do.
package plan
