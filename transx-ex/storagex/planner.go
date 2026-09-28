package storagex

import (
	"fmt"
	"os"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// ============================================================================
// Pipeline Definition
// ============================================================================

// Pipeline represents a planned transfer with multiple steps.
// It is core.Pipeline over DataLocation; its Strategy field carries the
// transfer strategy (auto, direct, relay) the planner resolved.
type Pipeline = core.Pipeline[DataLocation]

// Step represents a single transfer step in the pipeline.
type Step = core.Step[DataLocation]

// ============================================================================
// Plan Function: Single Entry Point
// ============================================================================

// Plan analyzes the DataMigrationModel and returns the optimal transfer Pipeline.
// The routing is based on StorageType combinations (3 cases only).
func Plan(model DataMigrationModel) (*Pipeline, error) {
	if err := Validate(model); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	srcStorage := model.Source.StorageType
	dstStorage := model.Destination.StorageType

	switch {
	// Case 1: filesystem ↔ filesystem
	case srcStorage == StorageTypeFilesystem && dstStorage == StorageTypeFilesystem:
		return planFilesystemTransfer(model)

	// Case 2: objectstorage ↔ objectstorage
	case srcStorage == StorageTypeObjectStorage && dstStorage == StorageTypeObjectStorage:
		return planObjectStorageTransfer(model)

	// Case 3: Cross-storage (filesystem ↔ objectstorage)
	default:
		return planCrossStorageTransfer(model)
	}
}

// ============================================================================
// Filesystem Transfer (rsync-based)
// ============================================================================

// planFilesystemTransfer handles all filesystem ↔ filesystem transfers.
// Supported scenarios:
//   - local→ssh (Push): rsync from local to remote
//   - ssh→local (Pull): rsync from remote to local
//   - ssh→ssh with strategy "relay": Pull to local staging, then Push to destination
//   - ssh→ssh with strategy "agent-forward" or "auto": AgentForward mode
//
// Note: local-to-local is not supported (use standard file copy utilities).
func planFilesystemTransfer(model DataMigrationModel) (*Pipeline, error) {
	srcIsRemote := model.Source.Filesystem != nil && model.Source.Filesystem.SSH != nil
	dstIsRemote := model.Destination.Filesystem != nil && model.Destination.Filesystem.SSH != nil

	// SSH → SSH with relay strategy: use local staging
	if srcIsRemote && dstIsRemote && model.Strategy == StrategyRelay {
		return planRelayFilesystemTransfer(model)
	}

	// Default: direct transfer (Pull, Push, or AgentForward)
	rsyncExec, err := NewRsyncExecutor(model.Source, model.Destination)
	if err != nil {
		return nil, fmt.Errorf("failed to create rsync executor: %w", err)
	}

	return &Pipeline{
		Name:     PipelineFilesystemTransfer,
		Strategy: model.Strategy,
		Steps: []Step{
			{
				Name:        StepRsyncTransfer,
				Source:      model.Source,
				Destination: model.Destination,
				Executor:    rsyncExec,
			},
		},
	}, nil
}

// planRelayFilesystemTransfer creates a two-step transfer via local staging.
// Step 1: Pull from remote source to local staging
// Step 2: Push from local staging to remote destination
func planRelayFilesystemTransfer(model DataMigrationModel) (*Pipeline, error) {
	stagingLoc, cleanup, err := newStagingLocation()
	if err != nil {
		return nil, err
	}

	// Step 1: Pull (SSH source → local staging)
	pullExec, err := NewRsyncExecutor(model.Source, stagingLoc)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create pull executor: %w", err)
	}

	// Step 2: Push (local staging → SSH destination)
	pushExec, err := NewRsyncExecutor(stagingLoc, model.Destination)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create push executor: %w", err)
	}

	return &Pipeline{
		Name:     PipelineFilesystemTransfer,
		Strategy: model.Strategy,
		Cleanup:  cleanup,
		Steps: []Step{
			{
				Name:        "pull-to-staging",
				Source:      model.Source,
				Destination: stagingLoc,
				Executor:    pullExec,
			},
			{
				Name:        "push-from-staging",
				Source:      stagingLoc,
				Destination: model.Destination,
				Executor:    pushExec,
			},
		},
	}, nil
}

// ============================================================================
// Object Storage Transfer (relay-based)
// ============================================================================

// planObjectStorageTransfer handles objectstorage ↔ objectstorage transfers.
// Always uses relay via local staging: S3 → local → S3
func planObjectStorageTransfer(model DataMigrationModel) (*Pipeline, error) {
	stagingLoc, cleanup, err := newStagingLocation()
	if err != nil {
		return nil, err
	}

	// Create providers for source and destination
	srcProvider, err := NewS3Provider(model.Source)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to create source S3 provider: %w", err)
	}
	dstProvider, err := NewS3Provider(model.Destination)
	if err != nil {
		_ = CloseS3Provider(srcProvider)
		cleanup()
		return nil, fmt.Errorf("failed to create destination S3 provider: %w", err)
	}

	srcS3Exec := NewS3Executor(srcProvider)
	dstS3Exec := NewS3Executor(dstProvider)

	return &Pipeline{
		Name:     PipelineObjectStorageTransfer,
		Strategy: model.Strategy,
		Cleanup:  withProviders(cleanup, srcProvider, dstProvider),
		Steps: []Step{
			{
				Name:        StepDownloadFromS3,
				Source:      model.Source,
				Destination: stagingLoc,
				Executor:    srcS3Exec,
			},
			{
				Name:        StepUploadToS3,
				Source:      stagingLoc,
				Destination: model.Destination,
				Executor:    dstS3Exec,
			},
		},
	}, nil
}

// ============================================================================
// Cross-Storage Transfer
// ============================================================================

// planCrossStorageTransfer handles filesystem ↔ objectstorage transfers.
// Determines direction and whether relay is needed.
func planCrossStorageTransfer(model DataMigrationModel) (*Pipeline, error) {
	srcIsFS := model.Source.IsFilesystem()

	if srcIsFS {
		// filesystem → objectstorage
		return planFilesystemToObjectStorage(model)
	}
	// objectstorage → filesystem
	return planObjectStorageToFilesystem(model)
}

// planFilesystemToObjectStorage handles filesystem → objectstorage.
// If source is SSH, uses relay: ssh → local → s3
// If source is local, direct: local → s3
func planFilesystemToObjectStorage(model DataMigrationModel) (*Pipeline, error) {
	s3Provider, err := NewS3Provider(model.Destination)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 provider: %w", err)
	}
	s3Exec := NewS3Executor(s3Provider)

	// Direct transfer if source is local filesystem
	if model.Source.IsLocal() {
		return &Pipeline{
			Name:     PipelineCrossStorageTransfer,
			Strategy: model.Strategy,
			Cleanup:  withProviders(nil, s3Provider),
			Steps: []Step{
				{
					Name:        StepUploadToS3,
					Source:      model.Source,
					Destination: model.Destination,
					Executor:    s3Exec,
				},
			},
		}, nil
	}

	// Relay transfer: ssh → local → s3
	stagingLoc, cleanup, err := newStagingLocation()
	if err != nil {
		_ = CloseS3Provider(s3Provider)
		return nil, err
	}
	rsyncExec, err := NewRsyncExecutor(model.Source, stagingLoc)
	if err != nil {
		_ = CloseS3Provider(s3Provider)
		cleanup()
		return nil, fmt.Errorf("failed to create rsync executor: %w", err)
	}

	return &Pipeline{
		Name:     PipelineCrossStorageTransfer,
		Strategy: model.Strategy,
		Cleanup:  withProviders(cleanup, s3Provider),
		Steps: []Step{
			{
				Name:        StepRsyncFromServer,
				Source:      model.Source,
				Destination: stagingLoc,
				Executor:    rsyncExec,
			},
			{
				Name:        StepUploadToS3,
				Source:      stagingLoc,
				Destination: model.Destination,
				Executor:    s3Exec,
			},
		},
	}, nil
}

// planObjectStorageToFilesystem handles objectstorage → filesystem.
// If destination is SSH, uses relay: s3 → local → ssh
// If destination is local, direct: s3 → local
func planObjectStorageToFilesystem(model DataMigrationModel) (*Pipeline, error) {
	s3Provider, err := NewS3Provider(model.Source)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 provider: %w", err)
	}
	s3Exec := NewS3Executor(s3Provider)

	// Direct transfer if destination is local filesystem
	if model.Destination.IsLocal() {
		return &Pipeline{
			Name:     PipelineCrossStorageTransfer,
			Strategy: model.Strategy,
			Cleanup:  withProviders(nil, s3Provider),
			Steps: []Step{
				{
					Name:        StepDownloadFromS3,
					Source:      model.Source,
					Destination: model.Destination,
					Executor:    s3Exec,
				},
			},
		}, nil
	}

	// Relay transfer: s3 → local → ssh
	stagingLoc, cleanup, err := newStagingLocation()
	if err != nil {
		_ = CloseS3Provider(s3Provider)
		return nil, err
	}
	rsyncExec, err := NewRsyncExecutor(stagingLoc, model.Destination)
	if err != nil {
		_ = CloseS3Provider(s3Provider)
		cleanup()
		return nil, fmt.Errorf("failed to create rsync executor: %w", err)
	}

	return &Pipeline{
		Name:     PipelineCrossStorageTransfer,
		Strategy: model.Strategy,
		Cleanup:  withProviders(cleanup, s3Provider),
		Steps: []Step{
			{
				Name:        StepDownloadFromS3,
				Source:      model.Source,
				Destination: stagingLoc,
				Executor:    s3Exec,
			},
			{
				Name:        StepRsyncToServer,
				Source:      stagingLoc,
				Destination: model.Destination,
				Executor:    rsyncExec,
			},
		},
	}, nil
}

// ============================================================================
// Helper Functions
// ============================================================================

// newStagingLocation creates a staging directory for one transfer and returns it
// as a local DataLocation together with the function that removes it again.
//
// EVERY RELAY GETS A DIRECTORY OF ITS OWN, and that is not merely tidiness. A
// relay's second step sends the whole staging directory onward — rsync pushes it
// to the destination host, the S3 executor uploads everything under it — so
// anything else sitting in that directory travels as though it were this
// transfer's data. Sharing one directory between transfers therefore means one
// transfer's leftovers arrive as another's payload, and the validation that
// follows reports them as files the destination has and the source does not.
//
// MkdirTemp is what makes it safe rather than merely unlikely: it names and
// creates in one atomic step, so two transfers planned at the same moment cannot
// be handed the same path. The directory goes under DefaultStagingPath rather
// than beside it, so concurrent relays stay collected in one place instead of
// scattering temporary directories across /tmp.
//
// The returned cleanup is best effort and deliberately silent. Neither this
// package nor core logs, a temporary directory that refuses to delete is not
// something the caller of a finished transfer can act on, and the one thing
// cleanup must never do is mask the transfer's own result — so it reports
// nothing.
func newStagingLocation() (DataLocation, func(), error) {
	if err := os.MkdirAll(DefaultStagingPath, 0o750); err != nil {
		return DataLocation{}, nil, fmt.Errorf("failed to create staging root %q: %w", DefaultStagingPath, err)
	}

	dir, err := os.MkdirTemp(DefaultStagingPath, "relay-")
	if err != nil {
		return DataLocation{}, nil, fmt.Errorf("failed to create staging directory: %w", err)
	}

	loc := DataLocation{
		StorageType: StorageTypeFilesystem,
		Path:        dir,
		Filesystem: &FilesystemAccess{
			AccessType: AccessTypeLocal,
		},
	}
	return loc, func() { _ = os.RemoveAll(dir) }, nil
}

// withProviders extends a pipeline's cleanup so it also closes the S3 providers
// the planner created. A tunnelled provider holds an SSH connection, and the
// pipeline's Cleanup is the one moment its owner is known to be finished with
// it. cleanup may be nil, and is returned unchanged when no provider holds
// anything, so a pipeline that allocated nothing still carries no Cleanup.
func withProviders(cleanup func(), providers ...S3Provider) func() {
	var held []S3Provider
	for _, p := range providers {
		if mp, ok := p.(*MinioProvider); ok && mp.tunnel != nil {
			held = append(held, p)
		}
	}
	if len(held) == 0 {
		return cleanup
	}

	return func() {
		for _, p := range held {
			_ = CloseS3Provider(p)
		}
		if cleanup != nil {
			cleanup()
		}
	}
}

// NewS3Provider creates an S3 provider from DataLocation.
func NewS3Provider(loc DataLocation) (S3Provider, error) {
	if !loc.IsObjectStorage() || loc.ObjectStorage == nil {
		return nil, fmt.Errorf("location is not object storage")
	}

	bucket, _ := ParseBucketAndKey(loc.Path)
	if bucket == "" {
		return nil, fmt.Errorf("bucket name is required in path")
	}

	osAccess := loc.ObjectStorage

	switch osAccess.AccessType {
	case AccessTypeMinio:
		if osAccess.Minio == nil {
			return nil, fmt.Errorf("minio S3 config is required")
		}
		// Azure Blob Storage answers on the same config block but does not speak
		// the S3 API, so it needs its own provider. The endpoint identifies it.
		if IsAzureBlobEndpoint(osAccess.Minio.Endpoint) {
			return NewAzureProvider(&AzureConfig{
				Endpoint:    osAccess.Minio.Endpoint,
				AccountName: osAccess.Minio.AccessKeyId,
				AccountKey:  osAccess.Minio.SecretAccessKey,
			}, bucket)
		}
		cfg := &MinioConfig{
			Endpoint:        osAccess.Minio.Endpoint,
			AccessKeyId:     osAccess.Minio.AccessKeyId,
			SecretAccessKey: osAccess.Minio.SecretAccessKey,
			Region:          osAccess.Minio.Region,
			UseSSL:          osAccess.Minio.UseSSL,
			BucketLookup:    osAccess.Minio.BucketLookup,
			SSHTunnel:       osAccess.Minio.SSHTunnel,
		}
		return NewMinioProvider(cfg, bucket)

	case AccessTypeSpider:
		if osAccess.Spider == nil {
			return nil, fmt.Errorf("Spider config is required")
		}
		return NewSpiderProvider(osAccess.Spider, bucket)

	case AccessTypeTumblebug:
		if osAccess.Tumblebug == nil {
			return nil, fmt.Errorf("Tumblebug config is required")
		}
		return NewTumblebugProvider(osAccess.Tumblebug)

	default:
		return nil, fmt.Errorf("unsupported object storage access type: %s", osAccess.AccessType)
	}
}
