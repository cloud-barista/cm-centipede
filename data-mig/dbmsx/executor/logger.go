package executor

import "log/slog"

// logger is the package-level structured logger. Defaults to slog.Default().
// Override via SetLogger before any executor operations.
var logger = slog.Default()

// SetLogger replaces the package-level logger. Call once at program startup.
func SetLogger(l *slog.Logger) { logger = l }
