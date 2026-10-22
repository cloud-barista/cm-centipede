package core

import (
	"os"
	"path/filepath"
)

// stagingDirName is the directory the default staging root is named after,
// created under the system temporary directory.
const stagingDirName = "transxex-staging"

// StagingRoot returns the local directory transx-ex stages data under when a
// transfer cannot go straight from source to destination — an object-storage-
// to-object-storage relay, or a database dump that has to land as a file before
// it is restored.
//
// dir is the root the caller asked for. Empty selects the default,
// $TMPDIR/transxex-staging, or /tmp/transxex-staging when TMPDIR is unset. The
// default is resolved on every call rather than once at start-up, so a change
// to TMPDIR is seen by the next transfer.
//
// Domains take a subdirectory of it rather than the root itself, so a filesystem
// relay and a database dump running at the same time cannot collide, and an
// operator clearing space can see which is which.
func StagingRoot(dir string) string {
	if dir != "" {
		return filepath.Clean(dir)
	}
	return filepath.Join(os.TempDir(), stagingDirName)
}

// StorageStagingPath returns the directory under the staging root that holds
// files relayed between filesystems and buckets. dir is as for StagingRoot.
func StorageStagingPath(dir string) string {
	return filepath.Join(StagingRoot(dir), "storage")
}

// DBMSStagingPath returns the directory under the staging root that holds
// intermediate dump files (.sql, .archive). dir is as for StagingRoot.
func DBMSStagingPath(dir string) string {
	return filepath.Join(StagingRoot(dir), "dbms")
}
