package dbmsx

import (
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
	execpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/executor"
)

// logger is the package-level structured logger. Defaults to slog.Default().
var logger = slog.Default()

// SetLogger injects l as the structured logger for dbmsx and its subpackages
// (driver, executor). Call once at program startup, before any migration operations.
//
// Example — zerolog bridge:
//
//	dbmsx.SetLogger(slog.New(zerologHandler))
func SetLogger(l *slog.Logger) {
	logger = l
	drvpkg.SetLogger(l)
	execpkg.SetLogger(l)
}
