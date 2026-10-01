package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Unit tests (no DBMS server required)
// =============================================================================

func TestNewDumpExecutor_InvalidType(t *testing.T) {
	_, err := NewDumpExecutor(drvpkg.DBMSLocation{DBMSType: "unsupported", Database: "db"}, drvpkg.ScopeFull, nil)
	if err == nil {
		t.Fatal("expected error for unsupported DBMS type, got nil")
	}
	var unsup *drvpkg.UnsupportedDBMSError
	if !errors.As(err, &unsup) {
		t.Errorf("expected *UnsupportedDBMSError, got %T: %v", err, err)
	}
}

func TestNewDumpExecutor_ValidType(t *testing.T) {
	exec, err := NewDumpExecutor(drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "testdb"}, drvpkg.ScopeFull, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()
	if exec == nil {
		t.Fatal("expected non-nil DumpExecutor")
	}
	// Staging directory must exist after construction.
	if _, statErr := os.Stat(drvpkg.DefaultStagingPath); statErr != nil {
		t.Errorf("staging directory %q should exist: %v", drvpkg.DefaultStagingPath, statErr)
	}
}

// stagingFile is the common shape of the per-engine cases below: create one,
// register its removal, hand back the path.
func stagingFile(t *testing.T, dbmsType, database, scope string) string {
	t.Helper()
	p, cleanup, err := NewStagingFile(dbmsType, database, scope)
	if err != nil {
		t.Fatalf("NewStagingFile(%s, %s, %s): %v", dbmsType, database, scope, err)
	}
	t.Cleanup(cleanup)
	return p
}

func TestNewStagingFile_MongoDB(t *testing.T) {
	p := stagingFile(t, drvpkg.DBMSTypeMongoDB, "mydb", drvpkg.ScopeFull)
	if !strings.HasSuffix(p, ".archive") {
		t.Errorf("MongoDB staging path should end with .archive, got %q", p)
	}
	if !strings.Contains(filepath.Base(p), "mydb") {
		t.Errorf("staging path should contain database name, got %q", p)
	}
}

func TestNewStagingFile_MySQL(t *testing.T) {
	p := stagingFile(t, drvpkg.DBMSTypeMySQL, "mydb", drvpkg.ScopeSchemaOnly)
	if !strings.HasSuffix(p, ".sql") {
		t.Errorf("MySQL staging path should end with .sql, got %q", p)
	}
}

func TestNewStagingFile_PostgreSQL(t *testing.T) {
	p := stagingFile(t, drvpkg.DBMSTypePostgreSQL, "pgdb", drvpkg.ScopeDataOnly)
	if !strings.HasSuffix(p, ".sql") {
		t.Errorf("PostgreSQL staging path should end with .sql, got %q", p)
	}
}

func TestNewStagingFile_MariaDB(t *testing.T) {
	p := stagingFile(t, drvpkg.DBMSTypeMariaDB, "mariadb", drvpkg.ScopeFull)
	if !strings.HasSuffix(p, ".sql") {
		t.Errorf("MariaDB staging path should end with .sql, got %q", p)
	}
}

func TestNewStagingFile_ScopeInName(t *testing.T) {
	full := filepath.Base(stagingFile(t, drvpkg.DBMSTypeMySQL, "db", drvpkg.ScopeFull))
	schema := filepath.Base(stagingFile(t, drvpkg.DBMSTypeMySQL, "db", drvpkg.ScopeSchemaOnly))
	if !strings.Contains(full, drvpkg.ScopeFull) {
		t.Errorf("staging name %q does not carry the scope", full)
	}
	if !strings.Contains(schema, drvpkg.ScopeSchemaOnly) {
		t.Errorf("staging name %q does not carry the scope", schema)
	}
}

// The property the deterministic name could not give: two migrations of the same
// database at the same scope used to share one file, and the second dump would
// overwrite the first while the first was still being restored.
func TestNewStagingFile_IsUniquePerCall(t *testing.T) {
	first := stagingFile(t, drvpkg.DBMSTypeMySQL, "mydb", drvpkg.ScopeFull)
	second := stagingFile(t, drvpkg.DBMSTypeMySQL, "mydb", drvpkg.ScopeFull)

	if first == second {
		t.Fatalf("two dumps of the same database and scope share %q; one would overwrite the other", first)
	}
	for _, p := range []string{first, second} {
		if filepath.Dir(p) != drvpkg.DefaultStagingPath {
			t.Errorf("staging file %q is not under %q", p, drvpkg.DefaultStagingPath)
		}
	}
}

// A dump is the source database in full, in plain text. The file is created here
// rather than by the driver so that it is private from the moment it exists —
// os.Create and a shell redirect would both leave it world-readable.
func TestNewStagingFile_CreatesAnEmptyPrivateFile(t *testing.T) {
	p := stagingFile(t, drvpkg.DBMSTypeMySQL, "mydb", drvpkg.ScopeFull)

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("staging file was not created: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("staging path %q is a directory", p)
	}
	if info.Size() != 0 {
		t.Errorf("staging file starts with %d bytes, want 0", info.Size())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("staging file mode = %04o, want 0600 — a dump must not be readable by others", perm)
	}
}

func TestNewStagingFile_CleanupRemovesIt(t *testing.T) {
	p, cleanup, err := NewStagingFile(drvpkg.DBMSTypeMySQL, "mydb", drvpkg.ScopeFull)
	if err != nil {
		t.Fatalf("NewStagingFile: %v", err)
	}
	if err := os.WriteFile(p, []byte("-- dump"), 0o600); err != nil {
		t.Fatalf("write staging file: %v", err)
	}

	cleanup()

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("staging file %q survived cleanup (stat error: %v)", p, err)
	}
	// Cleanup owns its file, not the directory other migrations are using.
	if _, err := os.Stat(drvpkg.DefaultStagingPath); err != nil {
		t.Errorf("cleanup removed the staging directory %q: %v", drvpkg.DefaultStagingPath, err)
	}
}

func TestDumpExecutor_OutputPath(t *testing.T) {
	loc := drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "mydb"}

	exec, err := NewDumpExecutor(loc, drvpkg.ScopeFull, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()

	got := exec.OutputPath()
	if got == "" {
		t.Fatal("OutputPath() is empty")
	}
	if filepath.Dir(got) != drvpkg.DefaultStagingPath {
		t.Errorf("OutputPath() = %q, want a file under %q", got, drvpkg.DefaultStagingPath)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("OutputPath() %q does not exist: %v", got, err)
	}

	// Each executor owns a file of its own — this is what the RestoreExecutor is
	// handed, and what makes two concurrent migrations independent.
	other, err := NewDumpExecutor(loc, drvpkg.ScopeFull, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer other.Cleanup()
	if other.OutputPath() == got {
		t.Errorf("two dump executors share the staging file %q", got)
	}
}

func TestDumpExecutor_CleanupRemovesStagingFile(t *testing.T) {
	exec, err := NewDumpExecutor(
		drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "mydb"},
		drvpkg.ScopeFull, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	path := exec.OutputPath()

	exec.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("staging file %q survived Cleanup (stat error: %v)", path, err)
	}
	// Called again after the file is gone — the pipeline's Cleanup and a
	// caller's own defer can both reach it.
	exec.Cleanup()
}

func TestDumpExecutor_Execute_WrapsError(t *testing.T) {
	// Use an SSH-tunnel source that will fail immediately (connection refused).
	exec, err := NewDumpExecutor(
		drvpkg.DBMSLocation{
			DBMSType:   drvpkg.DBMSTypeMySQL,
			Database:   "test",
			AccessType: drvpkg.AccessTypeSSHTunnel,
			SSHTunnel: &drvpkg.SSHTunnelConfig{
				SSH:    &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"},
				DBHost: "127.0.0.1",
				DBPort: 3306,
			},
		},
		drvpkg.ScopeFull,
		nil,
	)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()

	badSrc := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "test",
		AccessType: drvpkg.AccessTypeSSHTunnel,
		SSHTunnel: &drvpkg.SSHTunnelConfig{
			SSH:    &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"},
			DBHost: "127.0.0.1",
			DBPort: 3306,
		},
	}
	execErr := exec.Execute(context.Background(), badSrc, drvpkg.DBMSLocation{})
	if execErr == nil {
		t.Fatal("expected Execute to fail, got nil")
	}
	var migErr *MigrationError
	if !errors.As(execErr, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", execErr, execErr)
	}
	if migErr.Stage != StageDump {
		t.Errorf("MigrationError.Stage = %q, want %q", migErr.Stage, StageDump)
	}
}

func TestDumpExecutor_Execute_MigrationErrorUnwrap(t *testing.T) {
	exec, err := NewDumpExecutor(
		drvpkg.DBMSLocation{
			DBMSType:   drvpkg.DBMSTypeMySQL,
			Database:   "test",
			AccessType: drvpkg.AccessTypeSSHTunnel,
			SSHTunnel: &drvpkg.SSHTunnelConfig{
				SSH:    &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"},
				DBHost: "127.0.0.1",
				DBPort: 3306,
			},
		},
		drvpkg.ScopeFull,
		nil,
	)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()

	badSrc := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "test",
		AccessType: drvpkg.AccessTypeSSHTunnel,
		SSHTunnel: &drvpkg.SSHTunnelConfig{
			SSH:    &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"},
			DBHost: "127.0.0.1",
			DBPort: 3306,
		},
	}
	execErr := exec.Execute(context.Background(), badSrc, drvpkg.DBMSLocation{})
	if execErr == nil {
		t.Fatal("expected Execute to fail")
	}

	// Verify Unwrap chain is intact: errors.As must also reach the inner error.
	var migErr *MigrationError
	if errors.As(execErr, &migErr) {
		if migErr.Unwrap() == nil {
			t.Error("MigrationError.Unwrap() should return non-nil inner error")
		}
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
//	go test ./... -run TestDumpExecutor -v

func TestDumpExecutorIntegration_Execute(t *testing.T) {
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Skip("MYSQL_TEST_DSN not set; skipping DumpExecutor integration test")
	}

	loc := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306, Username: "root", Password: "pass"},
	}

	drv := drvpkg.NewMySQLDriver()
	if err := drv.TestConnection(context.Background(), loc); err != nil {
		t.Skipf("MySQL not reachable: %v", err)
	}

	exec, err := NewDumpExecutor(loc, drvpkg.ScopeSchemaOnly, nil)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()

	if err := exec.Execute(context.Background(), loc, drvpkg.DBMSLocation{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	info, statErr := os.Stat(exec.OutputPath())
	if statErr != nil {
		t.Fatalf("staging file not found at %q: %v", exec.OutputPath(), statErr)
	}
	if info.Size() == 0 {
		t.Errorf("staging file is empty at %q", exec.OutputPath())
	}
	t.Logf("dump written to %q (%d bytes)", exec.OutputPath(), info.Size())

	// Clean up staging file.
	os.Remove(exec.OutputPath())
}

func TestDumpExecutorIntegration_Execute_MigrationError(t *testing.T) {
	if os.Getenv("MYSQL_TEST_DSN") == "" {
		t.Skip("MYSQL_TEST_DSN not set; skipping DumpExecutor integration test")
	}

	exec, err := NewDumpExecutor(
		drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL, Database: "ghost"},
		drvpkg.ScopeFull,
		nil,
	)
	if err != nil {
		t.Fatalf("NewDumpExecutor: %v", err)
	}
	defer exec.Cleanup()

	badSrc := drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "ghost",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "192.0.2.1", Port: 3306},
	}
	execErr := exec.Execute(context.Background(), badSrc, drvpkg.DBMSLocation{})
	if execErr == nil {
		t.Fatal("expected Execute to fail for unreachable host")
	}
	var migErr *MigrationError
	if !errors.As(execErr, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", execErr, execErr)
	}
	if migErr.Stage != StageDump {
		t.Errorf("Stage = %q, want %q", migErr.Stage, StageDump)
	}
}
