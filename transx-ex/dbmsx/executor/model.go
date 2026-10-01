package executor

import (
	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Pipeline / Step Name Constants
// =============================================================================

const (
	PipelineDBMSTransfer = "dbms-transfer"
	StepDumpSource       = "dump-source"
	StepRestoreTarget    = "restore-target"
	StepDirectPipe       = "direct-pipe"
)

// =============================================================================
// Pipeline and Step
// =============================================================================

// Pipeline is an ordered sequence of migration Steps produced by the planner.
// It is core.Pipeline over DBMSLocation; its Strategy field carries the
// migration scope (schema-only, data-only, full) the planner resolved.
type Pipeline = core.Pipeline[drvpkg.DBMSLocation]

// Step is a single unit of work within a Pipeline.
type Step = core.Step[drvpkg.DBMSLocation]
