package dbmsx

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
	execpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/executor"
)

// =============================================================================
// Type aliases — re-export driver types so callers use unqualified names
// =============================================================================

type (
	DBMSLocation     = drvpkg.DBMSLocation
	DirectConfig     = drvpkg.DirectConfig
	SSHTunnelConfig  = drvpkg.SSHTunnelConfig
	SSHConfig        = drvpkg.SSHConfig
	DBMSFilterOption = drvpkg.DBMSFilterOption
	DBMSInfo         = drvpkg.DBMSInfo
	PgSchemaInfo     = drvpkg.PgSchemaInfo
	TableInfo        = drvpkg.TableInfo
	ColumnInfo       = drvpkg.ColumnInfo
	IndexInfo        = drvpkg.IndexInfo
	TargetGrant      = drvpkg.TargetGrant

	MetricOption     = drvpkg.MetricOption
	MySQLMetric      = drvpkg.MySQLMetric
	MariaDBMetric    = drvpkg.MariaDBMetric
	PostgreSQLMetric = drvpkg.PostgreSQLMetric
	MongoDBMetric    = drvpkg.MongoDBMetric

	CharsetProfile = drvpkg.CharsetProfile
	CharsetIssue   = drvpkg.CharsetIssue

	// VersionCompatibility is what CheckVersion reports about a source/target
	// server version pair.
	VersionCompatibility = drvpkg.VersionCompatibility
	Severity             = drvpkg.Severity

	DumpOption             = drvpkg.DumpOption
	CreateDatabaseOption   = drvpkg.CreateDatabaseOption
	MySQLCreateOption      = drvpkg.MySQLCreateOption
	MariaDBCreateOption    = drvpkg.MariaDBCreateOption
	PostgreSQLCreateOption = drvpkg.PostgreSQLCreateOption
	MongoDBCreateOption    = drvpkg.MongoDBCreateOption
	MongoDBCollation       = drvpkg.MongoDBCollation
	DropDatabaseOption     = drvpkg.DropDatabaseOption
	PostgreSQLDropOption   = drvpkg.PostgreSQLDropOption
)

// Const re-declarations — re-export driver constants.
const (
	DBMSTypeMySQL      = drvpkg.DBMSTypeMySQL
	DBMSTypeMariaDB    = drvpkg.DBMSTypeMariaDB
	DBMSTypePostgreSQL = drvpkg.DBMSTypePostgreSQL
	DBMSTypeMongoDB    = drvpkg.DBMSTypeMongoDB

	AccessTypeDirect    = drvpkg.AccessTypeDirect
	AccessTypeSSHTunnel = drvpkg.AccessTypeSSHTunnel

	// Transport security for a direct connection.
	TLSModeDisable    = drvpkg.TLSModeDisable
	TLSModePrefer     = drvpkg.TLSModePrefer
	TLSModeRequire    = drvpkg.TLSModeRequire
	TLSModeVerifyCA   = drvpkg.TLSModeVerifyCA
	TLSModeVerifyFull = drvpkg.TLSModeVerifyFull

	// Provider names — where a database is hosted. Same spelling as cm-honeybee.
	ProviderAWS       = drvpkg.ProviderAWS
	ProviderAlibaba   = drvpkg.ProviderAlibaba
	ProviderTencent   = drvpkg.ProviderTencent
	ProviderNCP       = drvpkg.ProviderNCP
	ProviderNHN       = drvpkg.ProviderNHN
	ProviderIBM       = drvpkg.ProviderIBM
	ProviderGCP       = drvpkg.ProviderGCP
	ProviderKT        = drvpkg.ProviderKT
	ProviderAzure     = drvpkg.ProviderAzure
	ProviderOpenStack = drvpkg.ProviderOpenStack
	ProviderOnPrem    = drvpkg.ProviderOnPrem

	ScopeSchemaOnly = drvpkg.ScopeSchemaOnly
	ScopeDataOnly   = drvpkg.ScopeDataOnly
	ScopeFull       = drvpkg.ScopeFull

	SeverityBlocker = drvpkg.SeverityBlocker
	SeverityWarning = drvpkg.SeverityWarning

	DefaultStagingPath = drvpkg.DefaultStagingPath
)

// =============================================================================
// Type aliases — re-export executor types so callers use unqualified names
// =============================================================================

type (
	Executor = execpkg.Executor
	Pipeline = execpkg.Pipeline
	Step     = execpkg.Step
)

// Const re-declarations — re-export executor pipeline/step name constants.
const (
	PipelineDBMSTransfer = execpkg.PipelineDBMSTransfer
	StepDumpSource       = execpkg.StepDumpSource
	StepRestoreTarget    = execpkg.StepRestoreTarget
	StepDirectPipe       = execpkg.StepDirectPipe
)

// =============================================================================
// Migration Status
// =============================================================================

// MigrationStatus represents the lifecycle state of a migration.
// It is core.Status, shared with the storage domain.
type MigrationStatus = core.Status

// Lifecycle states. Pending, Done and Failed are the shared core values; the
// rest name the intermediate phases a database migration passes through and
// exist only here.
const (
	StatusPending                     = core.StatusPending
	StatusDone                        = core.StatusDone
	StatusFailed                      = core.StatusFailed
	StatusChecking    MigrationStatus = "checking"
	StatusDumping     MigrationStatus = "dumping"
	StatusRestoring   MigrationStatus = "restoring"
	StatusRollingBack MigrationStatus = "rolling-back"
	StatusRolledBack  MigrationStatus = "rolled-back"
)

// =============================================================================
// Migration Progress
// =============================================================================

// MigrationProgress holds real-time progress information for an ongoing migration.
type MigrationProgress struct {
	Status           MigrationStatus `json:"status"`
	StartedAt        time.Time       `json:"startedAt"`
	ElapsedSec       float64         `json:"elapsedSec"`
	BytesTotal       int64           `json:"bytesTotal,omitempty"`
	BytesTransferred int64           `json:"bytesTransferred"`
	TablesTotal      int             `json:"tablesTotal,omitempty"`
	TablesDone       int             `json:"tablesDone"`
	CurrentTable     string          `json:"currentTable,omitempty"`
	ErrMessage       string          `json:"error,omitempty"`
}

// The accessors below satisfy core.Progress, which is how core.Handle keeps
// the fields common to every domain up to date.

func (p *MigrationProgress) GetStatus() core.Status     { return p.Status }
func (p *MigrationProgress) SetStatus(s core.Status)    { p.Status = s }
func (p *MigrationProgress) SetStartedAt(t time.Time)   { p.StartedAt = t }
func (p *MigrationProgress) SetElapsed(seconds float64) { p.ElapsedSec = seconds }
func (p *MigrationProgress) SetError(msg string)        { p.ErrMessage = msg }

// =============================================================================
// Migration Model
// =============================================================================

// DBMSMigrationModel describes a full DBMS migration request.
type DBMSMigrationModel struct {
	Source            DBMSLocation `json:"source"`
	Destination       DBMSLocation `json:"destination"`
	Scope             string       `json:"scope,omitempty"`
	RollbackOnFailure bool         `json:"rollbackOnFailure,omitempty"`
	// TargetGrant, when non-nil, causes PrepareTarget to run before the migration
	// starts: it requires the target database to already exist and be empty
	// (otherwise TargetDatabaseNotFoundError / TargetNotEmptyError), then
	// provisions the specified user with full privileges on that database.
	// The target database is never created automatically.
	TargetGrant *TargetGrant `json:"targetGrant,omitempty"`

	// SkipCharsetCheck skips the character-set compatibility check that runs
	// once the target is known to be empty, before any data is read.
	//
	// The check profiles both endpoints and refuses the migration when the
	// target cannot accept what the dump will carry — a collation it does not
	// recognise, or on PostgreSQL an encoding that cannot hold the source's
	// text. Those conditions fail the restore anyway; the check only moves the
	// failure to before the dump instead of after it. Set this when the check
	// is wrong about a particular pair and the migration should proceed
	// regardless.
	SkipCharsetCheck bool `json:"skipCharsetCheck,omitempty"`

	// SkipVersionCheck skips the server-version check that runs once the target
	// is known to be empty, before any data is read.
	//
	// The check refuses a target older than the source, because a dump is
	// written in the source server's dialect and anything the source added since
	// the target's release reaches the restore as a syntax error. Only a
	// difference in the components that decide a feature set counts; a
	// patch-level difference never blocks. Set this to migrate to an older
	// server anyway — when the schema is known to use nothing the target lacks.
	SkipVersionCheck bool `json:"skipVersionCheck,omitempty"`
}

// =============================================================================
// Validate
// =============================================================================

// Validate checks a DBMSMigrationModel for structural and semantic correctness.
func Validate(model DBMSMigrationModel) error {
	if err := validateLocation("source", model.Source); err != nil {
		return err
	}
	if err := validateLocation("destination", model.Destination); err != nil {
		return err
	}
	if model.Scope != "" {
		switch model.Scope {
		case ScopeSchemaOnly, ScopeDataOnly, ScopeFull:
			// valid
		default:
			return fmt.Errorf("scope %q is invalid: must be one of schema-only, data-only, full", model.Scope)
		}
	}
	return validateCrossLocation(model.Source, model.Destination)
}

func validateLocation(side string, loc DBMSLocation) error {
	if err := validateServerLocation(side, loc); err != nil {
		return err
	}

	if loc.Database == "" {
		return fmt.Errorf("%s.database is required", side)
	}

	if loc.Filter != nil {
		if err := loc.Filter.Validate(loc.DBMSType, loc.AccessType == AccessTypeDirect); err != nil {
			return fmt.Errorf("%s: %w", side, err)
		}
	}
	return nil
}

// validateServerLocation checks the parts of loc a server-level operation needs:
// the engine, the access type and its config, and the schema selection — which
// belongs here rather than with Database because it qualifies the engine, not
// the database, and has to be refused on an engine that has no schemas whatever
// the call. Database is not checked — such an operation does not target one, and
// for ListDatabases it may not even exist yet — and neither is Filter, which
// only ever applies to a dump.
func validateServerLocation(side string, loc DBMSLocation) error {
	switch loc.DBMSType {
	case DBMSTypeMySQL, DBMSTypeMariaDB, DBMSTypePostgreSQL, DBMSTypeMongoDB:
		// valid
	case "":
		return fmt.Errorf("%s.dbmsType is required", side)
	default:
		return fmt.Errorf("%s.dbmsType %q is not supported", side, loc.DBMSType)
	}

	switch loc.AccessType {
	case AccessTypeDirect:
		if loc.Direct == nil {
			return fmt.Errorf("%s.direct config is required when accessType is %q", side, AccessTypeDirect)
		}
		if err := validateDirectConfig(side, loc.Direct); err != nil {
			return err
		}
	case AccessTypeSSHTunnel:
		if loc.SSHTunnel == nil {
			return fmt.Errorf("%s.sshTunnel config is required when accessType is %q", side, AccessTypeSSHTunnel)
		}
		if err := validateSSHTunnelConfig(side, loc.SSHTunnel); err != nil {
			return err
		}
	case "":
		return fmt.Errorf("%s.accessType is required", side)
	default:
		return fmt.Errorf("%s.accessType %q is not supported", side, loc.AccessType)
	}

	if err := validateTLS(side, loc); err != nil {
		return err
	}

	return validatePgSchema(side, loc)
}

// validateTLS checks the transport-security fields of a direct location. The
// rules live in the driver package, next to the mode definitions they enforce,
// so the two cannot drift apart.
//
// An ssh-tunnel location has nothing to check: SSHTunnelConfig carries no TLS
// fields, because that path hands the engine's CLI tools no TLS options.
func validateTLS(side string, loc DBMSLocation) error {
	if loc.AccessType != AccessTypeDirect || loc.Direct == nil {
		return nil
	}
	return drvpkg.ValidateTLS(side, loc.DBMSType, loc.Direct)
}

// validateDatabaseLocation checks what a server-level DDL call against one named
// database needs: everything validateServerLocation covers, plus the name it is
// about to create or drop. Filter is not checked — it only ever applies to a
// dump — which is what separates this from validateLocation.
func validateDatabaseLocation(side string, loc DBMSLocation) error {
	if err := validateServerLocation(side, loc); err != nil {
		return err
	}
	if loc.Database == "" {
		return fmt.Errorf("%s.database is required", side)
	}
	return drvpkg.ValidateDatabaseName(loc.DBMSType, loc.Database)
}

// validatePgSchema checks one side's schema selection on its own: the field is
// PostgreSQL's alone, and each name in it has to survive the identifier quoting
// the generated DDL applies. How the two sides line up is validateCrossLocation's
// job — a selection can be perfectly valid here and still have no counterpart
// on the other end.
func validatePgSchema(side string, loc DBMSLocation) error {
	if len(loc.PgSchema) == 0 {
		return nil
	}
	if loc.DBMSType != DBMSTypePostgreSQL {
		return fmt.Errorf(
			"%s.pgSchema is PostgreSQL-only and cannot be used with dbmsType %q: "+
				"MySQL and MariaDB treat a schema as a database (set %s.database instead), "+
				"and MongoDB has no schemas",
			side, loc.DBMSType, side)
	}
	seen := make(map[string]bool, len(loc.PgSchema))
	for i, name := range loc.PgSchema {
		// The rules mirror ValidateDatabaseName's PostgreSQL branch — both a
		// schema and a database reach the engine as a double-quoted identifier
		// in generated DDL, and NAMEDATALEN bounds them alike — but are spelled
		// out here so the message names the schema rather than a database.
		reject := func(reason string) error {
			return fmt.Errorf("%s.pgSchema[%d] %q is not usable as a schema name: %s", side, i, name, reason)
		}
		switch {
		case name == "":
			return fmt.Errorf("%s.pgSchema[%d] is empty", side, i)
		case !utf8.ValidString(name):
			return reject("name is not valid UTF-8")
		case len(name) > pgMaxSchemaNameLen:
			return reject(fmt.Sprintf("name is longer than the %d bytes PostgreSQL allows", pgMaxSchemaNameLen))
		case strings.ContainsAny(name, "\x00\n\r"):
			return reject("name contains a NUL or a line break")
		case strings.Contains(name, `"`):
			// Double quotes delimit the identifier in the generated DDL.
			return reject("name contains a double quote")
		case drvpkg.IsSystemSchema(loc.DBMSType, name):
			return reject("it is a PostgreSQL system schema")
		case seen[name]:
			return fmt.Errorf("%s.pgSchema lists %q more than once", side, name)
		}
		seen[name] = true
	}
	return nil
}

// pgMaxSchemaNameLen is the longest schema name PostgreSQL accepts, in bytes:
// NAMEDATALEN minus the terminator, the same bound a database name has.
const pgMaxSchemaNameLen = 63

func validateDirectConfig(side string, cfg *DirectConfig) error {
	if cfg.Host == "" {
		return fmt.Errorf("%s.direct.host is required", side)
	}
	return nil
}

func validateSSHTunnelConfig(side string, cfg *SSHTunnelConfig) error {
	if cfg.SSH == nil {
		return fmt.Errorf("%s.sshTunnel.ssh is required", side)
	}
	if cfg.SSH.Host == "" {
		return fmt.Errorf("%s.sshTunnel.ssh.host is required", side)
	}
	if cfg.SSH.Username == "" {
		return fmt.Errorf("%s.sshTunnel.ssh.username is required", side)
	}
	return nil
}

// validateSameEngine refuses a migration whose two ends run different engines.
//
// Nothing downstream is built to translate between them. A dump is the source
// engine's own dialect — SHOW CREATE TABLE output, a pg_dump script, a MongoDB
// archive — and the restore hands it to the destination verbatim, so a
// cross-engine pair fails somewhere in the middle of the transfer rather than at
// the start. Refusing it here turns that into one clear message before anything
// is read.
//
// The rule is not new; it is where the rule now lives. cm-centipede enforced it
// in its own planning layer, which left the library itself accepting a pair it
// cannot carry out.
func validateSameEngine(src, dst DBMSLocation) error {
	if src.DBMSType == dst.DBMSType {
		return nil
	}
	return fmt.Errorf(
		"source.dbmsType %q and destination.dbmsType %q differ: "+
			"migration between different database engines is not supported",
		src.DBMSType, dst.DBMSType)
}

func validateCrossLocation(src, dst DBMSLocation) error {
	if err := validateSameEngine(src, dst); err != nil {
		return err
	}
	if src.DBMSType == DBMSTypeMongoDB || dst.DBMSType == DBMSTypeMongoDB {
		if src.AccessType != dst.AccessType {
			return fmt.Errorf(
				"MongoDB migration requires identical accessType on source and destination (got %q and %q): "+
					"direct mode produces JSON staging while ssh-tunnel mode produces archive format (incompatible)",
				src.AccessType, dst.AccessType,
			)
		}
	}
	return validatePgSchemaMapping(src, dst)
}

// validatePgSchemaMapping checks how the two sides' schema selections line up.
//
// An empty destination.pgSchema reproduces the source schema names as they are,
// whatever their number. A non-empty one renames them, matched by position, and
// so must have exactly as many entries as the source selects — which is why the
// source has to be spelled out for a rename of more than one schema: an
// unlisted source resolves to whatever the database happens to hold at run time,
// and positions would then be assigned to a list the caller never saw.
//
// Fewer destination names than source schemas would merge several schemas into
// one. That is refused outright rather than deferred to the engine: the merged
// schemas may each hold a table of the same name, and picking a winner (or a
// prefix) is not a decision a migration tool should make silently.
func validatePgSchemaMapping(src, dst DBMSLocation) error {
	if len(dst.PgSchema) == 0 {
		return nil
	}

	// A rename has to be expressible in the dump the plan will produce. The
	// ssh-tunnel-to-ssh-tunnel pipe streams pg_dump straight into psql, and
	// pg_dump has already written CREATE SCHEMA and SET search_path by the time
	// the bytes leave the source host, so there is no point at which the names
	// could be rewritten. The staging path builds its own DDL and can.
	if src.IsSSHTunnel() && dst.IsSSHTunnel() {
		return fmt.Errorf(
			"destination.pgSchema cannot rename schemas when both sides use accessType %q: "+
				"the dump is streamed from pg_dump into psql with the source schema names already in it; "+
				"use accessType %q on one side so the migration stages the dump, or leave destination.pgSchema empty",
			AccessTypeSSHTunnel, AccessTypeDirect)
	}

	// An unlisted source resolves to every user schema in the database. That is
	// unambiguous only when the rename has a single target, since there is then
	// nothing to line up in the wrong order — the driver still rejects the case
	// where the database turns out to hold more than one schema.
	if len(src.PgSchema) == 0 {
		if len(dst.PgSchema) == 1 {
			return nil
		}
		return fmt.Errorf(
			"destination.pgSchema lists %d schemas but source.pgSchema is empty: "+
				"renaming several schemas matches them by position, so the source schemas must be listed explicitly",
			len(dst.PgSchema))
	}

	if len(dst.PgSchema) != len(src.PgSchema) {
		if len(dst.PgSchema) < len(src.PgSchema) {
			return fmt.Errorf(
				"destination.pgSchema lists %d schemas for %d source schemas: "+
					"merging schemas is not supported, because objects of the same name in different "+
					"source schemas would collide; give one destination schema per source schema",
				len(dst.PgSchema), len(src.PgSchema))
		}
		return fmt.Errorf(
			"destination.pgSchema lists %d schemas but source.pgSchema lists %d: "+
				"schemas are renamed by position, so the two lists must be the same length",
			len(dst.PgSchema), len(src.PgSchema))
	}
	return nil
}
