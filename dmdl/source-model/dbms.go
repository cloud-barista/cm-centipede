package sourcemodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// DBColumnInfo mirrors dbmsx ColumnInfo (metadata of one column/field).
type DBColumnInfo struct {
	Name       string `json:"name"`
	DataType   string `json:"dataType"`
	IsNullable bool   `json:"isNullable,omitempty"`
	Default    string `json:"default,omitempty"`
	IsPrimary  bool   `json:"isPrimary,omitempty"`
	Comment    string `json:"comment,omitempty"`

	// CharacterSet and Collation are the column's own text handling, and appear
	// only where the engine records them per column. MySQL and MariaDB fill both
	// for string columns and neither for the rest. PostgreSQL fills Collation
	// alone, and only when the column declares one explicitly — a column that
	// inherits the database default reports nothing, which DBMSInfo.Collation
	// already says. MongoDB fills neither: its fields are inferred by sampling.
	CharacterSet string `json:"characterSet,omitempty"`
	Collation    string `json:"collation,omitempty"`
}

// DBIndexInfo mirrors dbmsx IndexInfo.
type DBIndexInfo struct {
	Name     string   `json:"name"`
	Columns  []string `json:"columns"`
	IsUnique bool     `json:"isUnique,omitempty"`
	Type     string   `json:"type,omitempty"`
}

// DBPgSchemaInfo mirrors dbmsx PgSchemaInfo: the per-schema breakdown of the
// four size figures DBMSInfo reports for the database. PostgreSQL only.
type DBPgSchemaInfo struct {
	Name       string `json:"name"`
	TotalSize  int64  `json:"totalSize"`
	DataSize   int64  `json:"dataSize"`
	IndexSize  int64  `json:"indexSize"`
	TableCount int    `json:"tableCount"`
}

// DBTableInfo mirrors dbmsx TableInfo (one table/collection).
// Name is the bare table name; on PostgreSQL the schema holding it is carried
// separately in PgSchema, so a caller that keys on the name keeps working when a
// database holds more than one schema.
//
// Columns and Indexes are opt-in on the inspect side, so they are omitted rather
// than serialised as null when the source did not collect them.
type DBTableInfo struct {
	PgSchema  string `json:"pgSchema,omitempty"`
	Name      string `json:"name"`
	RowCount  int64  `json:"rowCount"`
	DataSize  int64  `json:"dataSize"`
	IndexSize int64  `json:"indexSize"`
	TotalSize int64  `json:"totalSize"`
	Engine    string `json:"engine,omitempty"`

	// CharacterSet and Collation are the table's own text defaults, which its
	// columns inherit unless they declare their own. PostgreSQL has no
	// table-level collation and leaves both empty; MongoDB reports the
	// collection's default collation locale in Collation and leaves CharacterSet
	// empty.
	CharacterSet string `json:"characterSet,omitempty"`
	Collation    string `json:"collation,omitempty"`

	Columns []DBColumnInfo `json:"columns,omitempty"`
	Indexes []DBIndexInfo  `json:"indexes,omitempty"`
}

// DBMSInfo mirrors dbmsx.DBMSInfo in full (tables plus the schema object
// inventory). It uses the same camelCase json tags as dbmsx so that JSON
// produced by dbmsx parses and re-serialises unchanged, and is defined here
// rather than imported so the model package stays free of a dbmsx dependency.
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

	// TotalSize..TableCount are the sum over the schemas the inspection covered,
	// not the size of the database as PostgreSQL reports it. PgSchema breaks them
	// down per schema and always sums back to them; a schema holding no tables is
	// listed at zero rather than omitted. PostgreSQL only.
	TotalSize  int64            `json:"totalSize"`
	DataSize   int64            `json:"dataSize"`
	IndexSize  int64            `json:"indexSize"`
	TableCount int              `json:"tableCount"`
	PgSchema   []DBPgSchemaInfo `json:"pgSchema,omitempty"`
	Tables     []DBTableInfo    `json:"tables"`

	// Schema object inventory (name list) — populated only on metric opt-in.
	ForeignKeys       []string `json:"foreignKeys,omitempty"`
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

// SourceDBModel represents one DBMS source connection and the metadata of every
// database inspected through it. dbType is not carried here: the engine is a
// property of the connection, so the plan layer derives it from there rather
// than trusting a copy that could disagree.
//
// Databases is a list because a connection names a server, and a server holds as
// many databases as the inspection covered. It is a list even for a single
// database, so a consumer never has to handle two shapes — the same rule
// transx-ex's InspectDBMSAll follows — and it lines up 1:1 with
// targetmodel.MigrationDBModel, which pairs one connection with the databases
// migrated under it.
type SourceDBModel struct {
	Connection commonmodel.ConnectionRef `json:"connection"`
	Databases  []DBMSInfo                `json:"databases"`
}
