package base

import (
	"reflect"
	"testing"
)

// --- IsSystemDatabase ---

func TestIsSystemDatabase_ManagedServiceDatabases(t *testing.T) {
	// A managed service adds databases of its own that enumerate like any other
	// but refuse CONNECT: RDS shows "rdsadmin" on PostgreSQL and MariaDB and an
	// "innodb" database on MariaDB, Cloud SQL shows "cloudsqladmin", Azure shows
	// "azure_maintenance". None of them is a migration source.
	cases := []struct {
		engine string
		name   string
	}{
		{DBMSTypePostgreSQL, "rdsadmin"},
		{DBMSTypePostgreSQL, "cloudsqladmin"},
		{DBMSTypePostgreSQL, "azure_maintenance"},
		{DBMSTypePostgreSQL, "azure_sys"},
		{DBMSTypeMariaDB, "innodb"},
		{DBMSTypeMariaDB, "rdsadmin"},
		{DBMSTypeMySQL, "innodb"},
		{DBMSTypeMySQL, "rdsadmin"},
	}
	for _, c := range cases {
		if !IsSystemDatabase(c.engine, c.name) {
			t.Errorf("IsSystemDatabase(%s, %q) = false, want true", c.engine, c.name)
		}
	}
}

func TestIsSystemDatabase_ManagedNamesAreNotShared(t *testing.T) {
	// The managed names are per engine, not a global blocklist: MongoDB has no
	// such service database, and "innodb" means nothing to PostgreSQL.
	for _, c := range []struct{ engine, name string }{
		{DBMSTypeMongoDB, "rdsadmin"},
		{DBMSTypePostgreSQL, "innodb"},
		{DBMSTypeMySQL, "cloudsqladmin"},
	} {
		if IsSystemDatabase(c.engine, c.name) {
			t.Errorf("IsSystemDatabase(%s, %q) = true, want false", c.engine, c.name)
		}
	}
}

// --- UserDatabases ---

func TestUserDatabases_FiltersAndSorts(t *testing.T) {
	got := UserDatabases(DBMSTypePostgreSQL,
		[]string{"sales", "rdsadmin", "postgres", "", "template1", "hr"})
	want := []string{"hr", "sales"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UserDatabases = %v, want %v", got, want)
	}
}

func TestUserDatabases_NilWhenNoneRemain(t *testing.T) {
	// An RDS MariaDB server with no user database left holds exactly this set.
	got := UserDatabases(DBMSTypeMariaDB,
		[]string{"information_schema", "innodb", "mysql", "performance_schema", "sys"})
	if got != nil {
		t.Errorf("UserDatabases = %v, want nil", got)
	}
}

// --- IsSystemSchema ---

func TestIsSystemSchema_PostgreSQL(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"pg_catalog", true},
		{"pg_toast", true},
		{"information_schema", true},
		// Temporary schemas are numbered per session, so the whole pg_ prefix is
		// reserved rather than a fixed list of names.
		{"pg_temp_1", true},
		{"pg_toast_temp_7", true},
		{"public", false},
		{"sales", false},
		// The reservation is a prefix rule, not a substring one.
		{"my_pg_schema", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsSystemSchema(DBMSTypePostgreSQL, c.name); got != c.want {
			t.Errorf("IsSystemSchema(postgresql, %q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsSystemSchema_OtherEnginesHaveNone(t *testing.T) {
	// MySQL/MariaDB spell a schema "database", so information_schema is caught by
	// IsSystemDatabase instead; MongoDB has no schema layer at all.
	for _, engine := range []string{DBMSTypeMySQL, DBMSTypeMariaDB, DBMSTypeMongoDB} {
		for _, name := range []string{"information_schema", "pg_catalog", "public"} {
			if IsSystemSchema(engine, name) {
				t.Errorf("IsSystemSchema(%s, %q) = true, want false", engine, name)
			}
		}
	}
}

// --- UserSchemas ---

func TestUserSchemas_FiltersAndSorts(t *testing.T) {
	got := UserSchemas(DBMSTypePostgreSQL,
		[]string{"sales", "pg_catalog", "public", "", "information_schema", "hr", "pg_temp_3"})
	want := []string{"hr", "public", "sales"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UserSchemas = %v, want %v", got, want)
	}
}

func TestUserSchemas_NilWhenNoneRemain(t *testing.T) {
	if got := UserSchemas(DBMSTypePostgreSQL, []string{"pg_catalog", "information_schema"}); got != nil {
		t.Errorf("UserSchemas = %v, want nil", got)
	}
}

func TestUserSchemas_OtherEnginesKeepEverything(t *testing.T) {
	// Nothing is a system schema off PostgreSQL, so the call only sorts.
	got := UserSchemas(DBMSTypeMySQL, []string{"b", "a"})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UserSchemas = %v, want %v", got, want)
	}
}
