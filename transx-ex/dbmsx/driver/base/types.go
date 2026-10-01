package base

import (
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
)

// =============================================================================
// DBMS Type Constants
// =============================================================================

const (
	DBMSTypeMySQL      = "mysql"
	DBMSTypeMariaDB    = "mariadb"
	DBMSTypePostgreSQL = "postgresql"
	DBMSTypeMongoDB    = "mongodb"
)

// =============================================================================
// Access Type Constants
// =============================================================================

const (
	AccessTypeDirect    = "direct"
	AccessTypeSSHTunnel = "ssh-tunnel"
)

// =============================================================================
// Provider Constants
// =============================================================================

// The infrastructure a database can be hosted on: one of the cloud providers, or
// on-premises. These are the values cm-honeybee accepts for a source group's
// provider, spelled the same way — lower case, and "onprem" rather than any of
// the longer forms — so a name resolved there can be passed through unchanged.
//
// The list is not a constraint. DBMSLocation.ProviderName takes any string, and
// nothing in this library reads it; the constants exist so that callers naming
// the same provider spell it the same way.
const (
	ProviderAWS       = "aws"
	ProviderAlibaba   = "alibaba"
	ProviderTencent   = "tencent"
	ProviderNCP       = "ncp"
	ProviderNHN       = "nhn"
	ProviderIBM       = "ibm"
	ProviderGCP       = "gcp"
	ProviderKT        = "kt"
	ProviderAzure     = "azure"
	ProviderOpenStack = "openstack"

	// ProviderOnPrem marks a database running on the operator's own
	// infrastructure rather than at a cloud provider.
	ProviderOnPrem = "onprem"
)

// =============================================================================
// Scope Constants
// =============================================================================

const (
	ScopeSchemaOnly = "schema-only"
	ScopeDataOnly   = "data-only"
	ScopeFull       = "full"
)

// =============================================================================
// Staging Constants
// =============================================================================

// DefaultStagingPath is the local directory used to store intermediate dump
// files. It is the DBMS subdirectory of the module-wide staging root, so a
// database dump and a filesystem relay never share a directory.
const DefaultStagingPath = core.DBMSStagingPath

// =============================================================================
// TargetGrant
// =============================================================================

// TargetGrant holds the credentials for the new database user to be created
// during PrepareTarget on the destination DBMS.
type TargetGrant struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Host     string `json:"host,omitempty"` // MySQL/MariaDB only; defaults to "%"
}

// =============================================================================
// Dump Options
// =============================================================================

// DumpOption carries the choices a dump is written under that do not belong to
// either endpoint on its own. A nil option means the dump reproduces the source
// as it stands, which is what every caller outside a migration wants.
//
// It exists because a dump is the one operation that has to know something about
// where its output is going: DBDriver.Dump is handed the source location alone,
// and a schema rename is a fact about the destination. The migration layer
// resolves the two sides against each other and passes the outcome here, so the
// driver never sees two lists it has to line up itself.
type DumpOption struct {
	// PgSchemaRename maps a source schema name to the name its objects carry in
	// the dump. A schema missing from the map keeps its own name, and an empty
	// map renames nothing. PostgreSQL only; the other engines ignore it, since a
	// schema is a database to them and renaming one is choosing a target
	// database.
	PgSchemaRename map[string]string
}

// PgSchemaRenameOpt returns opt's rename map, or nil. Like the create and drop
// option accessors, it tolerates a nil option so driver code can read the field
// without a preceding nil check.
func PgSchemaRenameOpt(opt *DumpOption) map[string]string {
	if opt == nil {
		return nil
	}
	return opt.PgSchemaRename
}

// =============================================================================
// DBMS Location
// =============================================================================

// DBMSLocation identifies a database endpoint along with its access configuration.
type DBMSLocation struct {
	DBMSType string `json:"dbmsType"`
	Database string `json:"database"`
	// PgSchema selects the PostgreSQL schemas this location covers. Empty means
	// every user schema on the database — pg_catalog, information_schema and the
	// pg_* internals excluded. On a destination it doubles as the rename target
	// list, matched to the source schemas by position (see Validate).
	//
	// The field is PostgreSQL-only, as the name says: MySQL/MariaDB treat a
	// schema as a database and MongoDB has no equivalent, so Validate rejects a
	// non-empty value for those engines rather than silently ignoring it.
	PgSchema []string `json:"pgSchema,omitempty"`
	// ProviderName records where the database is hosted — one of the cloud
	// providers, or on-premises infrastructure (ProviderOnPrem). It is carried
	// for the caller's benefit and nothing in this library reads it: no routing,
	// validation or connection decision depends on it, and any value is accepted
	// on any engine.
	//
	// Empty means unspecified, which is not the same as on-premises: a caller
	// that did not record where a database lives has said nothing about it.
	ProviderName string                   `json:"providerName,omitempty"`
	AccessType   string                   `json:"accessType"`
	Direct       *DirectConfig            `json:"direct,omitempty"`
	SSHTunnel    *SSHTunnelConfig         `json:"sshTunnel,omitempty"`
	Filter       *filter.DBMSFilterOption `json:"filter,omitempty"`
	// Metric selects which information Inspect extracts. Inspect only; ignored
	// by dumps and migrations.
	Metric *MetricOption `json:"metric,omitempty"`
}

// IsDirect returns true when the location uses a direct TCP connection.
func (loc DBMSLocation) IsDirect() bool { return loc.AccessType == AccessTypeDirect }

// IsSSHTunnel returns true when the location uses an SSH tunnel connection.
func (loc DBMSLocation) IsSSHTunnel() bool { return loc.AccessType == AccessTypeSSHTunnel }

// IsMongoDB returns true when the location targets a MongoDB instance.
func (loc DBMSLocation) IsMongoDB() bool { return loc.DBMSType == DBMSTypeMongoDB }

// IsOnPrem returns true when the location is recorded as running on the
// operator's own infrastructure. An unspecified ProviderName returns false: a
// location that says nothing about where it lives is not a claim that it is
// on-premises.
//
// The comparison ignores case, matching how cm-honeybee folds a provider name
// before testing it, so a value that reached the caller as "OnPrem" still
// answers the question it was meant to answer.
func (loc DBMSLocation) IsOnPrem() bool {
	return strings.EqualFold(loc.ProviderName, ProviderOnPrem)
}

// =============================================================================
// Connection Configurations
// =============================================================================

// DirectConfig holds parameters for a direct TCP database connection.
type DirectConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	ConnectTimeout int    `json:"connectTimeout,omitempty"`

	// TLSMode selects transport security; the empty value is TLSModeDisable.
	// See tls.go for what the five modes mean and how each engine spells them.
	TLSMode string `json:"tlsMode,omitempty"`

	// TLSCAFile and TLSCAPEM supply the root that verify-ca and verify-full
	// check against; give one or neither, never both. Empty means the system
	// trust store, which carries only publicly trusted CAs.
	TLSCAFile string `json:"tlsCAFile,omitempty"`
	TLSCAPEM  string `json:"tlsCAPem,omitempty"`

	// AuthSource selects the authentication database. MongoDB only; ignored by
	// other drivers. When empty and credentials are given, MongoDB defaults to
	// "admin" (where users are usually created) rather than the target database.
	AuthSource string `json:"authSource,omitempty"`
}

// SSHTunnelConfig holds parameters for an SSH-proxied database connection.
//
// There are no TLS fields here on purpose. This path runs the engine's CLI
// tools on the remote host and passes them no TLS options, so a setting would
// have nothing to act on; the connection it describes is already carried
// inside the SSH session. Transport security for a database reached directly
// is DirectConfig.TLSMode.
type SSHTunnelConfig struct {
	SSH      *SSHConfig `json:"ssh"`
	DBHost   string     `json:"dbHost,omitempty"`
	DBPort   int        `json:"dbPort,omitempty"`
	Username string     `json:"username,omitempty"`
	Password string     `json:"password,omitempty"`
	// AuthSource selects the authentication database (MongoDB only; see DirectConfig).
	AuthSource string `json:"authSource,omitempty"`
}

// SSHConfig is declared in ssh.go as an alias for core.SSHConfig.

// =============================================================================
// Inspection Models
// =============================================================================

// PgSchemaInfo is the per-schema rollup of the four size statistics DBMSInfo
// reports for the location as a whole. PostgreSQL only.
type PgSchemaInfo struct {
	Name       string `json:"name"`
	TotalSize  int64  `json:"totalSize"`
	DataSize   int64  `json:"dataSize"`
	IndexSize  int64  `json:"indexSize"`
	TableCount int    `json:"tableCount"`
}

// DBMSInfo contains database server and schema metadata returned by Inspect().
//
// Schema-object inventories (Views, Functions, …) are name lists populated
// only when the corresponding metric flag is requested via DBMSLocation.Metric;
// each is omitted (nil) otherwise. Engine applicability:
//   - Events:            MySQL / MariaDB
//   - MaterializedViews, Sequences, Types, Extensions, Rules: PostgreSQL
//   - Validators:        MongoDB (collections carrying a schema validator)
//
// PostgreSQL qualifies every inventory entry with its schema (e.g.
// "sales.v_monthly"), quoting each part only where the engine requires it, so
// an entry is both readable and directly usable as a DROP target.
type DBMSInfo struct {
	ServerVersion string `json:"serverVersion"`
	Database      string `json:"database"`

	// CharacterSet is the database's default character set, which on PostgreSQL
	// is its encoding — the two name the same thing there. MongoDB stores UTF-8
	// only and leaves it empty.
	CharacterSet string `json:"characterSet,omitempty"`
	// Collation is the database's default collation: DEFAULT_COLLATION_NAME on
	// MySQL and MariaDB, datcollate on PostgreSQL. MongoDB has no database-level
	// collation and leaves it empty.
	Collation string `json:"collation,omitempty"`
	// Ctype is the locale governing character classification — PostgreSQL's
	// datctype, which the engine keeps separate from the collation order. The
	// other engines have no equivalent and leave it empty.
	Ctype string `json:"ctype,omitempty"`

	// TotalSize..TableCount are the sum over the schemas this location covers
	// (see DBMSLocation.PgSchema), not the size of the database as PostgreSQL
	// reports it: pg_database_size() also counts catalogs, TOAST and schemas
	// that were not selected. For the other engines a schema is a database, so
	// the distinction does not arise.
	TotalSize  int64 `json:"totalSize"`
	DataSize   int64 `json:"dataSize"`
	IndexSize  int64 `json:"indexSize"`
	TableCount int   `json:"tableCount"`
	// PgSchema breaks those four figures down per schema, in name order. It
	// covers every schema the location selected — a schema holding no tables is
	// listed with zeroes rather than omitted — so the totals above always equal
	// the sum of this list. PostgreSQL only.
	PgSchema []PgSchemaInfo `json:"pgSchema,omitempty"`
	Tables   []TableInfo    `json:"tables"`

	// Schema-object inventories (name lists), opt-in via Metric.
	ForeignKeys       []string `json:"foreignKeys,omitempty"` // relational engines
	Views             []string `json:"views,omitempty"`
	Functions         []string `json:"functions,omitempty"`
	Procedures        []string `json:"procedures,omitempty"`
	Triggers          []string `json:"triggers,omitempty"`
	Events            []string `json:"events,omitempty"`            // MySQL / MariaDB
	MaterializedViews []string `json:"materializedViews,omitempty"` // PostgreSQL
	Sequences         []string `json:"sequences,omitempty"`         // PostgreSQL
	Types             []string `json:"types,omitempty"`             // PostgreSQL
	Extensions        []string `json:"extensions,omitempty"`        // PostgreSQL
	Rules             []string `json:"rules,omitempty"`             // PostgreSQL
	Validators        []string `json:"validators,omitempty"`        // MongoDB
}

// TableInfo holds per-table or per-collection metadata.
//
// Name is always the bare table name. On PostgreSQL the owning schema is
// carried separately in PgSchema rather than folded into Name, so a caller that
// keys on the name keeps working when a database holds more than one schema.
//
// Columns and Indexes are opt-in via Metric like the DBMSInfo inventories, and
// are omitted rather than emitted as null when they were not collected.
type TableInfo struct {
	PgSchema  string `json:"pgSchema,omitempty"`
	Name      string `json:"name"`
	RowCount  int64  `json:"rowCount"`
	DataSize  int64  `json:"dataSize"`
	IndexSize int64  `json:"indexSize"`
	TotalSize int64  `json:"totalSize"`
	Engine    string `json:"engine,omitempty"`

	// CharacterSet and Collation are the table's own text defaults, which its
	// columns inherit unless they declare their own. MySQL and MariaDB record
	// only the collation on the table itself, so the character set is joined in
	// from information_schema.COLLATIONS rather than split off the collation
	// name: the prefix that carries it is a naming convention, not something the
	// engine guarantees.
	//
	// PostgreSQL has no table-level collation and leaves both empty. MongoDB
	// stores UTF-8 only, so it fills Collation with the collection's default
	// collation locale and leaves CharacterSet empty.
	CharacterSet string `json:"characterSet,omitempty"`
	Collation    string `json:"collation,omitempty"`

	Columns []ColumnInfo `json:"columns,omitempty"`
	Indexes []IndexInfo  `json:"indexes,omitempty"`
}

// ColumnInfo holds metadata for a single column or document field.
type ColumnInfo struct {
	Name       string `json:"name"`
	DataType   string `json:"dataType"`
	IsNullable bool   `json:"isNullable,omitempty"`
	Default    string `json:"default,omitempty"`
	IsPrimary  bool   `json:"isPrimary,omitempty"`
	Comment    string `json:"comment,omitempty"`

	// CharacterSet and Collation describe the column's own text handling, and
	// are set only where the engine records them per column. MySQL and MariaDB
	// fill both for string columns and neither for the rest. PostgreSQL fills
	// Collation alone, and only when the column declares one explicitly — a
	// column inheriting the database default reports nothing, which is what
	// DBMSInfo.Collation already says. MongoDB fills neither: its fields are
	// inferred by sampling and carry no collation.
	CharacterSet string `json:"characterSet,omitempty"`
	Collation    string `json:"collation,omitempty"`
}

// IndexInfo holds metadata for a single index.
type IndexInfo struct {
	Name     string   `json:"name"`
	Columns  []string `json:"columns"`
	IsUnique bool     `json:"isUnique,omitempty"`
	Type     string   `json:"type,omitempty"`
}
