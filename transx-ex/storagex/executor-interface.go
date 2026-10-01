package storagex

import (
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// Executor defines the interface for transfer operations.
// It is core.Executor over DataLocation: Execute performs the transfer from
// source to destination, honouring the context, and returns an error on failure.
//
// Two implementations cover the supported methods:
//   - RsyncExecutor: local and remote filesystem transfers, with SSH support
//   - S3Executor: object storage transfers using presigned URLs
type Executor = core.Executor[DataLocation]

// Transfer method constants
const (
	MethodLocal = "local" // Local filesystem transfer
	MethodSSH   = "ssh"   // Remote transfer via SSH/rsync
	MethodS3    = "s3"    // S3-compatible object storage
)

// Transfer category constants
const (
	CategoryRsync         = "rsync"          // rsync-based transfers (local/ssh)
	CategoryObjectStorage = "object-storage" // Object storage transfers (S3, etc.)
)

// IsRsyncMethod returns true if the method uses rsync for transfer.
func IsRsyncMethod(method string) bool {
	method = strings.ToLower(method)
	return method == MethodLocal || method == MethodSSH
}

// IsObjectStorageMethod returns true if the method uses object storage.
func IsObjectStorageMethod(method string) bool {
	method = strings.ToLower(method)
	return method == MethodS3 || strings.HasPrefix(method, "s3")
}

// GetCategory returns the transfer category for the given method.
func GetCategory(method string) string {
	if IsRsyncMethod(method) {
		return CategoryRsync
	}
	if IsObjectStorageMethod(method) {
		return CategoryObjectStorage
	}
	return ""
}
