package storagex

import "log/slog"

// logger is the package-level structured logger. Defaults to slog.Default().
var logger = slog.Default()

// SetLogger injects l as the structured logger for storagex. Call once at
// program startup, before any migration operations.
func SetLogger(l *slog.Logger) {
	logger = l
}
