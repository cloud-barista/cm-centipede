package mariadb

import base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"

// =============================================================================
// Resolved metric selections
// =============================================================================

// boolOr returns *p when set, otherwise def.
func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// sqlInspectMetric is the resolved metric selection shared by the MySQL and
// MariaDB drivers (identical object model).
type sqlInspectMetric struct {
	rowCountExact bool
	columns       bool
	indexes       bool
	foreignKeys   bool
	views         bool
	functions     bool
	procedures    bool
	triggers      bool
	events        bool
}

// needPerTableScan reports whether the per-table loop must issue extra queries.
func (m sqlInspectMetric) needPerTableScan() bool {
	return m.rowCountExact || m.columns || m.indexes
}

// needSchemaScan reports whether any database-wide schema object was requested.
func (m sqlInspectMetric) needSchemaScan() bool {
	return m.foreignKeys || m.views || m.functions || m.procedures || m.triggers || m.events
}

// mariadbMetric resolves loc.Metric.MariaDB into concrete flags.
func mariadbMetric(loc base.DBMSLocation) sqlInspectMetric {
	if loc.Metric == nil || loc.Metric.MariaDB == nil {
		return sqlInspectMetric{}
	}
	m := loc.Metric.MariaDB
	return sqlInspectMetric{
		rowCountExact: boolOr(m.RowCountExact, false),
		columns:       boolOr(m.Columns, false),
		indexes:       boolOr(m.Indexes, false),
		foreignKeys:   boolOr(m.ForeignKeys, false),
		views:         boolOr(m.Views, false),
		functions:     boolOr(m.Functions, false),
		procedures:    boolOr(m.Procedures, false),
		triggers:      boolOr(m.Triggers, false),
		events:        boolOr(m.Events, false),
	}
}
