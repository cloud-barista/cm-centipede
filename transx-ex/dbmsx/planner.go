package dbmsx

import (
	"fmt"
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
	execpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/executor"
)

// =============================================================================
// Plan — Public Entry Point
// =============================================================================

// Plan validates model, normalises the scope, and returns a ready-to-run Pipeline.
//
// Routing logic:
//   - source ssh-tunnel AND destination ssh-tunnel → planDirectPipe
//     (single step, no local staging file)
//   - all other combinations → planDBMSTransfer
//     (two steps: dump to staging file, restore from staging file)
func Plan(model DBMSMigrationModel) (*Pipeline, error) {
	if err := Validate(model); err != nil {
		return nil, err
	}
	if model.Scope == "" {
		model.Scope = ScopeFull
	}
	if model.Source.IsSSHTunnel() && model.Destination.IsSSHTunnel() {
		logger.Debug("plan routing", slog.String("strategy", "direct-pipe"))
		return planDirectPipe(model)
	}
	logger.Debug("plan routing", slog.String("strategy", "staging-transfer"))
	return planDBMSTransfer(model)
}

// =============================================================================
// planDirectPipe — SSH-tunnel to SSH-tunnel (no staging file)
// =============================================================================

// planDirectPipe creates a single-step Pipeline that streams the dump command's
// stdout directly into the restore command's stdin over two concurrent SSH sessions.
// No local file is written; data flows through an in-process io.Pipe.
func planDirectPipe(model DBMSMigrationModel) (*Pipeline, error) {
	drv, err := drvpkg.NewDBDriver(model.Source.DBMSType)
	if err != nil {
		return nil, fmt.Errorf("planDirectPipe: resolve driver: %w", err)
	}

	dumpCmd := drv.BuildDumpCmd(model.Source, model.Scope)
	restoreCmd := drv.BuildRestoreCmd(model.Destination)

	pipeExec := execpkg.NewPipeExecutor(
		dumpCmd, model.Source.SSHTunnel.SSH,
		restoreCmd, model.Destination.SSHTunnel.SSH,
	)

	return &Pipeline{
		Name:     PipelineDBMSTransfer,
		Strategy: model.Scope,
		Steps: []Step{
			{
				Name:        StepDirectPipe,
				Source:      model.Source,
				Destination: model.Destination,
				Executor:    pipeExec,
			},
		},
	}, nil
}

// =============================================================================
// planDBMSTransfer — staging-file transfer (all other access type combinations)
// =============================================================================

// planDBMSTransfer creates a two-step Pipeline:
//  1. DumpExecutor writes the source database to a local staging file.
//  2. RestoreExecutor reads the staging file and restores it to the destination.
//
// The dump executor creates the staging file and the restore executor is handed
// its path, so the two agree on one file that exists rather than on a formula
// they each apply. See executor.NewStagingFile for why that matters.
func planDBMSTransfer(model DBMSMigrationModel) (*Pipeline, error) {
	dumpExec, err := execpkg.NewDumpExecutor(model.Source, model.Scope, dumpOption(model))
	if err != nil {
		return nil, fmt.Errorf("planDBMSTransfer: create dump executor: %w", err)
	}
	stagingPath := dumpExec.OutputPath()

	restoreExec, err := execpkg.NewRestoreExecutor(model.Destination, model.Scope, stagingPath)
	if err != nil {
		dumpExec.Cleanup()
		return nil, fmt.Errorf("planDBMSTransfer: create restore executor: %w", err)
	}

	intermediate := createLocalStagingLocation(stagingPath)

	return &Pipeline{
		Name:     PipelineDBMSTransfer,
		Strategy: model.Scope,
		// The dump file is an intermediate between the two steps and nothing reads
		// it afterwards, while what it holds is the source database in full, in
		// plain text on local disk. Execute runs this however the pipeline ended,
		// so an aborted restore leaves no partial dump behind either.
		Cleanup: dumpExec.Cleanup,
		Steps: []Step{
			{
				Name:        StepDumpSource,
				Source:      model.Source,
				Destination: intermediate,
				Executor:    dumpExec,
			},
			{
				Name:        StepRestoreTarget,
				Source:      model.Source,
				Destination: model.Destination,
				Executor:    restoreExec,
			},
		},
	}, nil
}

// =============================================================================
// Helpers
// =============================================================================

// dumpOption turns the two sides of model into the choices the dump is written
// under. It returns nil when there is nothing to say, which is every migration
// that keeps the source's schema names — that is, all of them outside
// PostgreSQL, and most within it.
//
// This is the one place the positional pairing of Validate's rule table is
// resolved. The driver receives a map from source schema to destination schema
// and never has to line up two lists itself, so the pairing cannot be read one
// way here and another way there.
func dumpOption(model DBMSMigrationModel) *DumpOption {
	rename := pgSchemaRename(model)
	if len(rename) == 0 {
		return nil
	}
	return &DumpOption{PgSchemaRename: rename}
}

// pgSchemaRename pairs source schemas with destination schemas by position, and
// keeps only the pairs that actually change a name.
//
// It returns nil unless the destination names schemas explicitly. Validate has
// already established that the two lists line up — equal lengths with the source
// spelled out, or a single destination schema — so the only case left to handle
// here is the second one, where the source list is empty and the driver resolves
// it against the database. That pairing cannot be made until the schema is
// known, so it is left to the driver: a one-schema selection has exactly one
// candidate, and the driver renames whatever it finds.
func pgSchemaRename(model DBMSMigrationModel) map[string]string {
	src, dst := model.Source, model.Destination
	if src.DBMSType != DBMSTypePostgreSQL || len(dst.PgSchema) == 0 {
		return nil
	}
	if len(src.PgSchema) != len(dst.PgSchema) {
		return nil
	}
	rename := make(map[string]string, len(src.PgSchema))
	for i, from := range src.PgSchema {
		if to := dst.PgSchema[i]; to != from {
			rename[from] = to
		}
	}
	if len(rename) == 0 {
		return nil
	}
	return rename
}

// createLocalStagingLocation returns a placeholder DBMSLocation that represents
// the local staging file at stagingPath.  It is used as the Destination of the
// dump Step so that pipeline metadata is self-describing.  DumpExecutor ignores
// the destination parameter at execution time.
func createLocalStagingLocation(stagingPath string) DBMSLocation {
	return DBMSLocation{
		DBMSType:   "local",
		Database:   stagingPath,
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
}
