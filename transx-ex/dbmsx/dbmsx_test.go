package dbmsx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// MigrateDataAsync — unit tests (no live database required)
// =============================================================================

func TestMigrateDataAsync_ReturnsHandleImmediately(t *testing.T) {
	// Minimal valid model: invalid host so run() fails quickly, but the handle
	// must be returned before the goroutine completes.
	dmm := DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMySQL, "srcdb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
	start := time.Now()
	h := MigrateDataAsync(dmm)
	elapsed := time.Since(start)

	if h == nil {
		t.Fatal("MigrateDataAsync returned nil handle")
	}
	// The handle must be returned well before any network timeout (30 s).
	if elapsed > 2*time.Second {
		t.Errorf("MigrateDataAsync blocked for %v, want < 2s", elapsed)
	}
}

func TestMigrateDataAsync_InitialStatus_IsPending(t *testing.T) {
	dmm := DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMySQL, "srcdb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
	h := MigrateDataAsync(dmm)
	// Status may still be Pending or have advanced; it must never be a zero value.
	s := h.Status()
	if s == "" {
		t.Errorf("Status() is empty immediately after MigrateDataAsync")
	}
	// Wait so the goroutine finishes and we don't leak it.
	h.Wait() //nolint:errcheck
}

func TestMigrateDataAsync_Cancel_PropagatesContext(t *testing.T) {
	// Use an invalid host so CheckTargetEmpty will fail; Cancel is sent first to
	// verify the context plumbing.
	dmm := DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMySQL, "srcdb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
	h := MigrateDataAsync(dmm)
	h.Cancel()

	// After cancel, the context must eventually be Done.
	select {
	case <-h.Context().Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("ctx.Done() not closed after Cancel()")
	}
	if h.Context().Err() != context.Canceled {
		t.Errorf("ctx.Err() = %v, want context.Canceled", h.Context().Err())
	}
	h.Wait() //nolint:errcheck — drain goroutine
}

func TestMigrateDataAsync_Wait_EventuallyReturns(t *testing.T) {
	// With a localhost:3306 that does not exist, CheckTargetEmpty or dial will
	// fail quickly. Wait() must unblock within the connection timeout.
	dmm := DBMSMigrationModel{
		Source: DBMSLocation{
			DBMSType:   DBMSTypeMySQL,
			Database:   "srcdb",
			AccessType: AccessTypeDirect,
			Direct:     &DirectConfig{Host: "127.0.0.2", Port: 19999, ConnectTimeout: 1},
		},
		Destination: DBMSLocation{
			DBMSType:   DBMSTypeMySQL,
			Database:   "dstdb",
			AccessType: AccessTypeDirect,
			Direct:     &DirectConfig{Host: "127.0.0.2", Port: 19999, ConnectTimeout: 1},
		},
		Scope: ScopeFull,
	}
	h := MigrateDataAsync(dmm)
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()

	select {
	case err := <-done:
		// We expect an error (connection refused / timeout).
		if err == nil {
			t.Error("Wait() should return an error for an unreachable host")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait() did not return within 10 s")
	}
}

func TestMigrateDataAsync_FinalStatus_IsNotPending(t *testing.T) {
	dmm := DBMSMigrationModel{
		Source: DBMSLocation{
			DBMSType:   DBMSTypeMySQL,
			Database:   "srcdb",
			AccessType: AccessTypeDirect,
			Direct:     &DirectConfig{Host: "127.0.0.2", Port: 19999, ConnectTimeout: 1},
		},
		Destination: DBMSLocation{
			DBMSType:   DBMSTypeMySQL,
			Database:   "dstdb",
			AccessType: AccessTypeDirect,
			Direct:     &DirectConfig{Host: "127.0.0.2", Port: 19999, ConnectTimeout: 1},
		},
		Scope: ScopeFull,
	}
	h := MigrateDataAsync(dmm)
	h.Wait() //nolint:errcheck

	s := h.Status()
	if s == StatusPending || s == StatusChecking || s == StatusDumping || s == StatusRestoring {
		t.Errorf("Status() = %q after Wait(), expected a terminal status", s)
	}
}

// =============================================================================
// MigrateData — validation path (no live database required)
// =============================================================================

func TestMigrateData_InvalidModel_ReturnsError(t *testing.T) {
	// Missing DBMSType on source → Validate inside Plan will reject it.
	err := MigrateData(DBMSMigrationModel{
		Source: DBMSLocation{
			Database:   "srcdb",
			AccessType: AccessTypeDirect,
			Direct:     &DirectConfig{Host: "localhost"},
		},
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	})
	if err == nil {
		t.Fatal("MigrateData should return error for invalid model")
	}
}

func TestMigrateData_UnsupportedDriver_ReturnsError(t *testing.T) {
	// MigrateData validates before resolving a driver, so an unsupported engine
	// is rejected by Validate — naming the offending side — rather than reaching
	// NewDBDriver and its *UnsupportedDBMSError. That error stays covered where
	// it is produced: driver.NewDBDriver and the executor constructors.
	err := MigrateData(DBMSMigrationModel{
		Source:      directLoc("oracle", "srcdb"),
		Destination: directLoc("oracle", "dstdb"),
		Scope:       ScopeFull,
	})
	if err == nil {
		t.Fatal("MigrateData should return error for unsupported DBMS type")
	}
	if !strings.Contains(err.Error(), "source.dbmsType") {
		t.Errorf("expected error naming source.dbmsType, got %T: %v", err, err)
	}
}

// =============================================================================
// Dump — validation path
// =============================================================================

func TestDump_MissingDBMSType_ReturnsError(t *testing.T) {
	loc := DBMSLocation{
		Database:   "mydb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	err := Dump(loc, ScopeFull, "/tmp/out.sql")
	if err == nil {
		t.Fatal("Dump should return error when DBMSType is empty")
	}
}

func TestDump_UnsupportedDBMSType_ReturnsError(t *testing.T) {
	// validateLocation rejects unknown DBMS types before NewDBDriver is called.
	loc := DBMSLocation{
		DBMSType:   "oracle",
		Database:   "mydb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	err := Dump(loc, ScopeFull, "/tmp/out.sql")
	if err == nil {
		t.Fatal("Dump should return error for unsupported DBMS type")
	}
}

// =============================================================================
// Restore — validation path
// =============================================================================

func TestRestore_MissingDBMSType_ReturnsError(t *testing.T) {
	loc := DBMSLocation{
		Database:   "mydb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	err := Restore(loc, "/tmp/in.sql")
	if err == nil {
		t.Fatal("Restore should return error when DBMSType is empty")
	}
}

func TestRestore_UnsupportedDBMSType_ReturnsError(t *testing.T) {
	// validateLocation rejects unknown DBMS types before NewDBDriver is called.
	loc := directLoc("oracle", "mydb")
	err := Restore(loc, "/tmp/in.sql")
	if err == nil {
		t.Fatal("Restore should return error for unsupported DBMS type")
	}
}

// =============================================================================
// Inspect — validation path
// =============================================================================

func TestInspect_MissingDBMSType_ReturnsError(t *testing.T) {
	loc := DBMSLocation{
		Database:   "mydb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	_, err := Inspect(loc)
	if err == nil {
		t.Fatal("Inspect should return error when DBMSType is empty")
	}
}

func TestInspect_UnsupportedDBMSType_ReturnsError(t *testing.T) {
	// validateLocation rejects unknown DBMS types before NewDBDriver is called.
	_, err := Inspect(directLoc("oracle", "mydb"))
	if err == nil {
		t.Fatal("Inspect should return error for unsupported DBMS type")
	}
}

// =============================================================================
// InspectAll — validation path
// =============================================================================

func TestInspectAll_MissingDBMSType_ReturnsError(t *testing.T) {
	loc := DBMSLocation{
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	_, _, err := InspectAll(loc)
	if err == nil {
		t.Fatal("InspectAll should return error when DBMSType is empty")
	}
}

func TestInspectAll_UnsupportedDBMSType_ReturnsError(t *testing.T) {
	// validateServerLocation rejects unknown DBMS types before NewDBDriver is called.
	loc := directLoc("oracle", "")
	_, _, err := InspectAll(loc)
	if err == nil {
		t.Fatal("InspectAll should return error for unsupported DBMS type")
	}
}

func TestInspectAll_MissingDirectConfig_ReturnsError(t *testing.T) {
	loc := DBMSLocation{DBMSType: DBMSTypeMySQL, AccessType: AccessTypeDirect}
	_, _, err := InspectAll(loc)
	if err == nil {
		t.Fatal("InspectAll should return error when direct config is missing")
	}
}

func TestInspectAll_EmptyDatabase_PassesValidation(t *testing.T) {
	// InspectAll is a server-level call: an empty loc.Database must not be what
	// stops it. The location below is otherwise complete, so validation has to
	// let it through and the failure has to come from the unreachable host.
	loc := unreachableMySQL("")
	_, _, err := InspectAll(loc)
	if err == nil {
		t.Fatal("InspectAll should fail against an unreachable host")
	}
	if strings.Contains(err.Error(), "database is required") {
		t.Errorf("InspectAll must not require loc.Database, got: %v", err)
	}
}

// =============================================================================
// DescribeTable — validation path
// =============================================================================

func TestDescribeTable_MissingDBMSType_ReturnsError(t *testing.T) {
	loc := DBMSLocation{
		Database:   "mydb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost"},
	}
	_, err := DescribeTable(loc, "users")
	if err == nil {
		t.Fatal("DescribeTable should return error when DBMSType is empty")
	}
}

func TestDescribeTable_UnsupportedDBMSType_ReturnsError(t *testing.T) {
	// validateLocation rejects unknown DBMS types before NewDBDriver is called.
	_, err := DescribeTable(directLoc("oracle", "mydb"), "users")
	if err == nil {
		t.Fatal("DescribeTable should return error for unsupported DBMS type")
	}
}

// =============================================================================
// ListDatabases / CreateDatabase / DropDatabase — up-front validation
// =============================================================================

// The locations below point at a host nothing listens on. Every case here is
// expected to be refused before a connection is attempted, so a test that hangs
// or reports a dial error is reporting a real regression: a guard that stopped
// running up front.

func unreachableMySQL(database string) DBMSLocation {
	return DBMSLocation{
		DBMSType:   DBMSTypeMySQL,
		Database:   database,
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "127.0.0.1", Port: 1, ConnectTimeout: 1},
	}
}

// =============================================================================
// validateDatabaseLocation
// =============================================================================

func TestValidateDatabaseLocation_RequiresDatabase(t *testing.T) {
	// validateServerLocation deliberately skips Database, since ListDatabases has
	// no target; a create or a drop does, so this wrapper adds the check.
	loc := unreachableMySQL("")
	err := validateDatabaseLocation("target", loc)
	if err == nil {
		t.Fatal("empty database = nil, want an error")
	}
	if !strings.Contains(err.Error(), "target.database is required") {
		t.Errorf("error %q should name the missing field", err)
	}
}

func TestValidateDatabaseLocation_ChecksTheName(t *testing.T) {
	err := validateDatabaseLocation("target", unreachableMySQL("app`db"))
	var nameErr *InvalidDatabaseNameError
	if !errors.As(err, &nameErr) {
		t.Fatalf("got %v (%T), want *InvalidDatabaseNameError", err, err)
	}
}

func TestValidateDatabaseLocation_StillChecksAccessConfig(t *testing.T) {
	loc := unreachableMySQL("app")
	loc.Direct = nil
	if err := validateDatabaseLocation("target", loc); err == nil {
		t.Fatal("missing direct config = nil, want an error")
	}
}

func TestValidateDatabaseLocation_AcceptsValidLocation(t *testing.T) {
	if err := validateDatabaseLocation("target", unreachableMySQL("app")); err != nil {
		t.Fatalf("valid location = %v, want nil", err)
	}
}

// =============================================================================
// Guards applied before connecting
// =============================================================================

func TestCreateDatabase_RefusesSystemDatabase(t *testing.T) {
	for _, tc := range []struct {
		dbmsType string
		database string
	}{
		{DBMSTypeMySQL, "information_schema"},
		{DBMSTypeMySQL, "mysql"},
		{DBMSTypeMariaDB, "performance_schema"},
		{DBMSTypePostgreSQL, "template1"},
		{DBMSTypeMongoDB, "admin"},
	} {
		t.Run(tc.dbmsType+"/"+tc.database, func(t *testing.T) {
			loc := unreachableMySQL(tc.database)
			loc.DBMSType = tc.dbmsType

			err := CreateDatabase(loc, nil)
			var sysErr *SystemDatabaseError
			if !errors.As(err, &sysErr) {
				t.Fatalf("got %v (%T), want *SystemDatabaseError", err, err)
			}
			if sysErr.Operation != "create" {
				t.Errorf("Operation = %q, want \"create\"", sysErr.Operation)
			}
		})
	}
}

func TestDropDatabase_RefusesSystemDatabase(t *testing.T) {
	loc := unreachableMySQL("mysql")

	err := DropDatabase(loc, nil)
	var sysErr *SystemDatabaseError
	if !errors.As(err, &sysErr) {
		t.Fatalf("got %v (%T), want *SystemDatabaseError", err, err)
	}
	if sysErr.Operation != "drop" {
		t.Errorf("Operation = %q, want \"drop\"", sysErr.Operation)
	}
}

func TestCreateDatabase_RejectsBadNameBeforeConnecting(t *testing.T) {
	err := CreateDatabase(unreachableMySQL("app`db"), nil)
	var nameErr *InvalidDatabaseNameError
	if !errors.As(err, &nameErr) {
		t.Fatalf("got %v (%T), want *InvalidDatabaseNameError", err, err)
	}
}

func TestDropDatabase_RejectsEmptyNameBeforeConnecting(t *testing.T) {
	// Without this guard an empty name would reach the engine as "DROP DATABASE
	// ``", which is a syntax error at best and ambiguous at worst.
	err := DropDatabase(unreachableMySQL(""), nil)
	if err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("got %v, want a missing-database error", err)
	}
}

func TestCreateDatabase_RejectsBadOptionBeforeConnecting(t *testing.T) {
	loc := unreachableMySQL("app")
	loc.DBMSType = DBMSTypePostgreSQL

	// Encoding without a template is refused by the server; the option check
	// reports it without spending a connection.
	err := CreateDatabase(loc, &CreateDatabaseOption{
		PostgreSQL: &PostgreSQLCreateOption{Encoding: "UTF8"},
	})
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("got %v, want an error naming the missing template", err)
	}
}

func TestCreateDatabase_RejectsUnsupportedEngine(t *testing.T) {
	loc := unreachableMySQL("app")
	loc.DBMSType = "cassandra"

	if err := CreateDatabase(loc, nil); err == nil {
		t.Fatal("unsupported engine = nil, want an error")
	}
	if err := DropDatabase(loc, nil); err == nil {
		t.Fatal("unsupported engine = nil, want an error")
	}
}
