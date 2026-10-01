package dbmsx

import (
	"fmt"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
	execpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/executor"
)

// Type aliases — re-export driver error types so callers use unqualified names.
type (
	UnsupportedDBMSError        = drvpkg.UnsupportedDBMSError
	TargetNotEmptyError         = drvpkg.TargetNotEmptyError
	TargetDatabaseExistsError   = drvpkg.TargetDatabaseExistsError
	TargetDatabaseNotFoundError = drvpkg.TargetDatabaseNotFoundError
	SystemDatabaseError         = drvpkg.SystemDatabaseError
	InvalidDatabaseNameError    = drvpkg.InvalidDatabaseNameError
	SchemaNotFoundError         = drvpkg.SchemaNotFoundError
	CharsetIncompatibleError    = drvpkg.CharsetIncompatibleError
	VersionDowngradeError       = drvpkg.VersionDowngradeError
	OperationError              = drvpkg.OperationError
	ObjectError                 = drvpkg.ObjectError
)

// Const re-declarations — re-export driver operation constants.
const (
	OperationDump     = drvpkg.OperationDump
	OperationRestore  = drvpkg.OperationRestore
	OperationConnect  = drvpkg.OperationConnect
	OperationRollback = drvpkg.OperationRollback
)

// =============================================================================
// InspectError
// =============================================================================

// InspectError reports one database InspectAll could not read. It never aborts
// the call: the databases that were readable are still returned.
//
// Message carries the failure text and is what marshals to JSON, so a caller
// serving the skipped list over HTTP needs no conversion. Err keeps the
// original error for errors.As / errors.Is and is left out of the JSON.
type InspectError struct {
	Database string `json:"database"`
	Message  string `json:"error"`
	Err      error  `json:"-"`
}

// The receivers are values, not pointers: InspectAll hands back a []InspectError
// and each element has to satisfy error on its own.
func (e InspectError) Error() string {
	return fmt.Sprintf("inspect of database %q failed: %s", e.Database, e.Message)
}

func (e InspectError) Unwrap() error { return e.Err }

// Type alias — re-export executor error type so callers use the unqualified name.
type MigrationError = execpkg.MigrationError

// Const re-declarations — re-export executor stage constants.
const (
	StageDump    = execpkg.StageDump
	StageRestore = execpkg.StageRestore
)
