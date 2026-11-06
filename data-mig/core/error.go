package core

import (
	"fmt"
	"strconv"
)

// =============================================================================
// Stage Constants
// =============================================================================

// Pipeline stages a migration can fail at. Both domains report failures against
// this vocabulary: the storage domain uses backup/transfer/restore, the DBMS
// domain dump/restore.
const (
	StageBackup   = "backup"
	StageTransfer = "transfer"
	StageDump     = "dump"
	StageRestore  = "restore"
)

// =============================================================================
// Operation Constants
// =============================================================================

// Operations an OperationError can describe. Restore is shared: both domains
// write to a destination and call that step by the same name.
const (
	OperationBackup   = "backup"
	OperationTransfer = "transfer"
	OperationPreCmd   = "pre-command"
	OperationPostCmd  = "post-command"
	OperationDump     = "dump"
	OperationRestore  = "restore"
	OperationConnect  = "connect"
	OperationRollback = "rollback"
)

// =============================================================================
// Action Constants
// =============================================================================

// Actions an ObjectError can describe. They sit one level finer than a stage:
// a dump both lists objects and reads their definitions and their rows, and
// knowing which of those failed on a table is the difference between a
// privilege problem, a schema problem and a data problem.
const (
	ActionList      = "list"
	ActionReadDDL   = "read DDL"
	ActionReadRows  = "read rows"
	ActionWriteDDL  = "create"
	ActionWriteRows = "insert"
	ActionAlter     = "alter"
	ActionUpload    = "upload"
	ActionDownload  = "download"
	ActionInspect   = "inspect"
	ActionRefresh   = "refresh"
	ActionDrop      = "drop"
)

// =============================================================================
// Object Kind Constants
// =============================================================================

// Object kinds that are not part of the exclusion vocabulary in
// dbmsx/filter — a table is never excluded by object kind (the table filter
// selects those), and the storage kinds belong to the other domain entirely.
//
// For the database objects that vocabulary does cover — views, routines,
// triggers, events, foreign keys, indexes and the PostgreSQL/MongoDB kinds —
// ObjectError.Kind carries the filter's own ObjectKind* value verbatim rather
// than a copy declared here, so the two never drift into naming the same thing
// differently in a filter and in an error.
const (
	ObjectKindTable      = "table"
	ObjectKindCollection = "collection"
	ObjectKindDatabase   = "database"
	ObjectKindSchema     = "schema"
	ObjectKindFile       = "file"
	ObjectKindObject     = "object"
	ObjectKindBucket     = "bucket"
)

// =============================================================================
// MigrationError
// =============================================================================

// MigrationError wraps a lower-level error with the pipeline stage at which it
// occurred. Callers identify the stage with errors.As.
type MigrationError struct {
	Stage string
	Err   error
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("migration failed at stage %q: %v", e.Stage, e.Err)
}

func (e *MigrationError) Unwrap() error { return e.Err }

// =============================================================================
// OperationError
// =============================================================================

// OperationError provides detailed context for a failed operation.
//
// The struct spans both domains, so several fields are domain-specific and stay
// zero elsewhere: Method and IsRelayMode describe a storage transfer, DBMSType a
// database engine. Error() picks its wording from Operation, and — for the
// restore step, which both domains have — from whether DBMSType is set.
type OperationError struct {
	Operation string // one of the Operation* constants

	Method      string // storage: transfer method (rsync, s3)
	DBMSType    string // dbms: engine (mysql, postgresql, …)
	IsRelayMode bool   // storage: transfer relayed through local staging

	Source      string            // source path/endpoint
	Destination string            // destination path/endpoint
	Command     string            // executed command, when one was
	Output      string            // command output/stderr, for diagnostics
	Context     map[string]string // additional context
	Err         error
}

func (e *OperationError) Error() string {
	switch e.Operation {
	case OperationBackup:
		return fmt.Sprintf("backup operation failed for source '%s': %v", e.Source, e.Err)

	case OperationRestore:
		// Shared step name; the engine field tells the two domains apart.
		if e.DBMSType != "" {
			return fmt.Sprintf("restore failed on %s (destination: %s): %v", e.DBMSType, e.Destination, e.Err)
		}
		return fmt.Sprintf("restore operation failed for destination '%s': %v", e.Destination, e.Err)

	case OperationTransfer:
		mode := "direct"
		if e.IsRelayMode {
			mode = "relay"
		}
		return fmt.Sprintf("transfer failed (%s, %s mode): %s → %s: %v", e.Method, mode, e.Source, e.Destination, e.Err)

	case OperationDump:
		return fmt.Sprintf("dump failed on %s (source: %s): %v", e.DBMSType, e.Source, e.Err)

	case OperationConnect:
		return fmt.Sprintf("connection failed to %s (source: %s): %v", e.DBMSType, e.Source, e.Err)

	case OperationRollback:
		return fmt.Sprintf("rollback failed on %s (destination: %s): %v", e.DBMSType, e.Destination, e.Err)

	default:
		if e.DBMSType != "" {
			return fmt.Sprintf("operation %q failed on %s: %v", e.Operation, e.DBMSType, e.Err)
		}
		return fmt.Sprintf("operation '%s' failed: %v", e.Operation, e.Err)
	}
}

func (e *OperationError) Unwrap() error { return e.Err }

// GetOutput returns the captured command output, for diagnostics.
func (e *OperationError) GetOutput() string { return e.Output }

// GetMethod returns the transfer method (storage transfers only).
func (e *OperationError) GetMethod() string { return e.Method }

// IsOperation reports whether the error describes the given operation.
func (e *OperationError) IsOperation(operation string) bool { return e.Operation == operation }

// =============================================================================
// ObjectError
// =============================================================================

// ObjectError names the object being processed when an operation failed, and
// the action being taken on it.
//
// It is the layer between MigrationError, which says which step of the
// pipeline stopped, and OperationError, which says which endpoint refused:
// "the restore failed" and "the target server said 1227" together still leave
// an operator reading a dump file to find out which of three hundred objects
// it was. Drivers wrap their per-object loops with this so the answer is in
// the message, and — via errors.As — in a field a caller can put in an API
// response.
type ObjectError struct {
	// Kind is the object's type: ObjectKind* here, or the matching
	// ObjectKind* value from dbmsx/filter for the kinds it defines.
	Kind string

	// Owner is the enclosing database, schema or bucket. Optional.
	Owner string

	// Name is the object's own name.
	Name string

	// Action is what was being done to it, one of the Action* constants.
	Action string

	// Index and Total place the object within its batch, 1-based. Both are
	// zero when the position is not known. Unit names what Index counts;
	// empty means objects of Kind, so a restore that counts statements
	// rather than views sets it to "statement".
	Index int
	Total int
	Unit  string

	Err error
}

func (e *ObjectError) Error() string {
	action := e.Action
	if action == "" {
		action = "operation"
	}
	if pos := e.position(); pos != "" {
		return fmt.Sprintf("%s failed on %s (%s): %v", action, e.subject(), pos, e.Err)
	}
	return fmt.Sprintf("%s failed on %s: %v", action, e.subject(), e.Err)
}

func (e *ObjectError) Unwrap() error { return e.Err }

// subject renders the object as "kind owner.name", omitting whichever parts
// are unset. An error with neither kind nor name still reads sensibly.
func (e *ObjectError) subject() string {
	name := e.Name
	if name != "" && e.Owner != "" {
		name = e.Owner + "." + name
	}
	switch {
	case e.Kind != "" && name != "":
		return e.Kind + " " + name
	case name != "":
		return name
	case e.Kind != "":
		return e.Kind
	default:
		return "object"
	}
}

// position renders "3/12", or "statement 47/312" when Unit names the count.
func (e *ObjectError) position() string {
	if e.Index <= 0 {
		return ""
	}
	pos := strconv.Itoa(e.Index)
	if e.Total > 0 {
		pos += "/" + strconv.Itoa(e.Total)
	}
	if e.Unit != "" {
		return e.Unit + " " + pos
	}
	return pos
}

// =============================================================================
// Obj
// =============================================================================

// Obj identifies one object so that a loop can name it in every error it
// returns without repeating the fields at each return.
//
//	obj := core.Obj{Kind: filter.ObjectKindView, Owner: database, Name: view,
//		Index: i + 1, Total: len(views)}
//	if err != nil {
//		return obj.Fail(core.ActionReadDDL, err)
//	}
type Obj struct {
	Kind  string
	Owner string
	Name  string
	Index int
	Total int
	Unit  string
}

// Fail wraps err with the object and the action that was being taken on it.
// A nil err yields nil, so a caller may hand it a result unconditionally.
func (o Obj) Fail(action string, err error) error {
	if err == nil {
		return nil
	}
	return &ObjectError{
		Kind:   o.Kind,
		Owner:  o.Owner,
		Name:   o.Name,
		Action: action,
		Index:  o.Index,
		Total:  o.Total,
		Unit:   o.Unit,
		Err:    err,
	}
}
