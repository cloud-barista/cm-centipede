package executor

import (
	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Executor Method Constants
// =============================================================================

// Executor connection method identifiers.
const (
	MethodDirect    = "direct"
	MethodSSHTunnel = "ssh-tunnel"
)

// =============================================================================
// Executor Interface
// =============================================================================

// Executor carries out a single migration step between a source and destination.
// Implementations include DumpExecutor, RestoreExecutor, and PipeExecutor.
//
// It is core.Executor over DBMSLocation: Execute must not modify the source, must
// honour the context, and must return a MigrationError wrapping any failure so
// callers can identify the pipeline stage via errors.As.
type Executor = core.Executor[drvpkg.DBMSLocation]
