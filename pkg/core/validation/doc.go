// Package validation verifies that a finished migration actually landed.
//
// ValidateFileSystem, ValidateObjectStorage and ValidateDBMS each re-read both
// ends of a completed transfer and compare them, reporting one
// ValidationDetailItem per mismatch: file inventories with their sizes and
// either their modification times (ModeQuick) or their SHA256 checksums
// (ModeChecksum), object inventories with their ETags, and for DBMS the
// per-table row counts plus the schema object inventories. Comparing rows alone
// would miss anything without them — a view, a function, a trigger — and those
// are exactly what fails quietly.
//
// Each end is read at the same time as the other, and Options.Progress hears
// where a run has got to — files hashed, tables counted — so a validation that
// takes minutes can be told from one that has stopped.
//
// Cross-storage transfers (filesystem↔objectstorage) compare heterogeneous
// endpoints and are reported as skipped rather than validated.
//
// Connections are resolved through pkg/core/migration, using the same
// resolvers the migration itself used. Resolving them a second way here is how
// a transfer that succeeded ends up failing verification.
package validation
