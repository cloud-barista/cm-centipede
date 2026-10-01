package executor

import (
	"context"
	"fmt"
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Compile-time Interface Assertion
// =============================================================================

var _ Executor = (*RestoreExecutor)(nil)

// =============================================================================
// RestoreExecutor
// =============================================================================

// RestoreExecutor restores a local staging file into a destination DBMS.
// It wraps DBDriver.Restore and maps failures to *MigrationError{Stage: StageRestore}.
type RestoreExecutor struct {
	driver    drvpkg.DBDriver
	scope     string
	inputPath string
}

// NewRestoreExecutor creates a RestoreExecutor for the given destination location and scope.
// The driver is resolved from loc.DBMSType via NewDBDriver.
//
// inputPath is the staging file to read, which is the paired DumpExecutor's
// OutputPath. It is passed in rather than recomputed: the two used to agree by
// both applying the same formula to the same source, which made every dump of a
// given database and scope land on one shared path. They now agree because there
// is one path and it travels from the side that created it.
func NewRestoreExecutor(loc drvpkg.DBMSLocation, scope, inputPath string) (*RestoreExecutor, error) {
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	if inputPath == "" {
		return nil, fmt.Errorf("restore executor: inputPath is required (pass the paired DumpExecutor's OutputPath)")
	}
	return &RestoreExecutor{
		driver:    drv,
		scope:     scope,
		inputPath: inputPath,
	}, nil
}

// Execute restores the staging file written by the paired DumpExecutor into destination.
// On failure it returns *MigrationError{Stage: StageRestore, Err: <underlying>}.
func (e *RestoreExecutor) Execute(ctx context.Context, _, destination drvpkg.DBMSLocation) error {
	inputPath := e.inputPath
	logger.Info("restore execute started",
		slog.String("dbms", destination.DBMSType),
		slog.String("database", destination.Database),
		slog.String("inputPath", inputPath),
	)
	if err := e.driver.Restore(ctx, destination, inputPath, nil); err != nil {
		logger.Error("restore execute failed", slog.String("err", err.Error()))
		return &MigrationError{Stage: StageRestore, Err: err}
	}
	logger.Info("restore execute completed", slog.String("database", destination.Database))
	return nil
}
