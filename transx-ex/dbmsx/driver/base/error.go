package base

import (
	"fmt"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// =============================================================================
// Shared error vocabulary (core)
// =============================================================================

// Operation constants are declared once in core and shared with the storage
// domain; a dump and an object-storage upload are both "operations that failed
// against an endpoint", and callers switch on the same values.
const (
	OperationDump     = core.OperationDump
	OperationRestore  = core.OperationRestore
	OperationConnect  = core.OperationConnect
	OperationRollback = core.OperationRollback
)

// OperationError provides detailed context for a failed driver operation.
// See core.OperationError; driver code fills DBMSType, which selects the
// database wording in Error().
type OperationError = core.OperationError

// ObjectError names the database object a driver was processing when an
// operation failed. See core.ObjectError; Obj builds one for a loop.
type ObjectError = core.ObjectError

// Obj identifies one object across every error a driver loop can return.
type Obj = core.Obj

// Actions a driver takes on an object. Shared with the storage domain, which
// uses the upload/download pair instead.
const (
	ActionList      = core.ActionList
	ActionReadDDL   = core.ActionReadDDL
	ActionReadRows  = core.ActionReadRows
	ActionWriteDDL  = core.ActionWriteDDL
	ActionWriteRows = core.ActionWriteRows
	ActionAlter     = core.ActionAlter
	ActionInspect   = core.ActionInspect
	ActionRefresh   = core.ActionRefresh
	ActionDrop      = core.ActionDrop
)

// Object kinds outside the dbmsx/filter exclusion vocabulary. The kinds that
// vocabulary covers — views, routines, triggers, events, foreign keys,
// indexes — are named by its own ObjectKind* values, so an error and a filter
// never spell the same kind differently.
const (
	ObjectKindTable      = core.ObjectKindTable
	ObjectKindCollection = core.ObjectKindCollection
	ObjectKindDatabase   = core.ObjectKindDatabase
	ObjectKindSchema     = core.ObjectKindSchema
)

// =============================================================================
// UnsupportedDBMSError
// =============================================================================

// UnsupportedDBMSError is returned when a DBMSType value has no registered driver.
type UnsupportedDBMSError struct {
	DBMSType string
}

func (e *UnsupportedDBMSError) Error() string {
	return fmt.Sprintf("unsupported DBMS type: %q", e.DBMSType)
}

// TargetNotEmptyError is returned when CheckTargetEmpty finds existing tables.
type TargetNotEmptyError struct {
	DBMSType   string
	Database   string
	TableCount int
}

func (e *TargetNotEmptyError) Error() string {
	return fmt.Sprintf("destination database %q (%s) is not empty: %d table(s) already exist",
		e.Database, e.DBMSType, e.TableCount)
}

// =============================================================================
// TargetDatabaseExistsError
// =============================================================================

// TargetDatabaseExistsError is returned by CreateDatabase when the database it
// was asked to create is already there and the caller did not pass IfNotExists.
// PrepareTarget never returns it: that call requires the target database to
// already exist.
type TargetDatabaseExistsError struct {
	DBMSType string
	Database string
}

func (e *TargetDatabaseExistsError) Error() string {
	return fmt.Sprintf("target database %q already exists on %s: drop it first or use a different database name",
		e.Database, e.DBMSType)
}

// =============================================================================
// TargetDatabaseNotFoundError
// =============================================================================

// TargetDatabaseNotFoundError is returned by PrepareTarget when the target
// database does not exist. dbmsx does not create it implicitly; it must be
// provisioned beforehand — with CreateDatabase, or out of band.
// DropDatabase returns it too, for the database it was asked to drop, unless the
// caller passed IfExists.
type TargetDatabaseNotFoundError struct {
	DBMSType string
	Database string
}

func (e *TargetDatabaseNotFoundError) Error() string {
	return fmt.Sprintf("target database %q does not exist on %s: it must be created before migration",
		e.Database, e.DBMSType)
}

// =============================================================================
// SchemaNotFoundError
// =============================================================================

// SchemaNotFoundError is returned when a name in DBMSLocation.PgSchema does not
// exist in the database. A missing schema is refused rather than skipped: with
// the name simply dropped from the selection, a typo would migrate less than
// asked — or nothing at all — and still report success.
type SchemaNotFoundError struct {
	DBMSType string
	Database string
	Schema   string
}

func (e *SchemaNotFoundError) Error() string {
	return fmt.Sprintf("schema %q does not exist in database %q on %s",
		e.Schema, e.Database, e.DBMSType)
}

// =============================================================================
// SystemDatabaseError
// =============================================================================

// SystemDatabaseError is returned when a DDL call targets one of the engine's
// built-in databases — the catalogs, templates and bookkeeping stores listed in
// systemDatabases, which ListDatabases already hides. Creating one is impossible
// and dropping one breaks the server, so both calls refuse before connecting.
type SystemDatabaseError struct {
	DBMSType  string
	Database  string
	Operation string // "create" or "drop"
}

func (e *SystemDatabaseError) Error() string {
	return fmt.Sprintf("refusing to %s %q on %s: it is a system database",
		e.Operation, e.Database, e.DBMSType)
}

// =============================================================================
// CharsetIncompatibleError
// =============================================================================

// CharsetIncompatibleError is returned before a migration takes its dump, when
// the target cannot accept the source's character sets or collations. It carries
// every blocking issue rather than the first, so one run reports everything the
// operator has to fix instead of one thing per attempt.
//
// It is raised early on purpose: the same conditions would otherwise surface as
// a failed CREATE statement at the start of the restore, after the whole dump
// has been taken.
type CharsetIncompatibleError struct {
	DBMSType string
	Database string
	Issues   []CharsetIssue
}

func (e *CharsetIncompatibleError) Error() string {
	lines := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		lines = append(lines, "  - "+i.String())
	}
	return fmt.Sprintf(
		"target database %q (%s) cannot accept the source's character sets or collations:\n%s",
		e.Database, e.DBMSType, strings.Join(lines, "\n"))
}

// =============================================================================
// InvalidDatabaseNameError
// =============================================================================

// InvalidDatabaseNameError is returned when a database name cannot be used on
// the engine. Create and drop quote the name into their DDL — a statement cannot
// carry it as a bound parameter — so a name that the engine rejects, or that
// would break out of its quoting, is refused before any connection is made.
type InvalidDatabaseNameError struct {
	DBMSType string
	Database string
	Reason   string
}

func (e *InvalidDatabaseNameError) Error() string {
	return fmt.Sprintf("database name %q is not usable on %s: %s",
		e.Database, e.DBMSType, e.Reason)
}

// =============================================================================
// VersionDowngradeError
// =============================================================================

// VersionDowngradeError is returned before a migration takes its dump, when the
// target server is older than the source. A dump is written in the source
// server's dialect, so anything the source added since the target's release
// reaches the restore as a syntax error — and only after the whole dump has been
// taken, which is why this is raised at the start instead.
//
// Only a difference in the components that decide a feature set is reported; a
// patch-level difference never raises this. See CompareServerVersions.
type VersionDowngradeError struct {
	DBMSType      string
	Database      string
	SourceVersion string
	TargetVersion string
}

func (e *VersionDowngradeError) Error() string {
	return fmt.Sprintf(
		"target database %q runs %s %s, older than the source's %s: "+
			"a dump is written in the source server's dialect, so migrating to an older "+
			"server is not supported",
		e.Database, e.DBMSType, e.TargetVersion, e.SourceVersion)
}
