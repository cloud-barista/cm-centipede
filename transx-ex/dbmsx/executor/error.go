package executor

import "github.com/cloud-barista/cm-centipede/transx-ex/core"

// =============================================================================
// Shared error vocabulary (core)
// =============================================================================

// Stage constants are declared once in core and shared with the storage domain.
const (
	StageDump    = core.StageDump
	StageRestore = core.StageRestore
)

// MigrationError wraps a lower-level error with the pipeline stage at which it
// occurred. See core.MigrationError.
type MigrationError = core.MigrationError
