package migration

import (
	"fmt"
	"os"

	"github.com/cloud-barista/cm-centipede/pkg/config"
)

// ResolveStagingDir returns the staging root a migration runs with: the one its
// request named, else the server's configured default. Empty means neither was
// set, and transx-ex then picks its own default ($TMPDIR/transxex-staging, or
// /tmp/transxex-staging when TMPDIR is unset).
//
// It is resolved at execution rather than stored at creation, so a retry of a
// migration that named no directory follows the configuration in effect when
// the retry runs.
func ResolveStagingDir(requested string) string {
	if requested != "" {
		return requested
	}
	return config.Conf.Migration.StagingDir
}

// CheckStagingDir creates dir if it is missing and proves a file can be written
// in it. Both callers want the failure up front: at start-up for the configured
// default, and on the request for one a migration names, rather than as every
// item of an asynchronous migration failing the same way.
func CheckStagingDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create staging directory %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("staging directory %q is not writable: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}
