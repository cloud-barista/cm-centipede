package executor

import (
	"context"
	"errors"
	"os"
	"testing"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Unit tests (no DBMS server required)
// =============================================================================

func TestNewRestoreExecutor_InvalidType(t *testing.T) {
	_, err := NewRestoreExecutor(drvpkg.DBMSLocation{DBMSType: "unsupported", Database: "db"}, drvpkg.ScopeFull, "/tmp/dump.sql")
	if err == nil {
		t.Fatal("expected error for unsupported DBMS type, got nil")
	}
	var unsup *drvpkg.UnsupportedDBMSError
	if !errors.As(err, &unsup) {
		t.Errorf("expected *UnsupportedDBMSError, got %T: %v", err, err)
	}
}

func TestNewRestoreExecutor_ValidType(t *testing.T) {
	exec, err := NewRestoreExecutor(drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "testdb"}, drvpkg.ScopeFull, "/tmp/dump.sql")
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}
	if exec == nil {
		t.Fatal("expected non-nil RestoreExecutor")
	}
}

// TestRestoreExecutor_Execute_WrapsError verifies that a Restore failure is
// wrapped in *MigrationError{Stage: StageRestore}.  We trigger the failure by
// pointing at a staging file that does not exist (no prior Dump), which causes
// the direct-mode driver to fail when it tries to read the file.
func TestRestoreExecutor_Execute_WrapsError(t *testing.T) {
	dst := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "dest",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306, Username: "root", Password: "pass"},
	}
	exec, err := NewRestoreExecutor(dst, drvpkg.ScopeFull, "/tmp/dump.sql")
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}

	// Source references a database whose staging file will not exist.
	src := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "nonexistent-db-no-staging-file",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306},
	}
	execErr := exec.Execute(context.Background(), src, dst)
	if execErr == nil {
		t.Fatal("expected Execute to fail for missing staging file, got nil")
	}
	var migErr *MigrationError
	if !errors.As(execErr, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", execErr, execErr)
	}
	if migErr.Stage != StageRestore {
		t.Errorf("MigrationError.Stage = %q, want %q", migErr.Stage, StageRestore)
	}
}

func TestRestoreExecutor_Execute_MigrationErrorUnwrap(t *testing.T) {
	dst := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "dest",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306},
	}
	exec, err := NewRestoreExecutor(dst, drvpkg.ScopeFull, "/tmp/dump.sql")
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}

	src := drvpkg.DBMSLocation{
		DBMSType: drvpkg.DBMSTypeMySQL,
		Database: "nonexistent-db-no-staging-file",
	}
	execErr := exec.Execute(context.Background(), src, dst)
	if execErr == nil {
		t.Fatal("expected Execute to fail")
	}

	var migErr *MigrationError
	if errors.As(execErr, &migErr) {
		if migErr.Unwrap() == nil {
			t.Error("MigrationError.Unwrap() should return non-nil inner error")
		}
	}
}

// The dump and restore sides agree on one file because the path travels from the
// one that created it. They used to agree by each applying the same formula to
// the same source, which is what made every dump of a given database and scope
// land on one shared path.
func TestRestoreExecutor_ReadsThePairedDumpFile(t *testing.T) {
	src := drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "srcdb"}
	dst := drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "dstdb"}

	dumpExec, err := NewDumpExecutor(src, drvpkg.ScopeSchemaOnly, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer dumpExec.Cleanup()

	restoreExec, err := NewRestoreExecutor(dst, drvpkg.ScopeSchemaOnly, dumpExec.OutputPath())
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}
	if got := restoreExec.inputPath; got != dumpExec.OutputPath() {
		t.Errorf("restore reads %q, dump writes %q", got, dumpExec.OutputPath())
	}
}

// Without a path there is no dump to read, and the formula that used to invent
// one is gone — so say so at construction rather than failing on a missing file
// after the dump has already run.
func TestNewRestoreExecutor_RequiresInputPath(t *testing.T) {
	dst := drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "dstdb"}
	if _, err := NewRestoreExecutor(dst, drvpkg.ScopeFull, ""); err == nil {
		t.Error("NewRestoreExecutor accepted an empty inputPath")
	}
}

// =============================================================================
// Integration tests (require MYSQL_TEST_DSN)
// =============================================================================
//
// To run:
//
//	docker run -d --name mysql-test \
//	  -e MYSQL_ROOT_PASSWORD=pass -e MYSQL_DATABASE=testdb \
//	  -p 3306:3306 mysql:8
//
//	export MYSQL_TEST_DSN="root:pass@tcp(localhost:3306)/testdb"
//	go test ./... -run TestRestoreExecutorIntegration -v

func TestRestoreExecutorIntegration_Execute(t *testing.T) {
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Skip("MYSQL_TEST_DSN not set; skipping RestoreExecutor integration test")
	}

	src := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306, Username: "root", Password: "pass"},
	}
	dst := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "testdb_restore",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306, Username: "root", Password: "pass"},
	}

	drv := drvpkg.NewMySQLDriver()
	if err := drv.TestConnection(context.Background(), src); err != nil {
		t.Skipf("MySQL not reachable: %v", err)
	}

	// Dump first.
	dumpExec, err := NewDumpExecutor(src, drvpkg.ScopeSchemaOnly, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	if err := dumpExec.Execute(context.Background(), src, drvpkg.DBMSLocation{}); err != nil {
		t.Fatalf("DumpExecutor.Execute: %v", err)
	}
	defer dumpExec.Cleanup()

	// Restore into destination.
	restoreExec, err := NewRestoreExecutor(dst, drvpkg.ScopeSchemaOnly, dumpExec.OutputPath())
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}
	if err := restoreExec.Execute(context.Background(), src, dst); err != nil {
		t.Fatalf("RestoreExecutor.Execute: %v", err)
	}
	t.Log("restore completed successfully")
}

func TestRestoreExecutorIntegration_Execute_MigrationError(t *testing.T) {
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Skip("MYSQL_TEST_DSN not set; skipping RestoreExecutor integration test")
	}

	dst := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "ghost_dst",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "192.0.2.1", Port: 3306},
	}
	exec, err := NewRestoreExecutor(dst, drvpkg.ScopeFull, "/tmp/dump.sql")
	if err != nil {
		t.Fatalf("NewRestoreExecutor: %v", err)
	}

	src := drvpkg.DBMSLocation{
		DBMSType: drvpkg.DBMSTypeMySQL,
		Database: "nonexistent-db-no-staging-file",
	}
	execErr := exec.Execute(context.Background(), src, dst)
	if execErr == nil {
		t.Fatal("expected Execute to fail")
	}
	var migErr *MigrationError
	if !errors.As(execErr, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", execErr, execErr)
	}
	if migErr.Stage != StageRestore {
		t.Errorf("Stage = %q, want %q", migErr.Stage, StageRestore)
	}
}
