package core

// StagingRoot is the local directory transx-ex stages data under when a transfer
// cannot go straight from source to destination — an object-storage-to-object-
// storage relay, or a database dump that has to land as a file before it is
// restored.
//
// Domains take a subdirectory of it rather than the root itself, so a filesystem
// relay and a database dump running at the same time cannot collide, and an
// operator clearing space can see which is which.
const StagingRoot = "/tmp/transxex-staging"

const (
	// StorageStagingPath holds files relayed between filesystems and buckets.
	StorageStagingPath = StagingRoot + "/storage"

	// DBMSStagingPath holds intermediate dump files (.sql, .archive).
	DBMSStagingPath = StagingRoot + "/dbms"
)
