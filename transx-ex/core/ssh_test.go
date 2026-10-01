package core

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"github.com/cloud-barista/cm-centipede/transx-ex/core/sshtest"
)

// =============================================================================
// parsePrivateKey unit tests (no live SSH server required)
// =============================================================================

// rsaPEM generates a fresh PEM-encoded RSA-2048 private key for testing.
func rsaPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
}

func TestParsePrivateKey_NoneConfigured(t *testing.T) {
	cfg := &SSHConfig{Host: "localhost", Username: "user"}
	method, err := parsePrivateKey(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method != nil {
		t.Errorf("expected nil method when no auth fields are set, got %T", method)
	}
}

func TestParsePrivateKey_InlineKey(t *testing.T) {
	pemBytes := rsaPEM(t)
	cfg := &SSHConfig{
		Host:       "localhost",
		Username:   "user",
		PrivateKey: string(pemBytes),
	}
	method, err := parsePrivateKey(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method == nil {
		t.Fatal("expected non-nil AuthMethod for valid inline key")
	}
}

func TestParsePrivateKey_InlineKey_Invalid(t *testing.T) {
	cfg := &SSHConfig{
		Host:       "localhost",
		Username:   "user",
		PrivateKey: "not-a-valid-pem-key",
	}
	_, err := parsePrivateKey(cfg)
	if err == nil {
		t.Fatal("expected error for invalid inline private key")
	}
	if !strings.Contains(err.Error(), "parse inline private key") {
		t.Errorf("error message should mention inline private key, got: %v", err)
	}
}

func TestParsePrivateKey_KeyPath(t *testing.T) {
	pemBytes := rsaPEM(t)
	f, err := os.CreateTemp(t.TempDir(), "id_rsa_*.pem")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.Write(pemBytes); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	f.Close()

	cfg := &SSHConfig{
		Host:           "localhost",
		Username:       "user",
		PrivateKeyPath: f.Name(),
	}
	method, err := parsePrivateKey(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method == nil {
		t.Fatal("expected non-nil AuthMethod for valid key file")
	}
}

func TestParsePrivateKey_KeyPath_NotFound(t *testing.T) {
	cfg := &SSHConfig{
		Host:           "localhost",
		Username:       "user",
		PrivateKeyPath: "/nonexistent/path/id_rsa",
	}
	_, err := parsePrivateKey(cfg)
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
	if !strings.Contains(err.Error(), "read private key file") {
		t.Errorf("error message should mention file path, got: %v", err)
	}
}

func TestParsePrivateKey_InlineKey_TakesPriorityOverPath(t *testing.T) {
	pemBytes := rsaPEM(t)
	cfg := &SSHConfig{
		Host:           "localhost",
		Username:       "user",
		PrivateKey:     string(pemBytes),
		PrivateKeyPath: "/nonexistent/path/id_rsa", // should be ignored
	}
	// If inline key takes priority, no error is expected despite bad path.
	method, err := parsePrivateKey(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if method == nil {
		t.Fatal("expected non-nil AuthMethod")
	}
}

func TestParsePrivateKey_UseAgent_NoSocket(t *testing.T) {
	orig := os.Getenv("SSH_AUTH_SOCK")
	os.Unsetenv("SSH_AUTH_SOCK")
	t.Cleanup(func() {
		if orig != "" {
			os.Setenv("SSH_AUTH_SOCK", orig)
		}
	})

	cfg := &SSHConfig{Host: "localhost", Username: "user", UseAgent: true}
	_, err := parsePrivateKey(cfg)
	if err == nil {
		t.Fatal("expected error when SSH_AUTH_SOCK is unset")
	}
	if !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Errorf("error should mention SSH_AUTH_SOCK, got: %v", err)
	}
}

// =============================================================================
// executeViaSSH integration tests (require SSH_TEST_HOST to be set)
// =============================================================================
//
// To run these tests, start a local SSH container:
//
//	docker run -d -p 2222:22 \
//	  -e PUBLIC_KEY="$(cat ~/.ssh/id_rsa.pub)" \
//	  -e USER_NAME=testuser \
//	  lscr.io/linuxserver/openssh-server:latest
//
// Then export:
//
//	export SSH_TEST_HOST=localhost
//	export SSH_TEST_PORT=2222
//	export SSH_TEST_USER=testuser
//	export SSH_TEST_KEY=~/.ssh/id_rsa

func sshTestCfg(t *testing.T) *SSHConfig {
	t.Helper()
	host := os.Getenv("SSH_TEST_HOST")
	if host == "" {
		t.Skip("SSH_TEST_HOST not set; skipping integration test")
	}

	cfg := &SSHConfig{
		Host:     host,
		Username: "testuser",
	}
	if v := os.Getenv("SSH_TEST_PORT"); v != "" {
		port := 0
		if _, err := bytes.NewBufferString(v).Read([]byte(v)); err == nil {
			for _, ch := range v {
				port = port*10 + int(ch-'0')
			}
		}
		cfg.Port = port
	} else {
		cfg.Port = 2222
	}
	if v := os.Getenv("SSH_TEST_USER"); v != "" {
		cfg.Username = v
	}
	if keyPath := os.Getenv("SSH_TEST_KEY"); keyPath != "" {
		cfg.PrivateKeyPath = keyPath
	} else {
		home, _ := os.UserHomeDir()
		cfg.PrivateKeyPath = home + "/.ssh/id_rsa"
	}
	return cfg
}

func TestExecuteViaSSH_Echo(t *testing.T) {
	cfg := sshTestCfg(t)

	var buf bytes.Buffer
	if err := Exec(cfg, "echo hello", nil, &buf); err != nil {
		t.Fatalf("executeViaSSH error: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "hello") {
		t.Errorf("expected output to contain %q, got %q", "hello", got)
	}
}

func TestExecuteViaSSH_Stdin(t *testing.T) {
	cfg := sshTestCfg(t)

	input := "hello from stdin"
	var buf bytes.Buffer
	if err := Exec(cfg, "cat", strings.NewReader(input), &buf); err != nil {
		t.Fatalf("executeViaSSH error: %v", err)
	}
	if !strings.Contains(buf.String(), input) {
		t.Errorf("expected cat to echo %q, got %q", input, buf.String())
	}
}

func TestExecuteViaSSH_BadHost(t *testing.T) {
	cfg := &SSHConfig{
		Host:           "192.0.2.1", // TEST-NET, unreachable by definition
		Port:           22,
		Username:       "user",
		ConnectTimeout: 2,
	}
	err := Exec(cfg, "echo hello", nil, nil)
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestExecuteViaSSH_BadKey(t *testing.T) {
	cfg := sshTestCfg(t)

	// Replace the real key with a freshly generated one that the server doesn't know.
	pemBytes := rsaPEM(t)
	cfg.PrivateKey = string(pemBytes)
	cfg.PrivateKeyPath = ""

	err := Exec(cfg, "echo hello", nil, nil)
	if err == nil {
		t.Fatal("expected authentication error for unknown key")
	}
}

// =============================================================================
// SSHExec tests (in-process SSH server, no bastion required)
// =============================================================================

// startServer boots an in-process SSH server and returns it with a matching
// client config. No auth fields are set, matching the server's NoClientAuth.
func startServer(t *testing.T) (*sshtest.Server, *SSHConfig) {
	t.Helper()
	srv, err := sshtest.New()
	if err != nil {
		t.Fatalf("start SSH test server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	return srv, &SSHConfig{
		Host:           srv.Host(),
		Port:           srv.Port(),
		Username:       "tester",
		ConnectTimeout: 5,
	}
}

func TestSSHExec_PersistentReusesOneConnection(t *testing.T) {
	srv, cfg := startServer(t)

	exec, err := DialSSHExec(cfg)
	if err != nil {
		t.Fatalf("DialSSHExec: %v", err)
	}
	defer exec.Close()

	// Stands in for an Inspect issuing one query per table.
	const commands = 6
	for i := 0; i < commands; i++ {
		var buf bytes.Buffer
		if err := exec.Run("query", nil, &buf); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if buf.String() != "query" {
			t.Fatalf("Run %d output = %q, want %q", i, buf.String(), "query")
		}
	}

	if got := srv.Connections(); got != 1 {
		t.Errorf("connections = %d, want 1 — the point of DialSSHExec is one handshake", got)
	}
	if got := srv.Execs(); got != commands {
		t.Errorf("execs = %d, want %d", got, commands)
	}
}

func TestSSHExec_OneShotDialsPerCommand(t *testing.T) {
	srv, cfg := startServer(t)

	exec := NewSSHExec(cfg)
	const commands = 3
	for i := 0; i < commands; i++ {
		if err := exec.Run("query", nil, nil); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	if got := srv.Connections(); got != commands {
		t.Errorf("connections = %d, want %d for one-shot mode", got, commands)
	}
	if got := srv.Execs(); got != commands {
		t.Errorf("execs = %d, want %d", got, commands)
	}
}

func TestExecuteViaSSH_StillDialsPerCall(t *testing.T) {
	srv, cfg := startServer(t)

	// The exported one-shot entry point keeps its original semantics: dump and
	// restore rely on it for a single long-running command.
	for i := 0; i < 2; i++ {
		var buf bytes.Buffer
		if err := Exec(cfg, "mysqldump db", nil, &buf); err != nil {
			t.Fatalf("ExecuteViaSSH %d: %v", i, err)
		}
		if buf.String() != "mysqldump db" {
			t.Errorf("output = %q", buf.String())
		}
	}

	if got := srv.Connections(); got != 2 {
		t.Errorf("connections = %d, want 2", got)
	}
}

func TestSSHExec_PipesStdin(t *testing.T) {
	srv, cfg := startServer(t)

	exec, err := DialSSHExec(cfg)
	if err != nil {
		t.Fatalf("DialSSHExec: %v", err)
	}
	defer exec.Close()

	// Queries reach the CLI on stdin, so a reused session must still wire it up.
	var buf bytes.Buffer
	if err := exec.Run("psql", strings.NewReader("SELECT 1\n"), &buf); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if buf.String() != "psql" {
		t.Errorf("output = %q, want %q", buf.String(), "psql")
	}
	if got := srv.Execs(); got != 1 {
		t.Errorf("execs = %d, want 1", got)
	}
}

func TestSSHExec_ReconnectsAfterConnectionDropped(t *testing.T) {
	srv, cfg := startServer(t)

	exec, err := DialSSHExec(cfg)
	if err != nil {
		t.Fatalf("DialSSHExec: %v", err)
	}
	defer exec.Close()

	if err := exec.Run("first", nil, nil); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// A long inspection can outlive a bastion's idle timeout; the next command
	// must transparently re-establish the connection.
	srv.DropConnections()

	var buf bytes.Buffer
	if err := exec.Run("second", nil, &buf); err != nil {
		t.Fatalf("Run after drop: %v", err)
	}
	if buf.String() != "second" {
		t.Errorf("output = %q, want %q", buf.String(), "second")
	}

	if got := srv.Connections(); got != 2 {
		t.Errorf("connections = %d, want 2 (initial + reconnect)", got)
	}
}

func TestSSHExec_RunAfterClose(t *testing.T) {
	_, cfg := startServer(t)

	exec, err := DialSSHExec(cfg)
	if err != nil {
		t.Fatalf("DialSSHExec: %v", err)
	}
	if err := exec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close must be idempotent — drivers defer it while also returning early.
	if err := exec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if err := exec.Run("query", nil, nil); err == nil {
		t.Error("expected an error when running on a closed executor")
	}
}

func TestSSHExec_DialFailsOnUnreachableHost(t *testing.T) {
	cfg := &SSHConfig{
		Host:           "192.0.2.1", // TEST-NET, unreachable by definition
		Port:           22,
		Username:       "user",
		ConnectTimeout: 2,
	}
	if _, err := DialSSHExec(cfg); err == nil {
		t.Error("expected DialSSHExec to fail for an unreachable host")
	}
	// One-shot mode defers dialing to Run, which must surface the same failure.
	if err := NewSSHExec(cfg).Run("query", nil, nil); err == nil {
		t.Error("expected Run to fail for an unreachable host")
	}
}
