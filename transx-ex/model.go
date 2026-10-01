package transxex

import (
	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/fieldsec"
)

// ============================================================================
// Storage Domain — filesystem and object storage
// ============================================================================

type (
	// StorageMigrationModel defines a single filesystem/object storage migration.
	StorageMigrationModel = storagex.DataMigrationModel

	// StorageLocation describes one end of a storage migration.
	StorageLocation = storagex.DataLocation

	// StorageHandle controls a running storage migration asynchronously.
	StorageHandle = storagex.MigrationHandle

	// StorageProgress reports a storage migration's progress.
	StorageProgress = storagex.MigrationProgress

	// StoragePipeline is a planned storage transfer.
	StoragePipeline = storagex.Pipeline

	// StorageMetricOption selects what InspectFilesystem/InspectObjectStorage collect.
	StorageMetricOption = storagex.MetricOption

	// PathFilterOption is the ordered include/exclude pipeline for files and objects.
	PathFilterOption = storagex.FilterOption

	// PathFilterItem is one file, directory or object key a PathFilterOption judges.
	PathFilterItem = storagex.FilterItem

	FilesystemAccess    = storagex.FilesystemAccess
	ObjectStorageAccess = storagex.ObjectStorageAccess
	RsyncOption         = storagex.RsyncOption
	FilesystemMetric    = storagex.FilesystemMetric
	ObjectStorageMetric = storagex.ObjectStorageMetric

	S3MinioConfig   = storagex.S3MinioConfig
	SpiderConfig    = storagex.SpiderConfig
	TumblebugConfig = storagex.TumblebugConfig
	AuthConfig      = storagex.AuthConfig
	BasicAuthConfig = storagex.BasicAuthConfig
	JWTAuthConfig   = storagex.JWTAuthConfig

	S3Provider        = storagex.S3Provider
	ObjectInfo        = storagex.ObjectInfo
	MinioConfig       = storagex.MinioConfig
	MinioProvider     = storagex.MinioProvider
	AzureConfig       = storagex.AzureConfig
	AzureProvider     = storagex.AzureProvider
	SpiderProvider    = storagex.SpiderProvider
	TumblebugProvider = storagex.TumblebugProvider
	RsyncExecutor     = storagex.RsyncExecutor
	S3Executor        = storagex.S3Executor

	FSEntry         = storagex.FSEntry
	FSInspectResult = storagex.FSInspectResult
	OSEntry         = storagex.OSEntry
	OSInspectResult = storagex.OSInspectResult
)

// Storage type discriminators.
const (
	StorageTypeFilesystem    = storagex.StorageTypeFilesystem
	StorageTypeObjectStorage = storagex.StorageTypeObjectStorage
)

// Storage access types.
const (
	AccessTypeLocal     = storagex.AccessTypeLocal
	AccessTypeSSH       = storagex.AccessTypeSSH
	AccessTypeMinio     = storagex.AccessTypeMinio
	AccessTypeSpider    = storagex.AccessTypeSpider
	AccessTypeTumblebug = storagex.AccessTypeTumblebug
)

// Storage transfer strategies.
const (
	StrategyAuto   = storagex.StrategyAuto
	StrategyDirect = storagex.StrategyDirect
	StrategyRelay  = storagex.StrategyRelay
)

// Storage pipeline and step names.
const (
	PipelineFilesystemTransfer    = storagex.PipelineFilesystemTransfer
	PipelineObjectStorageTransfer = storagex.PipelineObjectStorageTransfer
	PipelineCrossStorageTransfer  = storagex.PipelineCrossStorageTransfer

	StepRsyncTransfer   = storagex.StepRsyncTransfer
	StepDownloadFromS3  = storagex.StepDownloadFromS3
	StepUploadToS3      = storagex.StepUploadToS3
	StepRsyncFromServer = storagex.StepRsyncFromServer
	StepRsyncToServer   = storagex.StepRsyncToServer
)

// Authentication types for the Spider and Tumblebug object storage APIs.
const (
	AuthTypeBasic = storagex.AuthTypeBasic
	AuthTypeJWT   = storagex.AuthTypeJWT
)

// StorageStagingPath is the local directory storage relays stage data in.
const StorageStagingPath = storagex.DefaultStagingPath

// ============================================================================
// DBMS Domain
// ============================================================================

type (
	// DBMSMigrationModel defines a single database migration.
	DBMSMigrationModel = dbmsx.DBMSMigrationModel

	// DBMSLocation describes one end of a database migration.
	DBMSLocation = dbmsx.DBMSLocation

	// DBMSHandle controls a running database migration asynchronously.
	DBMSHandle = dbmsx.MigrationHandle

	// DBMSProgress reports a database migration's progress.
	DBMSProgress = dbmsx.MigrationProgress

	// DBMSPipeline is a planned database migration.
	DBMSPipeline = dbmsx.Pipeline

	// DBMSMetricOption selects what InspectDBMS collects.
	DBMSMetricOption = dbmsx.MetricOption

	// DBMSFilterOption is the exclude-only pipeline for tables, rows and schema objects.
	DBMSFilterOption = dbmsx.DBMSFilterOption

	DirectConfig    = dbmsx.DirectConfig
	SSHTunnelConfig = dbmsx.SSHTunnelConfig
	TargetGrant     = dbmsx.TargetGrant

	MySQLMetric      = dbmsx.MySQLMetric
	MariaDBMetric    = dbmsx.MariaDBMetric
	PostgreSQLMetric = dbmsx.PostgreSQLMetric
	MongoDBMetric    = dbmsx.MongoDBMetric

	// CreateDatabaseOption selects the engine-specific attributes CreateDatabase
	// gives a new database.
	CreateDatabaseOption = dbmsx.CreateDatabaseOption

	// DropDatabaseOption carries DropDatabase's opt-in guards. Dropping is
	// unconditional by default.
	DropDatabaseOption = dbmsx.DropDatabaseOption

	MySQLCreateOption      = dbmsx.MySQLCreateOption
	MariaDBCreateOption    = dbmsx.MariaDBCreateOption
	PostgreSQLCreateOption = dbmsx.PostgreSQLCreateOption
	MongoDBCreateOption    = dbmsx.MongoDBCreateOption
	PostgreSQLDropOption   = dbmsx.PostgreSQLDropOption

	// CharsetProfile is one endpoint's text handling, as CheckCharset reads it.
	CharsetProfile = dbmsx.CharsetProfile

	// CharsetIssue is one difference CheckCharset found between two endpoints.
	CharsetIssue = dbmsx.CharsetIssue

	// VersionCompatibility is CheckVersion's verdict on a source/target pair.
	VersionCompatibility = dbmsx.VersionCompatibility

	DBMSInfo = dbmsx.DBMSInfo
	// PgSchemaInfo is the per-schema size rollup DBMSInfo carries for PostgreSQL.
	PgSchemaInfo = dbmsx.PgSchemaInfo
	TableInfo    = dbmsx.TableInfo
	ColumnInfo   = dbmsx.ColumnInfo
	IndexInfo    = dbmsx.IndexInfo
)

// Supported database engines.
const (
	DBMSTypeMySQL      = dbmsx.DBMSTypeMySQL
	DBMSTypeMariaDB    = dbmsx.DBMSTypeMariaDB
	DBMSTypePostgreSQL = dbmsx.DBMSTypePostgreSQL
	DBMSTypeMongoDB    = dbmsx.DBMSTypeMongoDB
)

// Database access types.
const (
	AccessTypeDirect    = dbmsx.AccessTypeDirect
	AccessTypeSSHTunnel = dbmsx.AccessTypeSSHTunnel
)

// Transport security for DirectConfig.TLSMode, least strict first. An empty
// mode is TLSModeDisable. The modes mean the same thing on every engine; what
// separates them is whether the connection must be encrypted and how far the
// server is verified.
const (
	TLSModeDisable    = dbmsx.TLSModeDisable
	TLSModePrefer     = dbmsx.TLSModePrefer
	TLSModeRequire    = dbmsx.TLSModeRequire
	TLSModeVerifyCA   = dbmsx.TLSModeVerifyCA
	TLSModeVerifyFull = dbmsx.TLSModeVerifyFull
)

// Provider names for DBMSLocation.ProviderName — where a database is hosted.
// Spelled as cm-honeybee spells them, so a name resolved there passes through
// unchanged. The list is not a constraint: any string is accepted, and nothing
// in the library reads the field.
const (
	ProviderAWS       = dbmsx.ProviderAWS
	ProviderAlibaba   = dbmsx.ProviderAlibaba
	ProviderTencent   = dbmsx.ProviderTencent
	ProviderNCP       = dbmsx.ProviderNCP
	ProviderNHN       = dbmsx.ProviderNHN
	ProviderIBM       = dbmsx.ProviderIBM
	ProviderGCP       = dbmsx.ProviderGCP
	ProviderKT        = dbmsx.ProviderKT
	ProviderAzure     = dbmsx.ProviderAzure
	ProviderOpenStack = dbmsx.ProviderOpenStack

	// ProviderOnPrem marks a database on the operator's own infrastructure.
	ProviderOnPrem = dbmsx.ProviderOnPrem
)

// Migration scopes.
const (
	ScopeSchemaOnly = dbmsx.ScopeSchemaOnly
	ScopeDataOnly   = dbmsx.ScopeDataOnly
	ScopeFull       = dbmsx.ScopeFull
)

// Database pipeline and step names.
const (
	PipelineDBMSTransfer = dbmsx.PipelineDBMSTransfer
	StepDumpSource       = dbmsx.StepDumpSource
	StepRestoreTarget    = dbmsx.StepRestoreTarget
	StepDirectPipe       = dbmsx.StepDirectPipe
)

// DBMSStagingPath is the local directory intermediate dump files are written to.
const DBMSStagingPath = dbmsx.DefaultStagingPath

// ============================================================================
// Shared — one declaration serves both domains
// ============================================================================

// SSHConfig describes an SSH hop. The same host, credentials and timeout apply
// whether files or a database travel over it, so there is one type.
type SSHConfig = core.SSHConfig

// MigrationStatus is the lifecycle state of a migration in either domain.
type MigrationStatus = core.Status

// Lifecycle states. Pending, Done and Failed occur in both domains; Running and
// Cancelled are reported by storage migrations, and the remaining values name
// the phases a database migration passes through.
const (
	StatusPending     = core.StatusPending
	StatusRunning     = core.StatusRunning
	StatusDone        = core.StatusDone
	StatusFailed      = core.StatusFailed
	StatusCancelled   = core.StatusCancelled
	StatusChecking    = dbmsx.StatusChecking
	StatusDumping     = dbmsx.StatusDumping
	StatusRestoring   = dbmsx.StatusRestoring
	StatusRollingBack = dbmsx.StatusRollingBack
	StatusRolledBack  = dbmsx.StatusRolledBack
)

// StagingRoot is the local directory both domains stage data under.
const StagingRoot = core.StagingRoot

// ============================================================================
// Field Encryption (storage domain only)
// ============================================================================

type (
	KeyPair         = fieldsec.KeyPair
	KeyStore        = fieldsec.KeyStore
	PublicKeyBundle = fieldsec.PublicKeyBundle
)
