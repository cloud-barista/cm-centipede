// Package commonmodel holds the property types shared by the source-side and
// target-side data migration models. Source models describe what was observed
// in the source environment; target models describe the migration plan to be
// executed. Anything both sides need — connection references, filter rules and
// the enumerations below — lives here so the two never drift apart.
package commonmodel

// ── DBMS engine ──────────────────────────────────────────────────────────────

// DBMSType identifies the database engine of a source or target.
type DBMSType string

const (
	DBMSTypeMySQL      DBMSType = "mysql"
	DBMSTypeMariaDB    DBMSType = "mariadb"
	DBMSTypePostgreSQL DBMSType = "postgresql"
	DBMSTypeMongoDB    DBMSType = "mongodb"
)

// ── Transfer strategy ────────────────────────────────────────────────────────

// Transfer strategy values, mapping 1:1 onto transx-ex's Strategy. They only
// carry real meaning for SSH→SSH (file system) transfers: cross-storage and
// S3→S3 always relay through local staging inside transx, and DBMS transfers
// are handled by dbmsx, so both are strategy-independent.
const (
	// StrategyAuto: transx picks the best method from the source/destination types.
	StrategyAuto = "auto"
	// StrategyDirect: force a direct SSH→SSH transfer (agent-forward).
	StrategyDirect = "direct"
	// StrategyRelay: force a relay through the local machine (bypasses the
	// agent-forward requirement). This is the default.
	StrategyRelay = "relay"
)

// ── Storage type ─────────────────────────────────────────────────────────────

// Storage-type discriminators for cross-storage migration destinations.
// A homogeneous job (SSH→SSH, S3→S3) leaves DstType empty; a cross-storage job
// (filesystem↔objectstorage) sets it to the destination's storage type.
const (
	StorageTypeFilesystem    = "filesystem"
	StorageTypeObjectStorage = "objectstorage"
)
