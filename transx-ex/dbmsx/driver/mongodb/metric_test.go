package mongodb

import (
	"testing"

	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
)

// bp returns a pointer to b (test helper for *bool metric fields).
func bp(b bool) *bool { return &b }

// ip returns a pointer to n (test helper for *int metric fields).
func ip(n int) *int { return &n }

// =============================================================================
// Nil defaults — every toggle is off; sampleSize defaults to DefaultSampleSize
// =============================================================================

func TestMetricResolve_NilDefaults(t *testing.T) {
	loc := base.DBMSLocation{} // Metric == nil

	m := mongoMetric(loc)
	if m.rowCountExact || m.columns || m.indexes || m.views || m.validators {
		t.Errorf("mongoMetric(nil) toggles = %+v, want all false", m)
	}
	if m.sampleSize != DefaultSampleSize {
		t.Errorf("mongoMetric(nil).sampleSize = %d, want %d", m.sampleSize, DefaultSampleSize)
	}
}

// =============================================================================
// needPerCollectionScan
// =============================================================================

func TestMetric_NeedPerCollectionScan(t *testing.T) {
	if (mongoInspectMetric{}).needPerCollectionScan() {
		t.Error("empty mongoInspectMetric.needPerCollectionScan() should be false")
	}
	if (mongoInspectMetric{rowCountExact: true}).needPerCollectionScan() {
		t.Error("rowCountExact alone must NOT trigger per-collection scan")
	}
	if !(mongoInspectMetric{columns: true}).needPerCollectionScan() {
		t.Error("columns should trigger per-collection scan")
	}
}

// =============================================================================
// needSchemaScan
// =============================================================================

func TestMetric_NeedSchemaScan(t *testing.T) {
	if (mongoInspectMetric{}).needSchemaScan() {
		t.Error("empty mongoInspectMetric.needSchemaScan() should be false")
	}
	if !(mongoInspectMetric{views: true}).needSchemaScan() {
		t.Error("mongo views should trigger needSchemaScan")
	}
	if !(mongoInspectMetric{validators: true}).needSchemaScan() {
		t.Error("mongo validators should trigger needSchemaScan")
	}
}

// =============================================================================
// Engine isolation — the MongoDB resolver ignores other engines' sub-structs
// =============================================================================

func TestMetric_WrongEngineIgnored(t *testing.T) {
	loc := base.DBMSLocation{Metric: &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{
		Columns: bp(true), Views: bp(true), Sequences: bp(true),
	}}}
	if mongoMetric(loc).columns {
		t.Error("mongoMetric must ignore PostgreSQL metric")
	}
}

// =============================================================================
// SampleSize resolution
// =============================================================================

func TestMongoMetric_SampleSize(t *testing.T) {
	cases := []struct {
		name string
		in   *int
		want int
	}{
		{"unset", nil, DefaultSampleSize},
		{"zero", ip(0), DefaultSampleSize},
		{"negative", ip(-5), DefaultSampleSize},
		{"explicit", ip(25), 25},
	}
	for _, c := range cases {
		loc := base.DBMSLocation{Metric: &base.MetricOption{MongoDB: &base.MongoDBMetric{SampleSize: c.in}}}
		if got := mongoMetric(loc).sampleSize; got != c.want {
			t.Errorf("%s: sampleSize = %d, want %d", c.name, got, c.want)
		}
	}
}
