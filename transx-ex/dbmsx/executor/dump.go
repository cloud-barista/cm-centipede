package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Compile-time Interface Assertion
// =============================================================================

var _ Executor = (*DumpExecutor)(nil)

// =============================================================================
// DumpExecutor
// =============================================================================

// DumpExecutor dumps a source DBMS to a local staging file.
// It wraps DBDriver.Dump and maps failures to *MigrationError{Stage: StageDump}.
type DumpExecutor struct {
	driver     drvpkg.DBDriver
	scope      string
	outputPath string
	cleanup    func()
	opt        *drvpkg.DumpOption
}

// NewDumpExecutor creates a DumpExecutor for the given source location and scope.
// It resolves the driver via NewDBDriver and creates the staging file the dump
// will be written to — one of its own, see NewStagingFile.
//
// The path is the executor's to hand out rather than to recompute: OutputPath
// gives it to the RestoreExecutor that reads the dump back, and Cleanup removes
// it once nobody needs it.
//
// opt carries the choices that depend on where the dump is headed — currently a
// PostgreSQL schema rename — and may be nil, which dumps the source as it stands.
func NewDumpExecutor(loc drvpkg.DBMSLocation, scope string, opt *drvpkg.DumpOption) (*DumpExecutor, error) {
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := NewStagingFile(loc.DBMSType, loc.Database, scope)
	if err != nil {
		return nil, err
	}
	return &DumpExecutor{
		driver:     drv,
		scope:      scope,
		outputPath: path,
		cleanup:    cleanup,
		opt:        opt,
	}, nil
}

// Cleanup removes the staging file this executor created. Safe to call more than
// once, and safe to call whether or not the dump ever ran.
func (e *DumpExecutor) Cleanup() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// Execute dumps the source database to the local staging file.
// The destination parameter is accepted for interface compatibility but is not used;
// all routing decisions are made from the source location.
// On failure it returns *MigrationError{Stage: StageDump, Err: <underlying>}.
func (e *DumpExecutor) Execute(ctx context.Context, source, _ drvpkg.DBMSLocation) error {
	logger.Info("dump execute started",
		slog.String("dbms", source.DBMSType),
		slog.String("database", source.Database),
		slog.String("scope", e.scope),
		slog.String("outputPath", e.outputPath),
	)
	if err := e.driver.Dump(ctx, source, e.scope, e.outputPath, nil, e.opt); err != nil {
		logger.Error("dump execute failed", slog.String("err", err.Error()))
		return &MigrationError{Stage: StageDump, Err: err}
	}
	logger.Info("dump execute completed", slog.String("outputPath", e.outputPath))
	return nil
}

// OutputPath returns the staging file path that Execute will write to.
// Callers (typically RestoreExecutor) use this to locate the dump for restoration.
func (e *DumpExecutor) OutputPath() string { return e.outputPath }

// =============================================================================
// Staging Path Helper
// =============================================================================

// stagingFilePattern returns the os.CreateTemp pattern a dump for the given
// dbmsType, database and scope is written under: the database and scope up
// front so a directory listing is readable, then the random part, then the
// extension. MongoDB archives use .archive; all other engines use .sql.
func stagingFilePattern(dbmsType, database, scope string) string {
	ext := ".sql"
	if dbmsType == drvpkg.DBMSTypeMongoDB {
		ext = ".archive"
	}
	return fmt.Sprintf("%s-%s-*%s", database, scope, ext)
}

// NewStagingFile creates the local file one migration dumps into, and returns
// its path together with the function that removes it.
//
// EVERY DUMP GETS A FILE OF ITS OWN. The name used to be derived from the
// database and the scope alone, which made it deterministic — and that was the
// point: DumpExecutor and RestoreExecutor computed it separately and agreed
// because the formula agreed. It also meant two migrations of the same database
// at the same scope shared one file, so one would overwrite the other's dump
// mid-restore. os.CreateTemp settles it by naming and creating in one atomic
// step; the path is passed from the dump side to the restore side instead of
// being recomputed.
//
// Creating the file here rather than letting the driver do it also pins the mode
// at 0600. A dump is the source database in full, in plain text, and the drivers
// reach it through os.Create or a shell redirect — both of which would leave it
// world-readable.
//
// The returned cleanup is best effort and silent: a staging file that will not
// delete is not something the caller of a finished migration can act on, and it
// must never mask the migration's own result.
func NewStagingFile(dbmsType, database, scope string) (string, func(), error) {
	if err := os.MkdirAll(drvpkg.DefaultStagingPath, 0o750); err != nil {
		return "", nil, fmt.Errorf("create staging directory %q: %w", drvpkg.DefaultStagingPath, err)
	}

	f, err := os.CreateTemp(drvpkg.DefaultStagingPath, stagingFilePattern(dbmsType, database, scope))
	if err != nil {
		return "", nil, fmt.Errorf("create staging file in %q: %w", drvpkg.DefaultStagingPath, err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("close staging file %q: %w", path, err)
	}

	return path, func() { _ = os.Remove(path) }, nil
}
