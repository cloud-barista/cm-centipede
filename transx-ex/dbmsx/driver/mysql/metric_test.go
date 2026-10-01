package mysql

import (
	"testing"

	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
)

// bp returns a pointer to b (test helper for *bool metric fields).
func bp(b bool) *bool { return &b }

// =============================================================================
// boolOr
// =============================================================================

func TestBoolOr(t *testing.T) {
	if got := boolOr(nil, true); got != true {
		t.Errorf("boolOr(nil, true) = %v, want true", got)
	}
	if got := boolOr(nil, false); got != false {
		t.Errorf("boolOr(nil, false) = %v, want false", got)
	}
	if got := boolOr(bp(false), true); got != false {
		t.Errorf("boolOr(&false, true) = %v, want false", got)
	}
	if got := boolOr(bp(true), false); got != true {
		t.Errorf("boolOr(&true, false) = %v, want true", got)
	}
}

// =============================================================================
// Nil defaults — every toggle is off
// =============================================================================

func TestMetricResolve_NilDefaults(t *testing.T) {
	loc := base.DBMSLocation{} // Metric == nil

	if m := mysqlMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mysqlMetric(nil) = %+v, want zero value", m)
	}

	// An empty MetricOption (present but no engine sub-struct) is also all-default.
	loc.Metric = &base.MetricOption{}
	if m := mysqlMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mysqlMetric(empty MetricOption) = %+v, want zero value", m)
	}
}

// =============================================================================
// Partial merge — only explicitly set fields change; explicit false stays false
// =============================================================================

func TestMetricResolve_PartialMerge(t *testing.T) {
	loc := base.DBMSLocation{Metric: &base.MetricOption{MySQL: &base.MySQLMetric{
		Columns:       bp(true),
		RowCountExact: bp(false), // explicit false must remain false
		Procedures:    bp(true),
	}}}
	m := mysqlMetric(loc)

	if !m.columns {
		t.Error("columns should be true (explicitly set)")
	}
	if !m.procedures {
		t.Error("procedures should be true (explicitly set)")
	}
	if m.rowCountExact {
		t.Error("rowCountExact should be false (explicitly set false)")
	}
	// Unset fields default to false.
	if m.indexes || m.views || m.functions || m.triggers || m.events || m.foreignKeys {
		t.Errorf("unset fields should default false, got %+v", m)
	}
}

// =============================================================================
// needPerTableScan
// =============================================================================

func TestMetric_NeedPerTableScan(t *testing.T) {
	cases := []struct {
		name string
		m    sqlInspectMetric
		want bool
	}{
		{"empty", sqlInspectMetric{}, false},
		{"rowCountExact", sqlInspectMetric{rowCountExact: true}, true},
		{"columns", sqlInspectMetric{columns: true}, true},
		{"indexes", sqlInspectMetric{indexes: true}, true},
		{"only schema object", sqlInspectMetric{views: true}, false},
	}
	for _, c := range cases {
		if got := c.m.needPerTableScan(); got != c.want {
			t.Errorf("%s: needPerTableScan() = %v, want %v", c.name, got, c.want)
		}
	}
}

// =============================================================================
// needSchemaScan
// =============================================================================

func TestMetric_NeedSchemaScan(t *testing.T) {
	if (sqlInspectMetric{}).needSchemaScan() {
		t.Error("empty sqlInspectMetric.needSchemaScan() should be false")
	}
	// Per-table flags alone do not trigger a schema scan.
	if (sqlInspectMetric{rowCountExact: true, columns: true, indexes: true}).needSchemaScan() {
		t.Error("per-table flags must NOT trigger needSchemaScan")
	}
	for _, m := range []sqlInspectMetric{
		{foreignKeys: true}, {views: true}, {functions: true},
		{procedures: true}, {triggers: true}, {events: true},
	} {
		if !m.needSchemaScan() {
			t.Errorf("%+v.needSchemaScan() should be true", m)
		}
	}
}

// =============================================================================
// Engine isolation — only the MySQL sub-struct is read
// =============================================================================

func TestMetric_WrongEngineIgnored(t *testing.T) {
	// PostgreSQL metric set, but the MySQL resolver must ignore it.
	loc := base.DBMSLocation{Metric: &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{
		Columns: bp(true), Views: bp(true), Sequences: bp(true),
	}}}
	if m := mysqlMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mysqlMetric must ignore PostgreSQL metric, got %+v", m)
	}

	// MySQL set only: the MySQL resolver reads it.
	loc2 := base.DBMSLocation{Metric: &base.MetricOption{MySQL: &base.MySQLMetric{Views: bp(true)}}}
	if !mysqlMetric(loc2).views {
		t.Error("mysqlMetric should read MySQL metric")
	}
}
