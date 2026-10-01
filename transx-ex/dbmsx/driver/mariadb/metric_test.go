package mariadb

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

	if m := mariadbMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mariadbMetric(nil) = %+v, want zero value", m)
	}

	// An empty MetricOption (present but no engine sub-struct) is also all-default.
	loc.Metric = &base.MetricOption{}
	if m := mariadbMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mariadbMetric(empty MetricOption) = %+v, want zero value", m)
	}
}

// =============================================================================
// Engine isolation — the MariaDB resolver reads only the MariaDB sub-struct
// =============================================================================

func TestMetric_WrongEngineIgnored(t *testing.T) {
	// PostgreSQL metric set, but the MariaDB resolver must ignore it.
	loc := base.DBMSLocation{Metric: &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{
		Columns: bp(true), Views: bp(true), Sequences: bp(true),
	}}}
	if m := mariadbMetric(loc); m != (sqlInspectMetric{}) {
		t.Errorf("mariadbMetric must ignore PostgreSQL metric, got %+v", m)
	}

	// MySQL set only: MariaDB resolver must ignore the MySQL sub-struct.
	loc2 := base.DBMSLocation{Metric: &base.MetricOption{MySQL: &base.MySQLMetric{Views: bp(true)}}}
	if mariadbMetric(loc2) != (sqlInspectMetric{}) {
		t.Error("mariadbMetric must ignore MySQL metric")
	}

	// MariaDB set: the MariaDB resolver reads it.
	loc3 := base.DBMSLocation{Metric: &base.MetricOption{MariaDB: &base.MariaDBMetric{Views: bp(true)}}}
	if !mariadbMetric(loc3).views {
		t.Error("mariadbMetric should read MariaDB metric")
	}
}
