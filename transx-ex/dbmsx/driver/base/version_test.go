package base

import (
	"reflect"
	"testing"
)

func TestParseServerVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int
	}{
		{"mysql", "8.0.32", []int{8, 0, 32}},
		{"mysql vendor build", "8.0.32-0ubuntu0.22.04.2", []int{8, 0, 32}},
		{"mariadb", "10.6.14-MariaDB-1:10.6.14+maria~ubu2004", []int{10, 6, 14}},
		{"postgresql", "14.23 (Ubuntu 14.23-1.pgdg22.04+1)", []int{14, 23}},
		{"postgresql bare", "16.1", []int{16, 1}},
		{"mongodb", "7.0.5", []int{7, 0, 5}},
		{"major only", "15", []int{15}},

		// SELECT version() output — the form the PostgreSQL driver avoids on
		// purpose, because it cannot be compared.
		{"descriptive string", "PostgreSQL 14.23 on x86_64-pc-linux-gnu", nil},
		{"empty", "", nil},
		{"non-numeric", "unknown", nil},

		// Parsing stops at the first component that is not a number rather than
		// discarding the ones before it.
		{"trailing non-numeric part", "8.0.x", []int{8, 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseServerVersion(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseServerVersion(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestCompareServerVersions(t *testing.T) {
	cases := []struct {
		name       string
		dbmsType   string
		a, b       string
		want       int
		comparable bool
	}{
		// MySQL family: the minor decides, so a patch difference is equivalence.
		{"mysql upgrade", DBMSTypeMySQL, "5.7.42", "8.0.32", -1, true},
		{"mysql downgrade", DBMSTypeMySQL, "8.0.32", "5.7.42", 1, true},
		{"mysql minor upgrade", DBMSTypeMySQL, "8.0.32", "8.1.0", -1, true},
		{"mysql patch ignored", DBMSTypeMySQL, "8.0.35", "8.0.32", 0, true},
		{"mariadb patch ignored", DBMSTypeMariaDB, "10.6.14-MariaDB", "10.6.11-MariaDB", 0, true},
		{"mariadb minor downgrade", DBMSTypeMariaDB, "10.6.14-MariaDB", "10.5.20-MariaDB", 1, true},

		// PostgreSQL 10+: MAJOR.PATCH, so only the major separates two versions.
		{"pg patch ignored", DBMSTypePostgreSQL, "14.5", "14.2", 0, true},
		{"pg major upgrade", DBMSTypePostgreSQL, "14.23", "16.1", -1, true},
		{"pg major downgrade", DBMSTypePostgreSQL, "16.1", "14.23", 1, true},
		// 9.x numbered releases MAJOR.MAJOR.PATCH, where the second component is
		// a feature release and does separate them.
		{"pg legacy minor downgrade", DBMSTypePostgreSQL, "9.6.24", "9.5.25", 1, true},
		{"pg legacy to modern", DBMSTypePostgreSQL, "9.6.24", "14.23", -1, true},

		{"mongodb upgrade", DBMSTypeMongoDB, "6.0.13", "7.0.5", -1, true},
		{"mongodb patch ignored", DBMSTypeMongoDB, "7.0.5", "7.0.2", 0, true},

		// A version that cannot be read reports so instead of comparing.
		{"source unreadable", DBMSTypeMySQL, "", "8.0.32", 0, false},
		{"target unreadable", DBMSTypePostgreSQL, "14.23", "PostgreSQL 16.1 on x86_64", 0, false},

		// Comparison stops at the shallower version rather than treating the
		// absent component as zero.
		{"uneven depth equal", DBMSTypePostgreSQL, "14", "14.2", 0, true},
		{"uneven depth mysql", DBMSTypeMySQL, "8", "8.0.32", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := CompareServerVersions(c.dbmsType, c.a, c.b)
			if ok != c.comparable {
				t.Fatalf("comparable = %v, want %v", ok, c.comparable)
			}
			if ok && got != c.want {
				t.Errorf("CompareServerVersions(%s, %q, %q) = %d, want %d",
					c.dbmsType, c.a, c.b, got, c.want)
			}
		})
	}
}

func TestCheckServerVersions(t *testing.T) {
	downgrade := CheckServerVersions(DBMSTypeMySQL, "8.0.32", "5.7.42")
	if !downgrade.Comparable || !downgrade.Downgrade {
		t.Errorf("8.0 → 5.7 = %+v, want a comparable downgrade", downgrade)
	}
	if downgrade.Source != "8.0.32" || downgrade.Target != "5.7.42" {
		t.Errorf("versions should be carried as reported, got %+v", downgrade)
	}

	upgrade := CheckServerVersions(DBMSTypeMySQL, "5.7.42", "8.0.32")
	if !upgrade.Comparable || upgrade.Downgrade {
		t.Errorf("5.7 → 8.0 = %+v, want a comparable upgrade", upgrade)
	}

	// An unreadable version is reported, not judged: Downgrade stays false so a
	// migration proceeds rather than being blocked on no evidence.
	unknown := CheckServerVersions(DBMSTypePostgreSQL, "PostgreSQL 16.1 on x86_64", "14.23")
	if unknown.Comparable || unknown.Downgrade {
		t.Errorf("unreadable source = %+v, want neither comparable nor a downgrade", unknown)
	}
}
