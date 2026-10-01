package base

// =============================================================================
// Metric Options
// =============================================================================

// MetricOption selects which information Inspect extracts. It splits by engine
// because schema objects differ per DBMS (e.g. events are MySQL/MariaDB-only,
// materialized views/sequences/rules are PostgreSQL-only). Only the field
// matching DBMSLocation.DBMSType is consulted.
//
// Inspect only; ignored by dumps and migrations.
type MetricOption struct {
	MySQL      *MySQLMetric      `json:"mysql,omitempty"`
	MariaDB    *MariaDBMetric    `json:"mariadb,omitempty"`
	PostgreSQL *PostgreSQLMetric `json:"postgresql,omitempty"`
	MongoDB    *MongoDBMetric    `json:"mongodb,omitempty"`
}

// MySQLMetric toggles the information collected by MySQLDriver.Inspect.
//
// Fields are *bool for partial merge: an omitted field (nil) keeps its default
// and only an explicitly set field overrides it. Server version, database size
// and the table list (with statistics-based row estimates) are always
// collected; every field below is opt-in and defaults to false.
type MySQLMetric struct {
	RowCountExact *bool `json:"rowCountExact,omitempty"` // exact SELECT COUNT(*) per table (else statistics estimate)
	Columns       *bool `json:"columns,omitempty"`       // per-table column metadata
	Indexes       *bool `json:"indexes,omitempty"`       // per-table index metadata
	ForeignKeys   *bool `json:"foreignKeys,omitempty"`   // foreign-key constraint names
	Views         *bool `json:"views,omitempty"`         // view names
	Functions     *bool `json:"functions,omitempty"`     // function names
	Procedures    *bool `json:"procedures,omitempty"`    // procedure names
	Triggers      *bool `json:"triggers,omitempty"`      // trigger names
	Events        *bool `json:"events,omitempty"`        // event names
}

// MariaDBMetric toggles the information collected by MariaDBDriver.Inspect.
// MariaDB is MySQL-compatible, so its object set matches MySQLMetric; it is a
// distinct type to allow the two engines to diverge later.
type MariaDBMetric struct {
	RowCountExact *bool `json:"rowCountExact,omitempty"`
	Columns       *bool `json:"columns,omitempty"`
	Indexes       *bool `json:"indexes,omitempty"`
	ForeignKeys   *bool `json:"foreignKeys,omitempty"`
	Views         *bool `json:"views,omitempty"`
	Functions     *bool `json:"functions,omitempty"`
	Procedures    *bool `json:"procedures,omitempty"`
	Triggers      *bool `json:"triggers,omitempty"`
	Events        *bool `json:"events,omitempty"`
}

// PostgreSQLMetric toggles the information collected by PostgreSQLDriver.Inspect.
// Same *bool partial-merge / opt-in rules as MySQLMetric.
type PostgreSQLMetric struct {
	RowCountExact     *bool `json:"rowCountExact,omitempty"`
	Columns           *bool `json:"columns,omitempty"`
	Indexes           *bool `json:"indexes,omitempty"`
	ForeignKeys       *bool `json:"foreignKeys,omitempty"`
	Views             *bool `json:"views,omitempty"`
	MaterializedViews *bool `json:"materializedViews,omitempty"`
	Functions         *bool `json:"functions,omitempty"`
	Procedures        *bool `json:"procedures,omitempty"`
	Triggers          *bool `json:"triggers,omitempty"`
	Sequences         *bool `json:"sequences,omitempty"`
	Types             *bool `json:"types,omitempty"`
	Extensions        *bool `json:"extensions,omitempty"`
	Rules             *bool `json:"rules,omitempty"`
}

// MongoDBMetric toggles the information collected by MongoDBDriver.Inspect.
// MongoDB is schemaless, so Columns is field inference by sampling; SampleSize
// controls how many documents are sampled (default 100). Same *bool
// partial-merge / opt-in rules as the other engines.
type MongoDBMetric struct {
	RowCountExact *bool `json:"rowCountExact,omitempty"` // exact countDocuments per collection (else collStats estimate)
	Columns       *bool `json:"columns,omitempty"`       // per-collection field inference (sampling)
	Indexes       *bool `json:"indexes,omitempty"`       // per-collection index metadata
	Views         *bool `json:"views,omitempty"`         // view names (viewOn/pipeline collections)
	Validators    *bool `json:"validators,omitempty"`    // collections carrying a schema validator
	SampleSize    *int  `json:"sampleSize,omitempty"`    // documents sampled for field inference (default 100)
}
