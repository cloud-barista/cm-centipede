package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloud-barista/cm-centipede/transx-ex/core/sshtest"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	"github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/mysql"
)

func TestNewDBDriver_Unsupported(t *testing.T) {
	_, err := NewDBDriver("oracle")
	if err == nil {
		t.Fatal("expected error for unsupported DBMS type")
	}
	var unsupported *UnsupportedDBMSError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedDBMSError, got %T: %v", err, err)
	}
	if unsupported.DBMSType != "oracle" {
		t.Errorf("DBMSType = %q, want %q", unsupported.DBMSType, "oracle")
	}
}

func TestNewDBDriver_EmptyType(t *testing.T) {
	_, err := NewDBDriver("")
	if err == nil {
		t.Fatal("expected error for empty DBMS type")
	}
	var unsupported *UnsupportedDBMSError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *UnsupportedDBMSError, got %T", err)
	}
}

func TestNewDBDriver_KnownTypes(t *testing.T) {
	known := []string{
		DBMSTypeMySQL,
		DBMSTypeMariaDB,
		DBMSTypePostgreSQL,
		DBMSTypeMongoDB,
	}
	for _, dbmsType := range known {
		drv, err := NewDBDriver(dbmsType)
		if err != nil {
			t.Errorf("NewDBDriver(%q) unexpected error: %v", dbmsType, err)
			continue
		}
		if drv == nil {
			t.Errorf("NewDBDriver(%q) returned nil driver", dbmsType)
		}
	}
}

func TestNewDBDriver_MySQL_ConcreteType(t *testing.T) {
	drv, err := NewDBDriver(DBMSTypeMySQL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := drv.(*mysql.MySQLDriver); !ok {
		t.Errorf("expected *MySQLDriver, got %T", drv)
	}
}

// =============================================================================
// SSH connection reuse across drivers
// =============================================================================
//
// Inspect issues several queries even before any per-table metric is requested
// (server version, database size, table list). Each driver must dial once for
// the whole operation rather than once per query; these tests pin that down at
// the driver level, where the wiring lives.
//
// The stub server echoes commands rather than emulating a DBMS, so Inspect
// returns junk or an error. That is irrelevant here: the assertion is on the
// number of handshakes, which is counted regardless of the outcome.

func sshLoc(t *testing.T, dbmsType string) (base.DBMSLocation, *sshtest.Server) {
	t.Helper()
	srv, err := sshtest.New()
	if err != nil {
		t.Fatalf("start SSH test server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	return base.DBMSLocation{
		DBMSType:   dbmsType,
		Database:   "app",
		AccessType: base.AccessTypeSSHTunnel,
		SSHTunnel: &base.SSHTunnelConfig{
			SSH: &base.SSHConfig{
				Host:           srv.Host(),
				Port:           srv.Port(),
				Username:       "tester",
				ConnectTimeout: 5,
			},
			DBHost:   "localhost",
			Username: "admin",
		},
	}, srv
}

func TestInspectViaSSH_DialsOncePerOperation(t *testing.T) {
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
		base.DBMSTypeMongoDB,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)

			drv, err := NewDBDriver(dbmsType)
			if err != nil {
				t.Fatalf("NewDBDriver: %v", err)
			}

			// The result is meaningless against an echo server; only the
			// handshake count matters.
			_, _ = drv.Inspect(context.Background(), loc)

			if got := srv.Connections(); got != 1 {
				t.Errorf("connections = %d, want 1 — Inspect must reuse one connection", got)
			}
			if got := srv.Execs(); got < 2 {
				t.Errorf("execs = %d, want at least 2 queries on that single connection", got)
			}
		})
	}
}

func TestDescribeTableViaSSH_DialsOncePerOperation(t *testing.T) {
	// The relational drivers query columns and indexes separately, so a
	// per-query dial would show up as two handshakes.
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)

			// PostgreSQL parses its result sets as JSON, and an echoed command
			// is not valid JSON: without a well-formed reply the column query
			// errors out and the index query is never reached, which would
			// hide the very behavior under test.
			srv.Responder = func(string) string { return "[]" }

			drv, err := NewDBDriver(dbmsType)
			if err != nil {
				t.Fatalf("NewDBDriver: %v", err)
			}

			_, _ = drv.DescribeTable(context.Background(), loc, "users")

			if got := srv.Connections(); got != 1 {
				t.Errorf("connections = %d, want 1", got)
			}
			if got := srv.Execs(); got != 2 {
				t.Errorf("execs = %d, want 2 (columns, indexes) on one connection", got)
			}
		})
	}
}

func TestDescribeCollectionViaSSH_UsesSingleRoundTrip(t *testing.T) {
	// MongoDB bundles count, indexes and field inference into one mongosh
	// invocation, so a collection costs exactly one exec.
	loc, srv := sshLoc(t, base.DBMSTypeMongoDB)

	drv, err := NewDBDriver(base.DBMSTypeMongoDB)
	if err != nil {
		t.Fatalf("NewDBDriver: %v", err)
	}

	_, _ = drv.DescribeTable(context.Background(), loc, "users")

	if got := srv.Connections(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
	if got := srv.Execs(); got != 1 {
		t.Errorf("execs = %d, want 1 bundled mongosh call", got)
	}
}

// =============================================================================
// CreateDatabase / DropDatabase over SSH
// =============================================================================
//
// Both calls issue two commands — an existence check, then the DDL — and must
// dial once for the pair rather than once per command. These tests pin that
// down, and they pin down which of the two branches the existence answer takes:
// the stub server's reply is chosen so the driver sees the database as absent
// (create) or present (drop), which is the only way the second command is
// reached at all.

// absentReply makes each engine's existence check report "no such database".
func absentReply(dbmsType string) string {
	if dbmsType == base.DBMSTypeMongoDB {
		// mongosh answers as sentinel-prefixed JSON: an empty name list.
		return "DBMSX_JSON:[]"
	}
	// A CLI that prints no rows means no match, for both mysql and psql.
	return ""
}

// presentReply makes each engine's existence check report a match.
func presentReply(dbmsType string) string {
	switch dbmsType {
	case base.DBMSTypeMongoDB:
		return `DBMSX_JSON:["app"]`
	case base.DBMSTypePostgreSQL:
		// SELECT EXISTS(…) comes back as psql's boolean "t".
		return "t"
	default:
		return "app"
	}
}

// createOptFor returns an option that makes the create actually issue DDL.
func createOptFor(dbmsType string) *base.CreateDatabaseOption {
	if dbmsType == base.DBMSTypeMongoDB {
		// MongoDB has no CREATE DATABASE: without a collection to create the
		// call is a deliberate no-op and never reaches a second command.
		return &base.CreateDatabaseOption{MongoDB: &base.MongoDBCreateOption{Collection: "seed"}}
	}
	return nil
}

func TestCreateDatabaseViaSSH_DialsOncePerOperation(t *testing.T) {
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
		base.DBMSTypeMongoDB,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)
			srv.Responder = func(string) string { return absentReply(dbmsType) }

			drv, err := NewDBDriver(dbmsType)
			if err != nil {
				t.Fatalf("NewDBDriver: %v", err)
			}

			if err := drv.CreateDatabase(context.Background(), loc, createOptFor(dbmsType)); err != nil {
				t.Fatalf("CreateDatabase: %v", err)
			}

			if got := srv.Connections(); got != 1 {
				t.Errorf("connections = %d, want 1 — the check and the DDL share one connection", got)
			}
			if got := srv.Execs(); got != 2 {
				t.Errorf("execs = %d, want 2 (existence check, then DDL)", got)
			}
		})
	}
}

func TestDropDatabaseViaSSH_DialsOncePerOperation(t *testing.T) {
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
		base.DBMSTypeMongoDB,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)
			srv.Responder = func(string) string { return presentReply(dbmsType) }

			drv, err := NewDBDriver(dbmsType)
			if err != nil {
				t.Fatalf("NewDBDriver: %v", err)
			}

			if err := drv.DropDatabase(context.Background(), loc, nil); err != nil {
				t.Fatalf("DropDatabase: %v", err)
			}

			if got := srv.Connections(); got != 1 {
				t.Errorf("connections = %d, want 1 — the check and the DDL share one connection", got)
			}
			if got := srv.Execs(); got != 2 {
				t.Errorf("execs = %d, want 2 (existence check, then DROP)", got)
			}
		})
	}
}

// =============================================================================
// Existence outcomes
// =============================================================================

func TestCreateDatabaseViaSSH_ExistingDatabase(t *testing.T) {
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
		base.DBMSTypeMongoDB,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)
			srv.Responder = func(string) string { return presentReply(dbmsType) }

			drv, _ := NewDBDriver(dbmsType)

			err := drv.CreateDatabase(context.Background(), loc, createOptFor(dbmsType))
			var exists *base.TargetDatabaseExistsError
			if !errors.As(err, &exists) {
				t.Fatalf("CreateDatabase = %v (%T), want *TargetDatabaseExistsError", err, err)
			}
			// No DDL may be issued once the database is known to be there.
			if got := srv.Execs(); got != 1 {
				t.Errorf("execs = %d, want 1 — only the existence check should run", got)
			}

			// IfNotExists turns the same state into a no-op success.
			opt := createOptFor(dbmsType)
			if opt == nil {
				opt = &base.CreateDatabaseOption{}
			}
			opt.IfNotExists = true
			if err := drv.CreateDatabase(context.Background(), loc, opt); err != nil {
				t.Errorf("CreateDatabase with IfNotExists = %v, want nil", err)
			}
		})
	}
}

func TestDropDatabaseViaSSH_MissingDatabase(t *testing.T) {
	for _, dbmsType := range []string{
		base.DBMSTypeMySQL,
		base.DBMSTypeMariaDB,
		base.DBMSTypePostgreSQL,
		base.DBMSTypeMongoDB,
	} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)
			srv.Responder = func(string) string { return absentReply(dbmsType) }

			drv, _ := NewDBDriver(dbmsType)

			err := drv.DropDatabase(context.Background(), loc, nil)
			var missing *base.TargetDatabaseNotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("DropDatabase = %v (%T), want *TargetDatabaseNotFoundError", err, err)
			}
			// No DROP may be issued for a database that is not there.
			if got := srv.Execs(); got != 1 {
				t.Errorf("execs = %d, want 1 — only the existence check should run", got)
			}

			if err := drv.DropDatabase(context.Background(), loc,
				&base.DropDatabaseOption{IfExists: true}); err != nil {
				t.Errorf("DropDatabase with IfExists = %v, want nil", err)
			}
		})
	}
}

func TestCreateDatabaseViaSSH_MongoDBWithoutCollectionIsNoOp(t *testing.T) {
	// MongoDB materialises a database only through a collection. Asked to create
	// one with no collection named, the driver reports success and issues nothing
	// beyond the existence check.
	loc, srv := sshLoc(t, base.DBMSTypeMongoDB)
	srv.Responder = func(string) string { return absentReply(base.DBMSTypeMongoDB) }

	drv, _ := NewDBDriver(base.DBMSTypeMongoDB)

	if err := drv.CreateDatabase(context.Background(), loc, nil); err != nil {
		t.Fatalf("CreateDatabase = %v, want nil", err)
	}
	if got := srv.Execs(); got != 1 {
		t.Errorf("execs = %d, want 1 — nothing should be created without a collection", got)
	}
}

// =============================================================================
// Server-level entry point
// =============================================================================
//
// The statements themselves travel on stdin, so the recorded command is the CLI
// invocation. What that invocation does reveal is the database the driver
// connected to — and both calls must avoid the database they are creating or
// dropping: it does not exist yet, or is about to stop existing.

func TestDatabaseDDLViaSSH_DoesNotSelectTargetDatabase(t *testing.T) {
	for _, dbmsType := range []string{base.DBMSTypeMySQL, base.DBMSTypeMariaDB} {
		t.Run(dbmsType, func(t *testing.T) {
			loc, srv := sshLoc(t, dbmsType)
			srv.Responder = func(string) string { return presentReply(dbmsType) }

			drv, _ := NewDBDriver(dbmsType)
			if err := drv.DropDatabase(context.Background(), loc, nil); err != nil {
				t.Fatalf("DropDatabase: %v", err)
			}

			cmds := srv.Commands()
			if len(cmds) != 2 {
				t.Fatalf("commands = %d, want 2", len(cmds))
			}
			// buildMySQLCmd appends the database name last when one is selected.
			for _, cmd := range cmds {
				if strings.HasSuffix(cmd, " "+loc.Database) {
					t.Errorf("command %q selects the target database; it must connect at server level", cmd)
				}
			}
		})
	}
}

func TestDatabaseDDLViaSSH_PostgreSQLUsesMaintenanceDatabase(t *testing.T) {
	// PostgreSQL refuses to drop the database the session is connected to, and a
	// database being created cannot be connected to at all, so both calls enter
	// through "postgres".
	for _, tc := range []struct {
		label string
		run   func(base.DBDriver, base.DBMSLocation) error
		reply string
	}{
		{
			label: "create",
			run: func(d base.DBDriver, loc base.DBMSLocation) error {
				return d.CreateDatabase(context.Background(), loc, nil)
			},
			reply: absentReply(base.DBMSTypePostgreSQL),
		},
		{
			label: "drop",
			run: func(d base.DBDriver, loc base.DBMSLocation) error {
				return d.DropDatabase(context.Background(), loc, nil)
			},
			reply: presentReply(base.DBMSTypePostgreSQL),
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			loc, srv := sshLoc(t, base.DBMSTypePostgreSQL)
			srv.Responder = func(string) string { return tc.reply }

			drv, _ := NewDBDriver(base.DBMSTypePostgreSQL)
			if err := tc.run(drv, loc); err != nil {
				t.Fatalf("%s: %v", tc.label, err)
			}

			cmds := srv.Commands()
			if len(cmds) != 2 {
				t.Fatalf("commands = %d, want 2", len(cmds))
			}
			for _, cmd := range cmds {
				if !strings.Contains(cmd, "-d postgres") {
					t.Errorf("command %q must connect through the postgres maintenance database", cmd)
				}
			}
		})
	}
}

func TestDatabaseDDLViaSSH_MongoDBChecksThroughAdmin(t *testing.T) {
	// listDatabases is an admin command, so the existence check enters through
	// "admin"; the drop itself has to run against the target database.
	loc, srv := sshLoc(t, base.DBMSTypeMongoDB)
	srv.Responder = func(string) string { return presentReply(base.DBMSTypeMongoDB) }

	drv, _ := NewDBDriver(base.DBMSTypeMongoDB)
	if err := drv.DropDatabase(context.Background(), loc, nil); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}

	cmds := srv.Commands()
	if len(cmds) != 2 {
		t.Fatalf("commands = %d, want 2", len(cmds))
	}
	if !strings.Contains(cmds[0], "/admin") {
		t.Errorf("existence check %q must go through the admin database", cmds[0])
	}
	if !strings.Contains(cmds[1], "/"+loc.Database) {
		t.Errorf("dropDatabase %q must run against the target database", cmds[1])
	}
}

func TestPostgreSQLDropForce_TerminatesBackendsFirst(t *testing.T) {
	// A PostgreSQL database with live sessions cannot be dropped, so Force adds a
	// pg_terminate_backend pass before the DROP — a third command on the same
	// connection.
	loc, srv := sshLoc(t, base.DBMSTypePostgreSQL)
	srv.Responder = func(string) string { return presentReply(base.DBMSTypePostgreSQL) }

	drv, _ := NewDBDriver(base.DBMSTypePostgreSQL)
	err := drv.DropDatabase(context.Background(), loc, &base.DropDatabaseOption{
		PostgreSQL: &base.PostgreSQLDropOption{Force: true},
	})
	if err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}

	if got := srv.Connections(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
	if got := srv.Execs(); got != 3 {
		t.Errorf("execs = %d, want 3 (existence check, terminate backends, DROP)", got)
	}
}
