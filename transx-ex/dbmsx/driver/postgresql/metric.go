package postgresql

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

// pgInspectMetric is the resolved PostgreSQL metric selection.
type pgInspectMetric struct {
	rowCountExact     bool
	columns           bool
	indexes           bool
	foreignKeys       bool
	views             bool
	materializedViews bool
	functions         bool
	procedures        bool
	triggers          bool
	sequences         bool
	types             bool
	extensions        bool
	rules             bool
}

func (m pgInspectMetric) needPerTableScan() bool {
	return m.rowCountExact || m.columns || m.indexes
}

func (m pgInspectMetric) needSchemaScan() bool {
	return m.foreignKeys || m.views || m.materializedViews || m.functions ||
		m.procedures || m.triggers || m.sequences || m.types || m.extensions || m.rules
}

// pgMetric resolves loc.Metric.PostgreSQL into concrete flags (all default false).
func pgMetric(loc base.DBMSLocation) pgInspectMetric {
	if loc.Metric == nil || loc.Metric.PostgreSQL == nil {
		return pgInspectMetric{}
	}
	m := loc.Metric.PostgreSQL
	return pgInspectMetric{
		rowCountExact:     boolOr(m.RowCountExact, false),
		columns:           boolOr(m.Columns, false),
		indexes:           boolOr(m.Indexes, false),
		foreignKeys:       boolOr(m.ForeignKeys, false),
		views:             boolOr(m.Views, false),
		materializedViews: boolOr(m.MaterializedViews, false),
		functions:         boolOr(m.Functions, false),
		procedures:        boolOr(m.Procedures, false),
		triggers:          boolOr(m.Triggers, false),
		sequences:         boolOr(m.Sequences, false),
		types:             boolOr(m.Types, false),
		extensions:        boolOr(m.Extensions, false),
		rules:             boolOr(m.Rules, false),
	}
}
