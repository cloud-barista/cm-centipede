package migration

import (
	"github.com/cloud-barista/cm-centipede/data-mig"
	"github.com/cloud-barista/cm-centipede/pkg/config"
)

// ResolveConcurrency returns the concurrency a migration runs with: the one its
// request named, else the server's configured default. Zero means neither was
// set, and data-mig then runs at its own default (datamig.DefaultConcurrency).
//
// It is resolved at execution rather than stored at creation, like the staging
// root (ResolveStagingDir), so a retry of a migration that named none follows
// the configuration in effect when the retry runs.
func ResolveConcurrency(requested int) int {
	if requested > 0 {
		return requested
	}
	return config.Conf.Migration.Concurrency
}

// CheckConcurrency checks a concurrency a request or the configuration names:
// 0 for the default, or 1 to datamig.MaxConcurrency. The error names the
// field itself ("concurrency 33 is out of range ...").
func CheckConcurrency(n int) error {
	return datamig.ValidateConcurrency(n)
}

// concurrencyReport is what an item ran at, as data-mig reported it: zero
// when it did not report.
type concurrencyReport struct {
	n    int
	note string
}
