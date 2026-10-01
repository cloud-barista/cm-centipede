package transxex

import (
	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex"
)

// ============================================================================
// Shared Error Types
// ============================================================================

type (
	// MigrationError reports the pipeline stage a migration failed at.
	// Identify the stage with errors.As.
	MigrationError = core.MigrationError

	// OperationError carries the detail of a failed operation: the endpoint,
	// the command, and whatever the command wrote before failing.
	OperationError = core.OperationError

	// ObjectError names the object that was being processed when a migration
	// failed — the table, view, routine or file — and the action being taken
	// on it. It sits inside MigrationError: the stage says which step stopped,
	// this says what it stopped on. Retrieve it with errors.As.
	ObjectError = core.ObjectError
)

// Pipeline stages. Backup and Transfer occur in storage migrations, Dump in
// database migrations; Restore occurs in both.
const (
	StageBackup   = core.StageBackup
	StageTransfer = core.StageTransfer
	StageDump     = core.StageDump
	StageRestore  = core.StageRestore
)

// Operations an OperationError can describe.
const (
	OperationBackup   = core.OperationBackup
	OperationTransfer = core.OperationTransfer
	OperationPreCmd   = core.OperationPreCmd
	OperationPostCmd  = core.OperationPostCmd
	OperationDump     = core.OperationDump
	OperationRestore  = core.OperationRestore
	OperationConnect  = core.OperationConnect
	OperationRollback = core.OperationRollback
)

// Actions an ObjectError can describe — one level finer than a stage, naming
// what was being done to the object rather than which step it belonged to.
const (
	ActionList      = core.ActionList
	ActionReadDDL   = core.ActionReadDDL
	ActionReadRows  = core.ActionReadRows
	ActionWriteDDL  = core.ActionWriteDDL
	ActionWriteRows = core.ActionWriteRows
	ActionAlter     = core.ActionAlter
	ActionUpload    = core.ActionUpload
	ActionDownload  = core.ActionDownload
	ActionInspect   = core.ActionInspect
	ActionRefresh   = core.ActionRefresh
	ActionDrop      = core.ActionDrop
)

// ============================================================================
// Domain-Specific Error Types
// ============================================================================

type (
	// UnsupportedTransferError reports a source/destination combination no
	// storage executor handles.
	UnsupportedTransferError = storagex.UnsupportedTransferError

	// UnsupportedDBMSError reports a database engine with no registered driver.
	UnsupportedDBMSError = dbmsx.UnsupportedDBMSError

	// TargetNotEmptyError reports a destination database that already holds tables.
	TargetNotEmptyError = dbmsx.TargetNotEmptyError

	// TargetDatabaseNotFoundError reports a database that does not exist: a
	// migration destination, which must be provisioned beforehand, or the target
	// of a DropDatabase call made without IfExists.
	TargetDatabaseNotFoundError = dbmsx.TargetDatabaseNotFoundError

	// TargetDatabaseExistsError reports a database CreateDatabase was asked to
	// create that is already there, when the call did not set IfNotExists.
	TargetDatabaseExistsError = dbmsx.TargetDatabaseExistsError

	// SystemDatabaseError reports a CreateDatabase or DropDatabase call aimed at
	// one of the engine's built-in databases.
	SystemDatabaseError = dbmsx.SystemDatabaseError

	// InvalidDatabaseNameError reports a database name the engine cannot use.
	InvalidDatabaseNameError = dbmsx.InvalidDatabaseNameError

	// SchemaNotFoundError reports a name in DBMSLocation.PgSchema that the
	// database does not have. A missing schema is refused rather than skipped:
	// with the name simply dropped from the selection, a typo would migrate less
	// than asked — or nothing at all — and still report success.
	SchemaNotFoundError = dbmsx.SchemaNotFoundError

	// CharsetIncompatibleError reports a target that cannot accept the source's
	// character sets or collations. It carries every blocking issue rather than
	// the first, so one run reports everything there is to fix.
	CharsetIncompatibleError = dbmsx.CharsetIncompatibleError

	// VersionDowngradeError reports a target server older than the source's. A
	// dump is written in the source server's dialect, so migrating to an older
	// server is refused before the dump is taken.
	VersionDowngradeError = dbmsx.VersionDowngradeError

	// DBMSInspectError reports one database InspectDBMSAll could not read. It is
	// collected rather than returned: the databases that were readable still come
	// back. Message is the field that marshals to JSON; Err keeps the original
	// error for errors.As / errors.Is.
	DBMSInspectError = dbmsx.InspectError
)

// ============================================================================
// Field Encryption Errors
// ============================================================================

var (
	// ErrKeyNotFound is returned when the requested key is not in the store.
	ErrKeyNotFound = storagex.ErrKeyNotFound

	// ErrKeyExpired is returned when the key has expired.
	ErrKeyExpired = storagex.ErrKeyExpired

	// ErrKeyMismatch is returned when the key ID does not match.
	ErrKeyMismatch = storagex.ErrKeyMismatch

	// ErrDecryptionFailed is returned when decryption fails.
	ErrDecryptionFailed = storagex.ErrDecryptionFailed

	// ErrInvalidPublicKey is returned when public key parsing fails.
	ErrInvalidPublicKey = storagex.ErrInvalidPublicKey
)
