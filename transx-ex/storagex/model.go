package storagex

import (
	"fmt"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/filter"
)

// ============================================================================
// Storage Types: What kind of storage is being used
// ============================================================================

const (
	// StorageTypeFilesystem represents local or remote filesystem storage.
	StorageTypeFilesystem = "filesystem"

	// StorageTypeObjectStorage represents S3-compatible object storage.
	StorageTypeObjectStorage = "objectstorage"
)

// ============================================================================
// Access Types: How to access the storage
// ============================================================================

// Filesystem access types
const (
	// AccessTypeLocal represents local filesystem access (no network).
	AccessTypeLocal = "local"

	// AccessTypeSSH represents remote filesystem access via SSH/rsync.
	AccessTypeSSH = "ssh"
)

// Object Storage access types
const (
	// AccessTypeMinio represents direct S3 SDK access using minio-go.
	AccessTypeMinio = "minio"

	// AccessTypeSpider represents access via CB-Spider Object Storage API.
	AccessTypeSpider = "spider"

	// AccessTypeTumblebug represents access via CB-Tumblebug Object Storage API.
	AccessTypeTumblebug = "tumblebug"
)

// ============================================================================
// Transfer Strategies
// ============================================================================

const (
	// StrategyAuto automatically selects the best transfer method.
	StrategyAuto = "auto"

	// StrategyDirect forces direct transfer (e.g., SSH agent forwarding).
	StrategyDirect = "direct"

	// StrategyRelay forces relay via local machine.
	StrategyRelay = "relay"
)

// ============================================================================
// Pipeline and Step Names (for consistent naming)
// ============================================================================

const (
	PipelineFilesystemTransfer    = "filesystem-transfer"
	PipelineObjectStorageTransfer = "objectstorage-transfer"
	PipelineCrossStorageTransfer  = "cross-storage-transfer"

	StepRsyncTransfer   = "rsync-transfer"
	StepDownloadFromS3  = "download-from-s3"
	StepUploadToS3      = "upload-to-s3"
	StepRsyncFromServer = "rsync-from-server"
	StepRsyncToServer   = "rsync-to-server"
)

// ============================================================================
// Staging Configuration
// ============================================================================

const (
	// DefaultStagingPath is the default local staging directory for relay
	// transfers. It is the storage subdirectory of the module-wide staging
	// root, so a filesystem relay and a database dump never share a directory.
	DefaultStagingPath = core.StorageStagingPath
)

// ============================================================================
// Filter Options
// ============================================================================

// FilterOption defines file filtering options for transfers and inspect.
// It is an alias for filter.Option, an ordered filter pipeline (see the filter
// package). The simple Include/Exclude fields apply only when Rules is empty,
// and are normalised into glob rules.
type FilterOption = filter.Option

// FilterItem is the metadata of one file, directory or object key that a
// FilterOption evaluates. Exported so a caller that has to decide the same
// include/exclude question outside a transfer — validation re-reading both ends,
// for one — asks the pipeline itself rather than reimplementing its semantics.
type FilterItem = filter.Item

// ============================================================================
// Metric Options
// ============================================================================

// MetricOption selects which information inspect extracts. It mirrors the
// Filesystem/ObjectStorage split of the access configuration so filesystem and
// object storage metrics stay separated.
type MetricOption struct {
	Filesystem    *FilesystemMetric    `json:"filesystem,omitempty"`    // For storageType="filesystem"
	ObjectStorage *ObjectStorageMetric `json:"objectStorage,omitempty"` // For storageType="objectstorage"
}

// FilesystemMetric toggles the information collected by InspectFilesystem.
// Path and IsMount are always returned regardless of these flags.
// TotalSize and ExtensionCount aggregate over the whole path recursively,
// ignoring FilterOption.MaxDepth.
//
// Fields are *bool so an omitted field (nil) keeps its default and only an
// explicitly set field overrides it (partial merge). Defaults: FolderModTime
// and FolderPerms are true; every other field is false.
//
// Path-wide aggregates (TotalSize, FileCount, ExtensionCount) cover the whole
// path recursively, ignoring FilterOption.MaxDepth. Per-folder fields
// (Folder*) describe each listed folder; FolderFileCount and FolderFileSize
// count only the files directly contained in that folder (non-recursive).
type FilesystemMetric struct {
	TotalSize       *bool `json:"totalSize,omitempty"`       // Total recursive size (bytes) of path (default false)
	FileCount       *bool `json:"fileCount,omitempty"`       // Total recursive file count under path (default false)
	ExtensionCount  *bool `json:"extensionCount,omitempty"`  // Per-extension file counts under path (default false)
	FolderModTime   *bool `json:"folderModTime,omitempty"`   // Include each folder's modification time (default true)
	FolderPerms     *bool `json:"folderPerms,omitempty"`     // Include each folder's permission string (default true)
	FolderFileCount *bool `json:"folderFileCount,omitempty"` // Include each folder's direct file count (default false)
	FolderFileSize  *bool `json:"folderFileSize,omitempty"`  // Include each folder's direct file size sum in bytes (default false)
}

// ObjectStorageMetric toggles the information collected by InspectObjectStorage.
// Key (folder prefix) is always returned regardless of these flags.
//
// Fields are *bool for partial merge (nil keeps the default). Unlike a
// filesystem folder, an object storage prefix has no intrinsic metadata, so
// every value is derived from child objects and every flag defaults to false.
//
// Path-wide aggregates (TotalSize, ObjectCount, ExtensionCount) cover all
// objects under the scanned bucket/prefix, ignoring FilterOption.MaxDepth.
// Per-prefix fields (Prefix*) describe each listed prefix and count only the
// objects directly under it (non-recursive), mirroring the filesystem Folder* fields.
type ObjectStorageMetric struct {
	TotalSize         *bool `json:"totalSize,omitempty"`         // Total size (bytes) of all objects (default false)
	ObjectCount       *bool `json:"objectCount,omitempty"`       // Total object count (default false)
	ExtensionCount    *bool `json:"extensionCount,omitempty"`    // Per-extension object counts by key (default false)
	PrefixModTime     *bool `json:"prefixModTime,omitempty"`     // Latest LastModified among each prefix's direct objects (default false)
	PrefixObjectCount *bool `json:"prefixObjectCount,omitempty"` // Direct object count per prefix (default false)
	PrefixObjectSize  *bool `json:"prefixObjectSize,omitempty"`  // Direct object size sum (bytes) per prefix (default false)
}

// ============================================================================
// Data Migration Model
// ============================================================================

// DataMigrationModel defines a single data migration task.
type DataMigrationModel struct {
	Source      DataLocation `json:"source" validate:"required"`
	Destination DataLocation `json:"destination" validate:"required"`

	// Strategy determines how the transfer is orchestrated.
	// "auto": Automatically select best method.
	// "direct": Force direct transfer (e.g., SSH agent forwarding).
	// "relay": Force relay via local machine.
	Strategy string `json:"strategy,omitempty" default:"auto" validate:"omitempty,oneof=auto direct relay"`

	// EncryptionKeyID indicates that sensitive fields are encrypted.
	// Empty string means plaintext, non-empty means encrypted with the specified key.
	// The key is one-time use and will be deleted after decryption.
	EncryptionKeyID string `json:"encryptionKeyId,omitempty"`
}

// IsEncrypted returns true if the model has encrypted sensitive fields.
func (m DataMigrationModel) IsEncrypted() bool {
	return m.EncryptionKeyID != ""
}

// ============================================================================
// Data Location (Unified Structure)
// ============================================================================

// DataLocation defines any data location with separated storage type and access method.
type DataLocation struct {
	// StorageType: What kind of storage
	// "filesystem": Local or remote filesystem
	// "objectstorage": S3-compatible object storage
	StorageType string `json:"storageType" validate:"required,oneof=filesystem objectstorage"`

	// Path to the data
	// For Filesystem: File path (e.g., "/data", "/home/user/data")
	// For ObjectStorage: Bucket/Key (e.g., "my-bucket/my-key")
	Path string `json:"path" validate:"required"`

	// Access configuration (one of the following based on StorageType)
	Filesystem    *FilesystemAccess    `json:"filesystem,omitempty"`    // For storageType="filesystem"
	ObjectStorage *ObjectStorageAccess `json:"objectStorage,omitempty"` // For storageType="objectstorage"

	// Filter defines file filtering options
	Filter *FilterOption `json:"filter,omitempty"`

	// Metric selects which information inspect extracts (inspect only; ignored by transfers)
	Metric *MetricOption `json:"metric,omitempty"`

	// Hooks for pre/post processing
	PreCmd  string `json:"preCmd,omitempty"`  // Command to run before transfer (source only)
	PostCmd string `json:"postCmd,omitempty"` // Command to run after transfer (destination only)
}

// ============================================================================
// Filesystem Access Configuration
// ============================================================================

// FilesystemAccess defines how to access filesystem storage.
type FilesystemAccess struct {
	// AccessType: How to access the filesystem
	// "local": Local filesystem (no network)
	// "ssh": Remote filesystem via SSH
	AccessType string `json:"accessType" validate:"required,oneof=local ssh"`

	// SSH configuration (required when accessType="ssh")
	SSH *SSHConfig `json:"ssh,omitempty"`

	// Rsync tuning for transfers involving this location (optional).
	// Source and destination options are merged additively by the executor.
	Rsync *RsyncOption `json:"rsync,omitempty"`
}

// SSHConfig defines SSH connection details.
//
// It is core.SSHConfig, shared with the DBMS domain: the same host, credentials
// and timeout describe an SSH hop whichever kind of data travels over it.
// Options that tune how rsync runs live in RsyncOption instead — they describe
// the transfer, not the connection.
//
// Authentication priority: PrivateKey > PrivateKeyPath > Agent > Password.
// PrivateKey accepts a single-line value with literal "\n" escapes, so a key
// injected through JSON or an environment variable needs no pre-processing.
type SSHConfig = core.SSHConfig

// RsyncOption tunes the rsync invocation used for filesystem transfers.
type RsyncOption struct {
	Archive  bool `json:"archive,omitempty" default:"true"`
	Compress bool `json:"compress,omitempty" default:"true"`
	Delete   bool `json:"delete,omitempty"`  // --delete: remove extraneous files from destination
	Verbose  bool `json:"verbose,omitempty"` // -v: increase verbosity
	DryRun   bool `json:"dryRun,omitempty"`  // --dry-run: trial run without changes
}

// ============================================================================
// Object Storage Access Configuration
// ============================================================================

// ObjectStorageAccess defines how to access object storage.
type ObjectStorageAccess struct {
	// AccessType: How to access object storage
	// "minio": Direct S3 SDK access using minio-go
	// "spider": Via CB-Spider Object Storage API
	// "tumblebug": Via CB-Tumblebug Object Storage API
	AccessType string `json:"accessType" validate:"required,oneof=minio spider tumblebug"`

	// Provider-specific configurations (one required based on accessType)
	Minio     *S3MinioConfig   `json:"minio,omitempty"`     // For accessType="minio"
	Spider    *SpiderConfig    `json:"spider,omitempty"`    // For accessType="spider"
	Tumblebug *TumblebugConfig `json:"tumblebug,omitempty"` // For accessType="tumblebug"
}

// S3MinioConfig defines S3 SDK configuration using minio-go.
type S3MinioConfig struct {
	Endpoint        string `json:"endpoint" validate:"required"`
	AccessKeyId     string `json:"accessKeyId" validate:"required"`
	SecretAccessKey string `json:"secretAccessKey" validate:"required"`
	// Region is the signing region. Leaving it empty is a choice, not an
	// omission: minio-go then asks the bucket for its location and signs with the
	// answer, which is the only way to reach a store whose signing region is not
	// the one in its hostname (NCP serves "kr.…" and signs "kr-standard").
	Region string `json:"region,omitempty"`
	UseSSL bool   `json:"useSSL,omitempty" default:"true"`
	// BucketLookup selects the bucket addressing style: "" (auto) | "dns" | "path".
	// Some S3-compatible CSPs require an explicit style (e.g. Tencent COS = dns).
	BucketLookup string `json:"bucketLookup,omitempty"`
	// SSHTunnel, when set, reaches Endpoint through this SSH host: every
	// connection to the store, presigned transfers included, is opened by the
	// SSH server. Endpoint is therefore resolved from that host's side, so
	// "127.0.0.1:9000" means the host itself. Not supported for Azure endpoints.
	SSHTunnel *SSHConfig `json:"sshTunnel,omitempty"`
}

// SpiderConfig defines CB-Spider Object Storage API configuration.
// Endpoint should include /spider prefix (e.g., "http://localhost:1024/spider").
type SpiderConfig struct {
	Endpoint       string      `json:"endpoint" validate:"required"`       // CB-Spider API base URL (e.g., "http://localhost:1024/spider")
	ConnectionName string      `json:"connectionName" validate:"required"` // CB-Spider connection name (e.g., "aws-connection")
	Expires        int         `json:"expires,omitempty" default:"3600"`   // Presigned URL expiration in seconds
	Auth           *AuthConfig `json:"auth,omitempty"`                     // Optional authentication configuration
}

// TumblebugConfig defines CB-Tumblebug Object Storage API configuration.
// Endpoint should include /tumblebug prefix (e.g., "http://localhost:1323/tumblebug").
type TumblebugConfig struct {
	Endpoint string      `json:"endpoint" validate:"required"`     // CB-Tumblebug API base URL (e.g., "http://localhost:1323/tumblebug")
	NsId     string      `json:"nsId" validate:"required"`         // Namespace ID for multi-tenancy
	OsId     string      `json:"osId" validate:"required"`         // Object Storage ID
	Expires  int         `json:"expires,omitempty" default:"3600"` // Presigned URL expiration in seconds
	Auth     *AuthConfig `json:"auth,omitempty"`                   // Optional authentication configuration
}

// ============================================================================
// Authentication Configuration
// ============================================================================

// Auth types
const (
	// AuthTypeBasic represents HTTP Basic Authentication.
	AuthTypeBasic = "basic"

	// AuthTypeJWT represents JWT (JSON Web Token) Authentication.
	AuthTypeJWT = "jwt"
)

// AuthConfig defines authentication configuration.
// Use authType to specify the authentication method.
// Only basic and JWT are implemented.
type AuthConfig struct {
	AuthType string           `json:"authType" validate:"required"` // "basic" or "jwt"
	Basic    *BasicAuthConfig `json:"basic,omitempty"`              // For authType="basic"
	JWT      *JWTAuthConfig   `json:"jwt,omitempty"`                // For authType="jwt"
}

// BasicAuthConfig defines HTTP Basic Authentication credentials.
type BasicAuthConfig struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// JWTAuthConfig defines JWT authentication configuration.
type JWTAuthConfig struct {
	Token string `json:"token" validate:"required"`
}

// ============================================================================
// Validation
// ============================================================================

// Validate checks if DataMigrationModel satisfies requirements.
func Validate(dmm DataMigrationModel) error {
	if err := validateLocation(dmm.Source, "source"); err != nil {
		return err
	}
	if err := validateLocation(dmm.Destination, "destination"); err != nil {
		return err
	}
	return nil
}

func validateLocation(loc DataLocation, context string) error {
	if strings.TrimSpace(loc.Path) == "" {
		return fmt.Errorf("%s: path is required", context)
	}

	switch loc.StorageType {
	case StorageTypeFilesystem:
		return validateFilesystemAccess(loc.Filesystem, context)

	case StorageTypeObjectStorage:
		return validateObjectStorageAccess(loc.ObjectStorage, context)

	default:
		return fmt.Errorf("%s: unsupported storage type: %s", context, loc.StorageType)
	}
}

func validateFilesystemAccess(fs *FilesystemAccess, context string) error {
	if fs == nil {
		return fmt.Errorf("%s: filesystem access config required", context)
	}

	switch fs.AccessType {
	case AccessTypeLocal:
		// No additional config needed
		return nil

	case AccessTypeSSH:
		if fs.SSH == nil {
			return fmt.Errorf("%s: SSH config required for ssh access", context)
		}
		if strings.TrimSpace(fs.SSH.Host) == "" {
			return fmt.Errorf("%s: SSH host is required", context)
		}
		if strings.TrimSpace(fs.SSH.Username) == "" {
			return fmt.Errorf("%s: SSH username is required", context)
		}
		if fs.SSH.Port < 0 || fs.SSH.Port > 65535 {
			return fmt.Errorf("%s: SSH port out of range", context)
		}
		return nil

	default:
		return fmt.Errorf("%s: unsupported filesystem access type: %s", context, fs.AccessType)
	}
}

func validateObjectStorageAccess(os *ObjectStorageAccess, context string) error {
	if os == nil {
		return fmt.Errorf("%s: object storage access config required", context)
	}

	switch os.AccessType {
	case AccessTypeMinio:
		if os.Minio == nil {
			return fmt.Errorf("%s: minio S3 config required", context)
		}
		if strings.TrimSpace(os.Minio.Endpoint) == "" {
			return fmt.Errorf("%s: S3 endpoint is required", context)
		}
		if strings.TrimSpace(os.Minio.AccessKeyId) == "" || strings.TrimSpace(os.Minio.SecretAccessKey) == "" {
			return fmt.Errorf("%s: S3 credentials are required", context)
		}
		if t := os.Minio.SSHTunnel; t != nil {
			if strings.TrimSpace(t.Host) == "" {
				return fmt.Errorf("%s: S3 SSH tunnel host is required", context)
			}
			if strings.TrimSpace(t.Username) == "" {
				return fmt.Errorf("%s: S3 SSH tunnel username is required", context)
			}
			if t.Port < 0 || t.Port > 65535 {
				return fmt.Errorf("%s: S3 SSH tunnel port out of range", context)
			}
			if IsAzureBlobEndpoint(os.Minio.Endpoint) {
				return fmt.Errorf("%s: an SSH tunnel is not supported for Azure Blob Storage", context)
			}
		}
		return nil

	case AccessTypeSpider:
		if os.Spider == nil {
			return fmt.Errorf("%s: Spider config required", context)
		}
		if strings.TrimSpace(os.Spider.Endpoint) == "" {
			return fmt.Errorf("%s: Spider endpoint is required", context)
		}
		if strings.TrimSpace(os.Spider.ConnectionName) == "" {
			return fmt.Errorf("%s: Spider connection name is required", context)
		}
		return nil

	case AccessTypeTumblebug:
		if os.Tumblebug == nil {
			return fmt.Errorf("%s: Tumblebug config required", context)
		}
		if strings.TrimSpace(os.Tumblebug.Endpoint) == "" {
			return fmt.Errorf("%s: Tumblebug endpoint is required", context)
		}
		if strings.TrimSpace(os.Tumblebug.NsId) == "" {
			return fmt.Errorf("%s: Tumblebug namespace ID is required", context)
		}
		if strings.TrimSpace(os.Tumblebug.OsId) == "" {
			return fmt.Errorf("%s: Tumblebug object storage ID is required", context)
		}
		return nil

	default:
		return fmt.Errorf("%s: unsupported object storage access type: %s", context, os.AccessType)
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

// IsFilesystem returns true if the location uses filesystem storage.
func (loc DataLocation) IsFilesystem() bool {
	return loc.StorageType == StorageTypeFilesystem
}

// IsObjectStorage returns true if the location uses object storage.
func (loc DataLocation) IsObjectStorage() bool {
	return loc.StorageType == StorageTypeObjectStorage
}

// IsLocal returns true if the location is local filesystem.
func (loc DataLocation) IsLocal() bool {
	return loc.IsFilesystem() && loc.Filesystem != nil && loc.Filesystem.AccessType == AccessTypeLocal
}

// IsRemote returns true if the location requires network access.
func (loc DataLocation) IsRemote() bool {
	if loc.IsObjectStorage() {
		return true
	}
	return loc.IsFilesystem() && loc.Filesystem != nil && loc.Filesystem.AccessType == AccessTypeSSH
}

// NeedsLocalStaging returns true if this location needs local staging for relay.
func (loc DataLocation) NeedsLocalStaging() bool {
	return loc.IsRemote()
}
