// Package validation verifies that a finished migration actually landed.
//
// ValidateFileSystem, ValidateObjectStorage and ValidateDBMS each re-read both
// ends of a completed transfer and compare them, reporting one
// ValidationDetailItem per mismatch: file inventories with their SHA256
// checksums, object inventories with their ETags, and for DBMS the per-table
// row counts plus the schema object inventories. Comparing rows alone would
// miss anything without them — a view, a function, a trigger — and those are
// exactly what fails quietly.
//
// Cross-storage transfers (filesystem↔objectstorage) compare heterogeneous
// endpoints and are reported as skipped rather than validated.
//
// Connections are resolved through pkg/core/migration, using the same
// resolvers the migration itself used. Resolving them a second way here is how
// a transfer that succeeded ends up failing verification.
package validation
