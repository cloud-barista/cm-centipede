// Package driver is a thin facade over the layered driver packages
// (base + per-engine implementations). It re-exports the public types,
// constants and constructors, so one import replaces five. The underlying
// packages may also be imported directly:
//
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base       shared types & interface
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/postgresql PostgreSQL implementation
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mysql      MySQL implementation
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mariadb    MariaDB implementation
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mongodb    MongoDB implementation
//	github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter            table/collection filtering
package driver

import (
	"log/slog"

	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mariadb"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mongodb"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mysql"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/postgresql"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
)

// =============================================================================
// Type re-exports
// =============================================================================

type (
	DBMSLocation                = base.DBMSLocation
	DirectConfig                = base.DirectConfig
	SSHTunnelConfig             = base.SSHTunnelConfig
	SSHConfig                   = base.SSHConfig
	TargetGrant                 = base.TargetGrant
	DumpOption                  = base.DumpOption
	DBMSInfo                    = base.DBMSInfo
	PgSchemaInfo                = base.PgSchemaInfo
	TableInfo                   = base.TableInfo
	ColumnInfo                  = base.ColumnInfo
	IndexInfo                   = base.IndexInfo
	MetricOption                = base.MetricOption
	MySQLMetric                 = base.MySQLMetric
	MariaDBMetric               = base.MariaDBMetric
	PostgreSQLMetric            = base.PostgreSQLMetric
	MongoDBMetric               = base.MongoDBMetric
	CreateDatabaseOption        = base.CreateDatabaseOption
	MySQLCreateOption           = base.MySQLCreateOption
	MariaDBCreateOption         = base.MariaDBCreateOption
	PostgreSQLCreateOption      = base.PostgreSQLCreateOption
	MongoDBCreateOption         = base.MongoDBCreateOption
	MongoDBCollation            = base.MongoDBCollation
	DropDatabaseOption          = base.DropDatabaseOption
	PostgreSQLDropOption        = base.PostgreSQLDropOption
	CharsetProfile              = base.CharsetProfile
	CharsetIssue                = base.CharsetIssue
	VersionCompatibility        = base.VersionCompatibility
	Severity                    = base.Severity
	DBDriver                    = base.DBDriver
	OperationError              = base.OperationError
	ObjectError                 = base.ObjectError
	UnsupportedDBMSError        = base.UnsupportedDBMSError
	TargetNotEmptyError         = base.TargetNotEmptyError
	TargetDatabaseExistsError   = base.TargetDatabaseExistsError
	TargetDatabaseNotFoundError = base.TargetDatabaseNotFoundError
	SystemDatabaseError         = base.SystemDatabaseError
	InvalidDatabaseNameError    = base.InvalidDatabaseNameError
	SchemaNotFoundError         = base.SchemaNotFoundError
	CharsetIncompatibleError    = base.CharsetIncompatibleError
	VersionDowngradeError       = base.VersionDowngradeError

	DBMSFilterOption = filter.DBMSFilterOption
)

// =============================================================================
// Constant re-exports
// =============================================================================

const (
	DBMSTypeMySQL      = base.DBMSTypeMySQL
	DBMSTypeMariaDB    = base.DBMSTypeMariaDB
	DBMSTypePostgreSQL = base.DBMSTypePostgreSQL
	DBMSTypeMongoDB    = base.DBMSTypeMongoDB

	AccessTypeDirect    = base.AccessTypeDirect
	AccessTypeSSHTunnel = base.AccessTypeSSHTunnel

	// Transport security for a direct connection. See base/tls.go for what the
	// five modes mean and how each engine spells them.
	TLSModeDisable    = base.TLSModeDisable
	TLSModePrefer     = base.TLSModePrefer
	TLSModeRequire    = base.TLSModeRequire
	TLSModeVerifyCA   = base.TLSModeVerifyCA
	TLSModeVerifyFull = base.TLSModeVerifyFull

	// Provider names — where a database is hosted. Same spelling as cm-honeybee.
	ProviderAWS       = base.ProviderAWS
	ProviderAlibaba   = base.ProviderAlibaba
	ProviderTencent   = base.ProviderTencent
	ProviderNCP       = base.ProviderNCP
	ProviderNHN       = base.ProviderNHN
	ProviderIBM       = base.ProviderIBM
	ProviderGCP       = base.ProviderGCP
	ProviderKT        = base.ProviderKT
	ProviderAzure     = base.ProviderAzure
	ProviderOpenStack = base.ProviderOpenStack
	ProviderOnPrem    = base.ProviderOnPrem

	ScopeSchemaOnly = base.ScopeSchemaOnly
	ScopeDataOnly   = base.ScopeDataOnly
	ScopeFull       = base.ScopeFull

	OperationDump     = base.OperationDump
	OperationRestore  = base.OperationRestore
	OperationConnect  = base.OperationConnect
	OperationRollback = base.OperationRollback

	SeverityBlocker = base.SeverityBlocker
	SeverityWarning = base.SeverityWarning

	DefaultStagingPath = base.DefaultStagingPath
)

// =============================================================================
// Function / constructor re-exports
// =============================================================================

// ExecuteViaSSH runs a command on a remote host over SSH. See base.ExecuteViaSSH.
var ExecuteViaSSH = base.ExecuteViaSSH

// IsSystemDatabase reports whether a name is one of an engine's built-in
// databases. See base.IsSystemDatabase.
var IsSystemDatabase = base.IsSystemDatabase

// IsSystemSchema reports whether a name is one of an engine's built-in schemas.
// See base.IsSystemSchema.
var IsSystemSchema = base.IsSystemSchema

// UserSchemas returns names with an engine's system schemas removed, sorted.
// See base.UserSchemas.
var UserSchemas = base.UserSchemas

// ValidateDatabaseName reports whether a name is usable as a database name on an
// engine. See base.ValidateDatabaseName.
var ValidateDatabaseName = base.ValidateDatabaseName

// ValidateCreateOption checks a create option against an engine before any
// connection is made. See base.ValidateCreateOption.
var ValidateCreateOption = base.ValidateCreateOption

// ValidateTLS checks a direct config's transport-security fields before any
// connection is made. See base.ValidateTLS.
var ValidateTLS = base.ValidateTLS

// TLSModes lists the accepted tlsMode values, least strict first.
// See base.TLSModes.
var TLSModes = base.TLSModes

// PgSchemaRenameOpt returns a dump option's schema rename map, or nil.
// See base.PgSchemaRenameOpt.
var PgSchemaRenameOpt = base.PgSchemaRenameOpt

// DiffCharset reports what stands between a source and a target profile.
// See base.DiffCharset.
var DiffCharset = base.DiffCharset

// ParseServerVersion reads the leading numeric components of a version string.
// See base.ParseServerVersion.
var ParseServerVersion = base.ParseServerVersion

// CompareServerVersions orders two version strings by the components that decide
// a feature set on the engine. See base.CompareServerVersions.
var CompareServerVersions = base.CompareServerVersions

// CheckServerVersions builds the verdict for one source/target version pair.
// See base.CheckServerVersions.
var CheckServerVersions = base.CheckServerVersions

// NewMySQLDriver returns a new MySQL driver. See mysql.NewMySQLDriver.
var NewMySQLDriver = mysql.NewMySQLDriver

// NewMariaDBDriver returns a new MariaDB driver. See mariadb.NewMariaDBDriver.
var NewMariaDBDriver = mariadb.NewMariaDBDriver

// NewPostgreSQLDriver returns a new PostgreSQL driver. See postgresql.NewPostgreSQLDriver.
var NewPostgreSQLDriver = postgresql.NewPostgreSQLDriver

// NewMongoDBDriver returns a new MongoDB driver. See mongodb.NewMongoDBDriver.
var NewMongoDBDriver = mongodb.NewMongoDBDriver

// SetLogger replaces the structured logger for the base package and every
// engine package. Call once at program startup.
func SetLogger(l *slog.Logger) {
	base.SetLogger(l)
	mysql.SetLogger(l)
	mariadb.SetLogger(l)
	postgresql.SetLogger(l)
	mongodb.SetLogger(l)
}

// NewDBDriver returns the DBDriver implementation for the given dbmsType.
// It returns *UnsupportedDBMSError when dbmsType is not recognised.
func NewDBDriver(dbmsType string) (base.DBDriver, error) {
	switch dbmsType {
	case base.DBMSTypeMySQL:
		return mysql.NewMySQLDriver(), nil
	case base.DBMSTypeMariaDB:
		return mariadb.NewMariaDBDriver(), nil
	case base.DBMSTypePostgreSQL:
		return postgresql.NewPostgreSQLDriver(), nil
	case base.DBMSTypeMongoDB:
		return mongodb.NewMongoDBDriver(), nil
	default:
		return nil, &base.UnsupportedDBMSError{DBMSType: dbmsType}
	}
}
