package migration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
	"github.com/rs/zerolog/log"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
	"github.com/cloud-barista/cm-centipede/pkg/client/beetle"
	"github.com/cloud-barista/cm-centipede/pkg/client/honeybee"
	"github.com/cloud-barista/cm-centipede/pkg/connsec"
)

// ToTransxSSHConfig maps a beetle SSHConfig to a transx-ex SSHConfig.
// transx-ex supports key-based and SSH-agent auth only (no password field).
func ToTransxSSHConfig(cfg beetle.SSHConfig) *transxex.SSHConfig {
	return &transxex.SSHConfig{
		Host:       cfg.Host,
		Port:       cfg.Port,
		Username:   cfg.Username,
		PrivateKey: normalizePEMKey(cfg.PrivateKey),
	}
}

// normalizePEMKey strips whitespace around a PEM private key but keeps the
// newline that terminates its last line. OpenSSH rejects a key file without it
// ("error in libcrypto") and silently falls back to another auth method, which
// on an unattended rsync means waiting on a password prompt that never comes.
func normalizePEMKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

// FilesystemLoc builds a transxex.StorageLocation for a remote filesystem path.
// Pass nil filter to scan all entries with no depth limit.
func FilesystemLoc(sshCfg *transxex.SSHConfig, path string, filter *transxex.PathFilterOption) transxex.StorageLocation {
	return transxex.StorageLocation{
		StorageType: transxex.StorageTypeFilesystem,
		Path:        path,
		Filesystem: &transxex.FilesystemAccess{
			AccessType: transxex.AccessTypeSSH,
			SSH:        sshCfg,
		},
		Filter: filter,
	}
}

// ResolveSSHConfig resolves a ConnectionRef to a transx SSHConfig.
// Supports honeybee and beetleSsh (references) and ssh (inline).
// Inline credentials are decrypted first. transx SSH uses key-based auth only,
// so the private key is required for filesystem transfers.
func ResolveSSHConfig(ref commonmodel.ConnectionRef) (*transxex.SSHConfig, error) {
	ref, err := connsec.DecryptRef(ref)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection ref: %w", err)
	}

	switch ref.Source {
	case commonmodel.ConnectionSourceHoneybee:
		if ref.Honeybee == nil {
			return nil, fmt.Errorf("honeybee ref is nil")
		}
		cfg, err := honeybee.GetConnectionInfo(*ref.Honeybee)
		if err != nil {
			return nil, fmt.Errorf("get honeybee connection: %w", err)
		}
		return &transxex.SSHConfig{
			Host:       cfg.Host,
			Port:       cfg.Port,
			Username:   cfg.User,
			PrivateKey: normalizePEMKey(cfg.PrivateKey),
		}, nil
	case commonmodel.ConnectionSourceBeetleSSH:
		if ref.BeetleSSH == nil {
			return nil, fmt.Errorf("beetleSsh ref is nil")
		}
		cfg, err := beetle.GetSSHAccessInfo(*ref.BeetleSSH)
		if err != nil {
			return nil, fmt.Errorf("get beetle ssh access: %w", err)
		}
		return ToTransxSSHConfig(*cfg), nil
	case commonmodel.ConnectionSourceSSH:
		if ref.SSH == nil {
			return nil, fmt.Errorf("ssh config is nil")
		}
		return &transxex.SSHConfig{
			Host:       ref.SSH.Host,
			Port:       ref.SSH.Port,
			Username:   ref.SSH.Username,
			PrivateKey: normalizePEMKey(ref.SSH.PrivateKey),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported source type for SSH migration: %s", ref.Source)
	}
}

// resolveTransxLoc builds a transxex.StorageLocation for ref interpreted as the given
// storageType (commonmodel.StorageTypeFilesystem / commonmodel.StorageTypeObjectStorage).
// Used to resolve either side of a (possibly cross-storage) transfer. filter is
// attached to the returned location (pass nil for the destination side).
func resolveTransxLoc(ref commonmodel.ConnectionRef, storageType, path string, filter *transxex.PathFilterOption) (transxex.StorageLocation, error) {
	switch storageType {
	case commonmodel.StorageTypeObjectStorage:
		return ResolveObjectStorageLoc(ref, path, filter)
	case commonmodel.StorageTypeFilesystem, "":
		ssh, err := ResolveSSHConfig(ref)
		if err != nil {
			return transxex.StorageLocation{}, err
		}
		return FilesystemLoc(ssh, path, filter), nil
	default:
		return transxex.StorageLocation{}, fmt.Errorf("unknown storage type %q", storageType)
	}
}

// MigrateFileSystem transfers each FSMigrationInfo directory from a filesystem
// source to the destination. The destination is another filesystem (SSH→SSH) or,
// when m.DstType is objectstorage, an object-storage bucket (filesystem→S3 cross-
// storage; transx handles the ssh→local→S3 relay). Processes folders in ascending
// Order and emits a ProgressEvent per folder. Returns ctx.Err() on cancellation;
// per-folder transfer errors are reported via ProgressEvent only (continues).
func MigrateFileSystem(ctx context.Context, m targetmodel.MigrationFileSystemModel, progressCh chan<- ProgressEvent) error {
	srcSSH, err := ResolveSSHConfig(m.SrcConnection)
	if err != nil {
		return fmt.Errorf("resolve src connection: %w", err)
	}
	dstType := m.DstType
	if dstType == "" {
		dstType = commonmodel.StorageTypeFilesystem
	}
	// Normalise the transfer strategy; empty defaults to relay. Guards migrations
	// created directly (bypassing the plan layer) with an unset strategy.
	strategy := m.Strategy
	if strategy == "" {
		strategy = commonmodel.StrategyRelay
	}

	folders := make([]targetmodel.FSMigrationInfo, len(m.Folders))
	copy(folders, m.Folders)
	sort.Slice(folders, func(i, j int) bool {
		return folders[i].Order < folders[j].Order
	})

	for _, folder := range folders {
		select {
		case <-ctx.Done():
			progressCh <- ProgressEvent{
				ItemPath: folder.SrcPath,
				Status:   "cancelled",
				Err:      ctx.Err(),
			}
			return ctx.Err()
		default:
		}

		start := time.Now()

		filter, filterErr := ToTransxFilter(folder.Rules)
		if filterErr != nil {
			progressCh <- ProgressEvent{
				ItemPath:   folder.SrcPath,
				Status:     "failed",
				DurationMs: time.Since(start).Milliseconds(),
				Err:        filterErr,
			}
			continue
		}

		srcLoc := FilesystemLoc(srcSSH, folder.SrcPath, filter)
		dstLoc, dstErr := resolveTransxLoc(m.DstConnection, dstType, folder.DstPath, nil)
		if dstErr != nil {
			progressCh <- ProgressEvent{
				ItemPath:   folder.SrcPath,
				Status:     "failed",
				DurationMs: time.Since(start).Milliseconds(),
				Err:        fmt.Errorf("resolve dst connection: %w", dstErr),
			}
			continue
		}

		movedBytes, transferErr := runStorageTransfer(ctx, transxex.StorageMigrationModel{
			Source:      srcLoc,
			Destination: dstLoc,
			Strategy:    strategy,
		}, folder.SrcPath, progressCh)
		durationMs := time.Since(start).Milliseconds()

		status := "success"
		if transferErr != nil {
			status = "failed"
			log.Error().
				Str("srcPath", folder.SrcPath).
				Str("dstPath", folder.DstPath).
				Err(transferErr).
				Msg("filesystem folder transfer failed")
		}

		progressCh <- ProgressEvent{
			ItemPath:   folder.SrcPath,
			Status:     status,
			SizeBytes:  movedBytes,
			DurationMs: durationMs,
			Err:        transferErr,
		}
	}

	return nil
}
