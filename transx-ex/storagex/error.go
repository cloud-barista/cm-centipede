package storagex

import (
	"fmt"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/fieldsec"
)

// ============================================================================
// Encryption Errors (re-exported from fieldsec)
// ============================================================================

var (
	// ErrKeyNotFound is returned when the requested key is not in the store.
	ErrKeyNotFound = fieldsec.ErrKeyNotFound

	// ErrKeyExpired is returned when the key has expired.
	ErrKeyExpired = fieldsec.ErrKeyExpired

	// ErrKeyMismatch is returned when the key ID doesn't match.
	ErrKeyMismatch = fieldsec.ErrKeyMismatch

	// ErrDecryptionFailed is returned when decryption fails.
	ErrDecryptionFailed = fieldsec.ErrDecryptionFailed

	// ErrInvalidPublicKey is returned when public key parsing fails.
	ErrInvalidPublicKey = fieldsec.ErrInvalidPublicKey
)

// ============================================================================
// Shared error vocabulary (core)
// ============================================================================

// Migration stages. Declared once in core and shared with the DBMS domain.
const (
	StageBackup   = core.StageBackup
	StageTransfer = core.StageTransfer
	StageRestore  = core.StageRestore
)

// Operation types. Declared once in core and shared with the DBMS domain.
const (
	OperationBackup   = core.OperationBackup
	OperationRestore  = core.OperationRestore
	OperationTransfer = core.OperationTransfer
	OperationPreCmd   = core.OperationPreCmd
	OperationPostCmd  = core.OperationPostCmd
)

// MigrationError reports the pipeline stage a migration failed at.
// See core.MigrationError.
type MigrationError = core.MigrationError

// OperationError provides detailed context about a failed transfer operation.
// See core.OperationError; storage code fills Method and IsRelayMode.
type OperationError = core.OperationError

// ObjectError names the file or object being processed when a transfer failed.
// See core.ObjectError; Obj builds one for a loop.
type ObjectError = core.ObjectError

// Obj identifies one file or object across every error a transfer loop returns.
type Obj = core.Obj

// Actions the storage domain takes on a file or object.
const (
	ActionList     = core.ActionList
	ActionUpload   = core.ActionUpload
	ActionDownload = core.ActionDownload
	ActionInspect  = core.ActionInspect
)

// Object kinds the storage domain names.
const (
	ObjectKindFile   = core.ObjectKindFile
	ObjectKindObject = core.ObjectKindObject
	ObjectKindBucket = core.ObjectKindBucket
)

// ============================================================================
// Storage-Specific Errors
// ============================================================================

// UnsupportedTransferError indicates that no executor is available for the given
// transfer combination.
type UnsupportedTransferError struct {
	SourceMethod      string
	DestinationMethod string
}

func (e *UnsupportedTransferError) Error() string {
	return fmt.Sprintf("unsupported transfer: %s -> %s (no registered executor)", e.SourceMethod, e.DestinationMethod)
}
