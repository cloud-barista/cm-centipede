package postgresql

import (
	"testing"

	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
)

// bp returns a pointer to b (test helper for *bool metric fields).
func bp(b bool) *bool { return &b }

// =============================================================================
// Nil defaults — every toggle is off
// =============================================================================

func TestMetricResolve_NilDefaults(t *testing.T) {
	loc := base.DBMSLocation{} // Metric == nil

	if m := pgMetric(loc); m != (pgInspectMetric{}) {
		t.Errorf("pgMetric(nil) = %+v, want zero value", m)
	}

	// An empty MetricOption (present but no engine sub-struct) is also all-default.
	loc.Metric = &base.MetricOption{}
	if m := pgMetric(loc); m != (pgInspectMetric{}) {
		t.Errorf("pgMetric(empty MetricOption) = %+v, want zero value", m)
	}
}

// =============================================================================
// needPerTableScan
// =============================================================================

func TestMetric_NeedPerTableScan(t *testing.T) {
	if (pgInspectMetric{}).needPerTableScan() {
		t.Error("empty pgInspectMetric.needPerTableScan() should be false")
	}
	if !(pgInspectMetric{indexes: true}).needPerTableScan() {
		t.Error("pgInspectMetric{indexes}.needPerTableScan() should be true")
	}
}

// =============================================================================
// needSchemaScan
// =============================================================================

func TestMetric_NeedSchemaScan(t *testing.T) {
	if (pgInspectMetric{}).needSchemaScan() {
		t.Error("empty pgInspectMetric.needSchemaScan() should be false")
	}
	for _, m := range []pgInspectMetric{
		{materializedViews: true}, {sequences: true}, {types: true},
		{extensions: true}, {rules: true}, {foreignKeys: true},
	} {
		if !m.needSchemaScan() {
			t.Errorf("%+v.needSchemaScan() should be true", m)
		}
	}
}

// =============================================================================
// Engine isolation — the PostgreSQL resolver reads the PostgreSQL sub-struct
// =============================================================================

func TestMetric_WrongEngineIgnored(t *testing.T) {
	loc := base.DBMSLocation{Metric: &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{
		Columns: bp(true), Views: bp(true), Sequences: bp(true),
	}}}
	if pm := pgMetric(loc); !pm.columns || !pm.views || !pm.sequences {
		t.Errorf("pgMetric should read PostgreSQL metric, got %+v", pm)
	}
}

// =============================================================================
// pgSchemaObjectFields — requested objects bind to the right DBMSInfo fields
// =============================================================================

func TestPgSchemaObjectFields(t *testing.T) {
	schemas := []string{"public"}

	// Nothing requested → no fields.
	if got := pgSchemaObjectFields(pgInspectMetric{}, &base.DBMSInfo{}, schemas); len(got) != 0 {
		t.Errorf("empty metric should yield 0 fields, got %d", len(got))
	}

	info := &base.DBMSInfo{}
	m := pgInspectMetric{
		foreignKeys: true, views: true, materializedViews: true,
		functions: true, procedures: true, triggers: true,
		sequences: true, types: true, extensions: true, rules: true,
	}
	fields := pgSchemaObjectFields(m, info, schemas)
	if len(fields) != 10 {
		t.Fatalf("all-on metric should yield 10 fields, got %d", len(fields))
	}

	// Each destination must point at the matching DBMSInfo slice, and each
	// query must be the one that enumerates that object kind.
	byQuery := map[string]*[]string{
		pgObjectQueryForeignKeys(schemas):   &info.ForeignKeys,
		pgObjectQueryViews(schemas):         &info.Views,
		pgObjectQueryMatViews(schemas):      &info.MaterializedViews,
		pgObjectQueryRoutines(schemas, "f"): &info.Functions,
		pgObjectQueryRoutines(schemas, "p"): &info.Procedures,
		pgObjectQueryTriggers(schemas):      &info.Triggers,
		pgObjectQuerySequences(schemas):     &info.Sequences,
		pgObjectQueryTypes(schemas):         &info.Types,
		pgObjectQueryExtensions:             &info.Extensions,
		pgObjectQueryRules(schemas):         &info.Rules,
	}
	for _, f := range fields {
		want, ok := byQuery[f.query]
		if !ok {
			t.Errorf("unexpected query: %q", f.query)
			continue
		}
		if f.dst != want {
			t.Errorf("query %q bound to wrong DBMSInfo field", f.query)
		}
	}

	// A subset request yields only those fields.
	sub := pgSchemaObjectFields(pgInspectMetric{views: true, sequences: true}, &base.DBMSInfo{}, schemas)
	if len(sub) != 2 {
		t.Errorf("subset metric should yield 2 fields, got %d", len(sub))
	}
}
