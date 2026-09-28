package transxex

import (
	"crypto/rsa"
	"log/slog"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/fieldsec"
)

// ============================================================================
// Migration — Storage
// ============================================================================

// TransferStorage moves the data described by m from source to destination.
// Unlike MigrateStorage it runs no pre- or post-commands.
func TransferStorage(m StorageMigrationModel) error { return storagex.Transfer(m) }

// TransferStorageAsync starts TransferStorage in the background and returns a
// handle for progress and cancellation.
func TransferStorageAsync(m StorageMigrationModel) *StorageHandle { return storagex.TransferAsync(m) }

// MigrateStorage runs the complete storage migration: Source.PreCmd if set, the
// transfer, then Destination.PostCmd if set.
func MigrateStorage(m StorageMigrationModel) error { return storagex.MigrateData(m) }

// MigrateStorageAsync starts MigrateStorage in the background and returns a
// handle for progress and cancellation.
func MigrateStorageAsync(m StorageMigrationModel) *StorageHandle {
	return storagex.MigrateDataAsync(m)
}

// ============================================================================
// Migration — DBMS
// ============================================================================

// MigrateDBMS runs a database migration synchronously. It verifies the
// destination is empty, plans the pipeline, and executes it — rolling back on
// failure when m.RollbackOnFailure is set.
func MigrateDBMS(m DBMSMigrationModel) error { return dbmsx.MigrateData(m) }

// MigrateDBMSAsync starts MigrateDBMS in the background and returns a handle
// for progress and cancellation.
func MigrateDBMSAsync(m DBMSMigrationModel) *DBMSHandle { return dbmsx.MigrateDataAsync(m) }

// ============================================================================
// Single Operations — DBMS only
// ============================================================================

// DumpDBMS exports the database at loc to outputPath. scope must be one of
// ScopeSchemaOnly, ScopeDataOnly or ScopeFull.
func DumpDBMS(loc DBMSLocation, scope, outputPath string) error {
	return dbmsx.Dump(loc, scope, outputPath)
}

// RestoreDBMS imports the file at inputPath into the database at loc.
func RestoreDBMS(loc DBMSLocation, inputPath string) error { return dbmsx.Restore(loc, inputPath) }

// RollbackDropDBMS drops every object inside the database at loc, leaving the
// database itself intact. Best-effort: individual failures are ignored. Use it
// to clean a target before retrying a migration.
func RollbackDropDBMS(loc DBMSLocation) error { return dbmsx.RollbackDrop(loc) }

// ============================================================================
// Database Management — DBMS only
// ============================================================================

// CreateDatabase creates the database named by loc.Database on loc's server.
// Like ListDatabases it connects at server level, since the database does not
// exist yet; unlike it, loc.Database is required — it names the new database.
//
// An existing database yields *TargetDatabaseExistsError unless
// opt.IfNotExists is set; opt may be nil for engine defaults. MongoDB has no
// CREATE DATABASE, so the call materialises the database only when
// opt.MongoDB.Collection is set, and otherwise succeeds without doing anything.
func CreateDatabase(loc DBMSLocation, opt *CreateDatabaseOption) error {
	return dbmsx.CreateDatabase(loc, opt)
}

// DropDatabase drops the database named by loc.Database and everything in it.
// The default is unconditional; opt.RequireEmpty refuses a database that still
// holds objects, and opt.IfExists turns a missing one into a no-op success.
//
// This is not RollbackDropDBMS: that empties a database and keeps it, this
// removes the database itself. System databases are refused.
func DropDatabase(loc DBMSLocation, opt *DropDatabaseOption) error {
	return dbmsx.DropDatabase(loc, opt)
}

// ============================================================================
// Inspection
// ============================================================================

// InspectFilesystem scans the filesystem path at loc and returns its folders
// and the metrics loc.Metric selects.
func InspectFilesystem(loc StorageLocation) (*FSInspectResult, error) {
	return storagex.InspectFilesystem(loc)
}

// InspectObjectStorage scans the bucket/prefix at loc and returns its prefixes
// and the metrics loc.Metric selects.
func InspectObjectStorage(loc StorageLocation) (*OSInspectResult, error) {
	return storagex.InspectObjectStorage(loc)
}

// InspectDBMS returns server- and schema-level metadata for the database at loc.
func InspectDBMS(loc DBMSLocation) (*DBMSInfo, error) { return dbmsx.Inspect(loc) }

// InspectDBMSAll inspects every user database on loc's server and returns their
// metadata, sorted by database name. loc.Database is ignored; loc.Metric applies
// to every database alike.
//
// A database that fails individually is reported in the second return value
// instead of aborting the whole call. The error is returned only when nothing
// could be inspected: loc is invalid, or the enumeration itself failed.
func InspectDBMSAll(loc DBMSLocation) ([]DBMSInfo, []DBMSInspectError, error) {
	return dbmsx.InspectAll(loc)
}

// ListDatabases returns the user databases on the server at loc, sorted, with
// the engine's system databases omitted. loc.Database is not required.
func ListDatabases(loc DBMSLocation) ([]string, error) { return dbmsx.ListDatabases(loc) }

// ListSchemas returns the user schemas inside loc.Database, sorted, with the
// engine's system schemas omitted — what may be put in loc.PgSchema, which this
// call itself ignores. Only PostgreSQL has schemas distinct from databases; the
// other engines return an empty list.
func ListSchemas(loc DBMSLocation) ([]string, error) { return dbmsx.ListSchemas(loc) }

// DescribeTable returns column and index metadata for one table at loc.
func DescribeTable(loc DBMSLocation, table string) (*TableInfo, error) {
	return dbmsx.DescribeTable(loc, table)
}

// ServerVersion returns the version string loc's server identifies itself by,
// exactly as reported — "8.0.32", "10.6.14-MariaDB-1:10.6.14", "14.23 (Ubuntu
// 14.23-1.pgdg22.04+1)", "7.0.5".
//
// It is a server-level call — one query, no table statistics — and loc.Database
// need not exist, so a target can be read before its first migration has created
// anything. ParseServerVersion reduces the string to numbers.
func ServerVersion(loc DBMSLocation) (string, error) { return dbmsx.ServerVersion(loc) }

// ParseServerVersion reads the leading numeric components of a version string:
// "10.6.14-MariaDB" yields [10 6 14]. A string with no leading number yields
// nil, which means the version cannot be compared rather than that it is zero.
func ParseServerVersion(s string) []int { return dbmsx.ParseServerVersion(s) }

// CompareServerVersions orders two version strings by the components that decide
// a feature set on dbmsType: negative when a precedes b, zero when equivalent,
// positive when a follows b. The second return is false when either string
// carries no leading number, and the int then means nothing.
//
// A patch-level difference never separates two versions. Releases are
// MAJOR.MINOR.PATCH everywhere but PostgreSQL, which from 10 onwards numbers
// them MAJOR.PATCH — 14.2 and 14.5 are one feature set — so only the major
// counts there.
func CompareServerVersions(dbmsType, a, b string) (int, bool) {
	return dbmsx.CompareServerVersions(dbmsType, a, b)
}

// ============================================================================
// Plan and Validate
// ============================================================================

// PlanStorage resolves m into an executable transfer pipeline without running it.
func PlanStorage(m StorageMigrationModel) (*StoragePipeline, error) { return storagex.Plan(m) }

// PlanDBMS resolves m into an executable migration pipeline without running it.
func PlanDBMS(m DBMSMigrationModel) (*DBMSPipeline, error) { return dbmsx.Plan(m) }

// ValidateStorage reports whether m is structurally and semantically usable.
func ValidateStorage(m StorageMigrationModel) error { return storagex.Validate(m) }

// RsyncFilterArgs returns the native rsync arguments a path filter translates to.
// See storagex.RsyncFilterArgs.
func RsyncFilterArgs(opt *PathFilterOption) ([]string, error) {
	return storagex.RsyncFilterArgs(opt)
}

// ValidateDBMS reports whether m is structurally and semantically usable.
func ValidateDBMS(m DBMSMigrationModel) error { return dbmsx.Validate(m) }

// CheckCharset reports what stands between the character-set handling of two
// database endpoints, most severe first. It reads both and changes neither.
//
// MigrateDBMS runs this for itself before it starts, so calling it beforehand is
// not required — it exists to show the answer while a migration is still being
// planned. A blocking difference surfaces there as *CharsetIncompatibleError.
func CheckCharset(src, dst DBMSLocation) ([]CharsetIssue, error) {
	return dbmsx.CheckCharset(src, dst)
}

// CheckVersion reports whether dst's server can accept a dump taken from src's.
// It reads both and changes neither.
//
// MigrateDBMS runs this for itself before it starts, so calling it beforehand is
// not required — it exists to show the answer while a migration is still being
// planned. A target older than the source surfaces there as
// *VersionDowngradeError; set DBMSMigrationModel.SkipVersionCheck to proceed
// anyway.
func CheckVersion(src, dst DBMSLocation) (*VersionCompatibility, error) {
	return dbmsx.CheckVersion(src, dst)
}

// ============================================================================
// Field Encryption — storage domain only
// ============================================================================

// InitKeyStore initialises the process-wide key store used to decrypt migration
// models. Keys expire after keyExpiryDuration; expired keys are swept every
// cleanupInterval.
func InitKeyStore(keyExpiryDuration, cleanupInterval time.Duration) {
	storagex.InitKeyStore(keyExpiryDuration, cleanupInterval)
}

// GetKeyStore returns the process-wide key store.
func GetKeyStore() *KeyStore { return storagex.GetKeyStore() }

// GetKeyExpiry returns the configured key lifetime.
func GetKeyExpiry() time.Duration { return storagex.GetKeyExpiry() }

// ParsePublicKeyBundle reconstructs an RSA public key from its wire form.
func ParsePublicKeyBundle(bundle PublicKeyBundle) (*rsa.PublicKey, error) {
	return fieldsec.ParsePublicKeyBundle(bundle)
}

// NewKeyStore creates an independent key store. Most callers want the
// process-wide one from GetKeyStore instead.
func NewKeyStore() *KeyStore { return fieldsec.NewKeyStore() }

// EncryptStorageModel encrypts the sensitive fields of m — SSH private keys,
// object storage credentials, API passwords and tokens — under publicKey, and
// stamps keyID on the result.
//
// Database models have no counterpart: DBMSMigrationModel credentials are not
// encrypted at this layer.
func EncryptStorageModel(m StorageMigrationModel, publicKey *rsa.PublicKey, keyID string) (StorageMigrationModel, error) {
	return storagex.EncryptModel(m, publicKey, keyID)
}

// DecryptStorageModel decrypts the fields EncryptStorageModel encrypted, using
// the key its EncryptionKeyID names from the process-wide store. The key is
// single-use and is removed once consumed.
func DecryptStorageModel(m StorageMigrationModel) (StorageMigrationModel, error) {
	return storagex.DecryptModel(m)
}

// DecryptStorageModelWith decrypts m with an explicitly supplied key pair,
// bypassing the process-wide store.
func DecryptStorageModelWith(m StorageMigrationModel, keyPair *KeyPair) (StorageMigrationModel, error) {
	return storagex.DecryptModelWith(m, keyPair)
}

// ============================================================================
// Storage Constructors and Helpers
// ============================================================================

// NewS3Provider builds the object storage provider loc's access configuration
// selects: MinIO, Azure Blob Storage, CB-Spider or CB-Tumblebug. Access type
// "minio" resolves to Azure when its endpoint addresses Azure Blob Storage.
func NewS3Provider(loc StorageLocation) (S3Provider, error) { return storagex.NewS3Provider(loc) }

// CloseS3Provider releases what a provider holds open — the SSH connection of a
// MinIO location with an SSH tunnel. It is a no-op for every other provider, so
// a caller of NewS3Provider can always defer it.
func CloseS3Provider(p S3Provider) error { return storagex.CloseS3Provider(p) }

// NewMinioProvider builds a provider that talks S3 directly through minio-go.
// S3-compatible services only — use NewAzureProvider for Azure Blob Storage,
// or let NewS3Provider route by endpoint.
func NewMinioProvider(config *MinioConfig, bucket string) (*MinioProvider, error) {
	return storagex.NewMinioProvider(config, bucket)
}

// NewAzureProvider builds a provider that talks to Azure Blob Storage through
// the Azure SDK. container is the Azure container the bucket name refers to.
func NewAzureProvider(config *AzureConfig, container string) (*AzureProvider, error) {
	return storagex.NewAzureProvider(config, container)
}

// IsAzureBlobEndpoint reports whether an object storage endpoint addresses
// Azure Blob Storage rather than an S3-compatible service.
func IsAzureBlobEndpoint(endpoint string) bool { return storagex.IsAzureBlobEndpoint(endpoint) }

// NewSpiderProvider builds a provider that goes through the CB-Spider API.
func NewSpiderProvider(config *SpiderConfig, bucket string) (*SpiderProvider, error) {
	return storagex.NewSpiderProvider(config, bucket)
}

// NewTumblebugProvider builds a provider that goes through the CB-Tumblebug API.
func NewTumblebugProvider(config *TumblebugConfig) (*TumblebugProvider, error) {
	return storagex.NewTumblebugProvider(config)
}

// NewS3Executor wraps a provider as a pipeline executor.
func NewS3Executor(provider S3Provider) *S3Executor { return storagex.NewS3Executor(provider) }

// NewRsyncExecutor builds the rsync executor for a source/destination pair,
// choosing pull, push or agent-forward from their access types.
func NewRsyncExecutor(src, dst StorageLocation) (*RsyncExecutor, error) {
	return storagex.NewRsyncExecutor(src, dst)
}

// ParseBucketAndKey splits an object storage path into its bucket and key.
func ParseBucketAndKey(path string) (bucket, key string) { return storagex.ParseBucketAndKey(path) }

// ============================================================================
// Logging
// ============================================================================

// SetLogger injects l as the structured logger for transx-ex. Call once at
// program startup, before any migration operation.
//
// Only the database domain logs today; the call is safe regardless.
//
// Example — zerolog bridge:
//
//	transxex.SetLogger(slog.New(zerologHandler))
func SetLogger(l *slog.Logger) { dbmsx.SetLogger(l) }

// ============================================================================
// Storage Pre/Post Commands
// ============================================================================

// RunStoragePreCommand executes loc.PreCmd, the hook that prepares data before
// a transfer. MigrateStorage runs it automatically; use this only to perform
// that step alone.
func RunStoragePreCommand(loc StorageLocation) error { return storagex.RunPreCommand(loc) }

// RunStoragePostCommand executes loc.PostCmd, the hook that consumes data after
// a transfer. MigrateStorage runs it automatically; use this only to perform
// that step alone.
func RunStoragePostCommand(loc StorageLocation) error { return storagex.RunPostCommand(loc) }
