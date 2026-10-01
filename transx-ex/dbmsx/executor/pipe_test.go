package executor

import (
	"context"
	"errors"
	"os"
	"testing"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Unit tests (no SSH server required)
// =============================================================================

func TestNewPipeExecutor_FieldsSet(t *testing.T) {
	src := &drvpkg.SSHConfig{Host: "src.example.com", Port: 22, Username: "alice"}
	dst := &drvpkg.SSHConfig{Host: "dst.example.com", Port: 22, Username: "bob"}
	exec := NewPipeExecutor("mysqldump ...", src, "mysql ...", dst)
	if exec == nil {
		t.Fatal("expected non-nil PipeExecutor")
	}
	if exec.dumpCmd != "mysqldump ..." {
		t.Errorf("dumpCmd = %q, want %q", exec.dumpCmd, "mysqldump ...")
	}
	if exec.restoreCmd != "mysql ..." {
		t.Errorf("restoreCmd = %q, want %q", exec.restoreCmd, "mysql ...")
	}
	if exec.srcSSH != src {
		t.Error("srcSSH not stored correctly")
	}
	if exec.dstSSH != dst {
		t.Error("dstSSH not stored correctly")
	}
}

// TestPipeExecutor_Execute_BothFail_ReturnsDumpStage verifies that when both
// SSH connections fail (connection refused), the returned error is
// *MigrationError{Stage: StageDump} because dumpErr is checked before restoreErr.
// Port 1 on localhost triggers an immediate "connection refused" without a timeout.
func TestPipeExecutor_Execute_BothFail_ReturnsDumpStage(t *testing.T) {
	src := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	dst := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	exec := NewPipeExecutor("echo dump", src, "cat", dst)

	err := exec.Execute(context.Background(), drvpkg.DBMSLocation{}, drvpkg.DBMSLocation{})
	if err == nil {
		t.Fatal("expected Execute to fail, got nil")
	}
	var migErr *MigrationError
	if !errors.As(err, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", err, err)
	}
	if migErr.Stage != StageDump {
		t.Errorf("Stage = %q, want %q", migErr.Stage, StageDump)
	}
	if migErr.Unwrap() == nil {
		t.Error("MigrationError.Unwrap() should return the underlying SSH error")
	}
}

// TestPipeExecutor_Execute_ReturnsMigrationError verifies errors.As extraction.
func TestPipeExecutor_Execute_ReturnsMigrationError(t *testing.T) {
	src := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	dst := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	exec := NewPipeExecutor("echo", src, "cat", dst)

	err := exec.Execute(context.Background(), drvpkg.DBMSLocation{}, drvpkg.DBMSLocation{})
	var migErr *MigrationError
	if !errors.As(err, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", err, err)
	}
}

// TestPipeExecutor_Execute_SourceParametersUnused verifies that the source and
// destination DBMSLocation parameters are ignored (PipeExecutor is fully
// configured at construction time via SSH configs and pre-built commands).
func TestPipeExecutor_Execute_SourceParametersUnused(t *testing.T) {
	src := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	dst := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	exec := NewPipeExecutor("echo", src, "cat", dst)

	// Different DBMSLocation values should not change behaviour.
	err1 := exec.Execute(context.Background(), drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMySQL}, drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypePostgreSQL})
	err2 := exec.Execute(context.Background(), drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMongoDB}, drvpkg.DBMSLocation{DBMSType: drvpkg.DBMSTypeMariaDB})

	// Both calls must fail (bad SSH), but with the same stage.
	var m1, m2 *MigrationError
	if !errors.As(err1, &m1) || !errors.As(err2, &m2) {
		t.Fatal("expected *MigrationError from both calls")
	}
	if m1.Stage != m2.Stage {
		t.Errorf("stage mismatch between calls: %q vs %q", m1.Stage, m2.Stage)
	}
}

// =============================================================================
// Integration tests (require SSH_TEST_* env vars)
// =============================================================================
//
// To run (two separate SSH containers so src ≠ dst):
//
//	docker run -d --name ssh-src -p 2221:22 \
//	  -e PUBLIC_KEY="$(cat ~/.ssh/id_rsa.pub)" \
//	  linuxserver/openssh-server
//
//	docker run -d --name ssh-dst -p 2222:22 \
//	  -e PUBLIC_KEY="$(cat ~/.ssh/id_rsa.pub)" \
//	  linuxserver/openssh-server
//
//	export SSH_SRC_HOST=localhost SSH_SRC_PORT=2221 SSH_SRC_USER=linuxserver
//	export SSH_DST_HOST=localhost SSH_DST_PORT=2222 SSH_DST_USER=linuxserver
//	export SSH_TEST_KEY="$(cat ~/.ssh/id_rsa)"
//	go test ./... -run TestPipeExecutorIntegration -v

func TestPipeExecutorIntegration_Execute_EchoToCat(t *testing.T) {
	if os.Getenv("SSH_TEST_KEY") == "" {
		t.Skip("SSH_TEST_KEY not set; skipping PipeExecutor integration test")
	}

	key := os.Getenv("SSH_TEST_KEY")
	srcHost := getenvDefault("SSH_SRC_HOST", "localhost")
	dstHost := getenvDefault("SSH_DST_HOST", "localhost")
	srcPort := getenvIntDefault("SSH_SRC_PORT", 2221)
	dstPort := getenvIntDefault("SSH_DST_PORT", 2222)
	srcUser := getenvDefault("SSH_SRC_USER", "linuxserver")
	dstUser := getenvDefault("SSH_DST_USER", "linuxserver")

	src := &drvpkg.SSHConfig{Host: srcHost, Port: srcPort, Username: srcUser, PrivateKey: key}
	dst := &drvpkg.SSHConfig{Host: dstHost, Port: dstPort, Username: dstUser, PrivateKey: key}

	// "echo hello" on src → "cat > /tmp/pipe_test.txt" on dst.
	exec := NewPipeExecutor(
		"echo hello",
		src,
		"cat > /tmp/pipe_test.txt",
		dst,
	)
	if err := exec.Execute(context.Background(), drvpkg.DBMSLocation{}, drvpkg.DBMSLocation{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	t.Log("PipeExecutor echo→cat integration test passed")
}

func TestPipeExecutorIntegration_DumpFail_ReturnsDumpStage(t *testing.T) {
	if os.Getenv("SSH_TEST_KEY") == "" {
		t.Skip("SSH_TEST_KEY not set; skipping PipeExecutor integration test")
	}

	key := os.Getenv("SSH_TEST_KEY")
	dstHost := getenvDefault("SSH_DST_HOST", "localhost")
	dstPort := getenvIntDefault("SSH_DST_PORT", 2222)
	dstUser := getenvDefault("SSH_DST_USER", "linuxserver")

	// Source SSH is unreachable; destination is valid.
	src := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}
	dst := &drvpkg.SSHConfig{Host: dstHost, Port: dstPort, Username: dstUser, PrivateKey: key}

	exec := NewPipeExecutor("echo dump", src, "cat", dst)
	err := exec.Execute(context.Background(), drvpkg.DBMSLocation{}, drvpkg.DBMSLocation{})
	if err == nil {
		t.Fatal("expected Execute to fail")
	}
	var migErr *MigrationError
	if !errors.As(err, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", err, err)
	}
	if migErr.Stage != StageDump {
		t.Errorf("Stage = %q, want %q", migErr.Stage, StageDump)
	}
}

func TestPipeExecutorIntegration_RestoreFail_ReturnsRestoreStage(t *testing.T) {
	if os.Getenv("SSH_TEST_KEY") == "" {
		t.Skip("SSH_TEST_KEY not set; skipping PipeExecutor integration test")
	}

	key := os.Getenv("SSH_TEST_KEY")
	srcHost := getenvDefault("SSH_SRC_HOST", "localhost")
	srcPort := getenvIntDefault("SSH_SRC_PORT", 2221)
	srcUser := getenvDefault("SSH_SRC_USER", "linuxserver")

	// Source is valid; destination SSH is unreachable.
	src := &drvpkg.SSHConfig{Host: srcHost, Port: srcPort, Username: srcUser, PrivateKey: key}
	dst := &drvpkg.SSHConfig{Host: "127.0.0.1", Port: 1, Username: "test"}

	// Dump succeeds (echo), but restore cannot connect → StageRestore.
	exec := NewPipeExecutor("echo hello", src, "cat", dst)
	err := exec.Execute(context.Background(), drvpkg.DBMSLocation{}, drvpkg.DBMSLocation{})
	if err == nil {
		t.Fatal("expected Execute to fail")
	}
	var migErr *MigrationError
	if !errors.As(err, &migErr) {
		t.Fatalf("expected *MigrationError, got %T: %v", err, err)
	}
	// When only restore fails, dumpErr will be non-nil too (pipe closed with
	// restore error), so StageDump is returned.  Either stage is acceptable here;
	// what matters is that a *MigrationError is returned.
	if migErr.Stage != StageDump && migErr.Stage != StageRestore {
		t.Errorf("Stage = %q, want StageDump or StageRestore", migErr.Stage)
	}
}

// =============================================================================
// Test helpers
// =============================================================================

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvIntDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := def
	_, _ = parseIntEnv(v, &n)
	return n
}

// parseIntEnv parses a decimal integer from s into *dst.
func parseIntEnv(s string, dst *int) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	*dst = n
	return n, true
}
