package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// =============================================================================
// SSHConfig
// =============================================================================

// SSHConfig holds SSH server connection parameters.
//
// Authentication sources are tried in this order: inline PrivateKey, then
// PrivateKeyPath, then the SSH agent, then Password. Every source that is set
// contributes a method, so a caller may supply several. When none is set and an
// agent is reachable the agent is used as a fallback; when none is set at all
// the connection is attempted without a method, which is what a server
// configured for NoClientAuth expects.
//
// Options that describe how a transfer runs rather than how to connect (rsync
// flags, for instance) do not belong here — see storagex.RsyncOption.
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port,omitempty"`
	Username       string `json:"username"`
	Password       string `json:"password,omitempty"`
	ConnectTimeout int    `json:"connectTimeout,omitempty"`

	// PrivateKey is a PEM-encoded private key. Literal "\n" escape sequences are
	// accepted, so a key injected through an environment variable or a YAML
	// scalar works without pre-processing.
	PrivateKey     string `json:"privateKey,omitempty"`
	PrivateKeyPath string `json:"privateKeyPath,omitempty"`
	UseAgent       bool   `json:"useAgent,omitempty"`
}

// =============================================================================
// One-shot execution
// =============================================================================

// Exec dials the host described by cfg, runs cmd on the remote shell, and wires
// stdin/stdout when the respective arguments are non-nil. The connection is
// closed before Exec returns.
//
// A default connect timeout of 30 s applies when cfg.ConnectTimeout is 0.
// Use SSHExec instead when several commands run against the same host.
func Exec(cfg *SSHConfig, cmd string, stdin io.Reader, stdout io.Writer) error {
	client, err := dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	return runSession(client, cmd, stdin, stdout)
}

// CombinedOutput runs cmd on the host described by cfg and returns stdout with
// any stderr appended, mirroring exec.Cmd.CombinedOutput. Prefer Exec or
// SSHExec when the output is machine-parsed: those keep stderr out of it.
func CombinedOutput(cfg *SSHConfig, cmd string) ([]byte, error) {
	var out bytes.Buffer
	err := Exec(cfg, cmd, nil, &out)
	if err != nil {
		if stderr := SSHStderr(err); stderr != "" {
			out.WriteString(stderr)
		}
	}
	return out.Bytes(), err
}

// =============================================================================
// Runner — several commands over one connection
// =============================================================================

// Runner executes a single command over an already-established SSH connection
// and returns its stdout. Any stderr is folded into the error, so diagnostics
// never contaminate parsed output.
type Runner func(cmd string) ([]byte, error)

// WithClient dials cfg once and invokes fn with a Runner bound to that single
// connection, so a multi-command operation pays the handshake cost only once.
// The connection is closed when fn returns.
func WithClient(cfg *SSHConfig, fn func(run Runner) error) error {
	exec, err := DialSSHExec(cfg)
	if err != nil {
		return err
	}
	defer exec.Close() //nolint:errcheck — close failure cannot affect fn's result

	return fn(func(cmd string) ([]byte, error) {
		var out bytes.Buffer
		if runErr := exec.Run(cmd, nil, &out); runErr != nil {
			return out.Bytes(), runErr
		}
		return out.Bytes(), nil
	})
}

// =============================================================================
// SSHExec — connection reuse across commands
// =============================================================================

// SSHExec runs commands on a remote host over SSH.
//
// A single operation can issue a large number of small commands: an Inspect with
// per-table metrics enabled runs one query per table (two for the relational
// engines), and a filesystem Inspect walks one directory at a time. Dialing per
// command would mean a TCP connect, an SSH handshake and a key exchange each
// time, which dominates the runtime long before the commands themselves do.
// SSHExec therefore holds one connection open and opens a fresh session per
// command, so a wide Inspect pays the handshake once.
//
// Instances are safe for sequential use from one goroutine; Run serialises
// concurrent callers rather than multiplexing them.
type SSHExec struct {
	cfg *SSHConfig

	mu     sync.Mutex
	client *ssh.Client
	// persistent selects reuse: when false, Run dials and closes a connection
	// per command, matching Exec's behavior.
	persistent bool
	closed     bool
}

// NewSSHExec returns an executor that dials a fresh connection for every Run.
// Use it for one-off commands, or where holding a connection open across a
// long-running transfer is undesirable.
func NewSSHExec(cfg *SSHConfig) *SSHExec {
	return &SSHExec{cfg: cfg}
}

// DialSSHExec opens a connection up front and reuses it for every Run until
// Close. The caller must Close the returned executor.
func DialSSHExec(cfg *SSHConfig) (*SSHExec, error) {
	client, err := dial(cfg)
	if err != nil {
		return nil, err
	}
	return &SSHExec{cfg: cfg, client: client, persistent: true}, nil
}

// Run executes cmd on the remote host, wiring stdin/stdout when non-nil.
func (e *SSHExec) Run(cmd string, stdin io.Reader, stdout io.Writer) error {
	if e == nil {
		return fmt.Errorf("ssh exec: nil executor")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return fmt.Errorf("ssh exec: already closed")
	}

	if !e.persistent {
		client, err := dial(e.cfg)
		if err != nil {
			return err
		}
		defer client.Close()
		return runSession(client, cmd, stdin, stdout)
	}

	if e.client == nil {
		client, err := dial(e.cfg)
		if err != nil {
			return err
		}
		e.client = client
	}

	session, err := e.client.NewSession()
	if err != nil {
		// The connection went away — an idle timeout on the bastion, for
		// instance. Reconnecting is safe here precisely because the session
		// could not be opened, so cmd has not run and cannot run twice.
		e.client.Close()
		e.client = nil

		client, dialErr := dial(e.cfg)
		if dialErr != nil {
			return fmt.Errorf("ssh new session (%v) and reconnect failed: %w", err, dialErr)
		}
		e.client = client

		session, err = e.client.NewSession()
		if err != nil {
			return fmt.Errorf("ssh new session after reconnect: %w", err)
		}
	}
	defer session.Close()

	return pumpSession(session, cmd, stdin, stdout)
}

// Close releases the held connection, if any. It is safe to call more than once.
func (e *SSHExec) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	e.closed = true
	if e.client == nil {
		return nil
	}
	err := e.client.Close()
	e.client = nil
	return err
}

// =============================================================================
// Dial and session plumbing
// =============================================================================

// dial opens an SSH connection to the host described by cfg.
func dial(cfg *SSHConfig) (*ssh.Client, error) {
	authMethods, err := parseAuthMethods(cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh auth setup: %w", err)
	}

	timeout := 30 * time.Second
	if cfg.ConnectTimeout > 0 {
		timeout = time.Duration(cfg.ConnectTimeout) * time.Second
	}

	port := "22"
	if cfg.Port > 0 {
		port = strconv.Itoa(cfg.Port)
	}

	addr := net.JoinHostPort(cfg.Host, port)
	netConn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // host key verification is the caller's responsibility
		Timeout:         timeout,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, clientCfg)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// runSession opens one session on client and runs cmd on it.
func runSession(client *ssh.Client, cmd string, stdin io.Reader, stdout io.Writer) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh new session: %w", err)
	}
	defer session.Close()
	return pumpSession(session, cmd, stdin, stdout)
}

// pumpSession wires stdin/stdout to session and runs cmd to completion.
//
// stderr is always captured, whether or not the caller wants stdout: a remote
// CLI reports why it failed there, and session.Run only tells us the exit
// status. Without it a failed mysqldump or psql would surface as a bare
// "Process exited with status 1". On failure the captured text is returned in
// an *SSHRunError so callers can attach it to their own error types.
func pumpSession(session *ssh.Session, cmd string, stdin io.Reader, stdout io.Writer) error {
	var wg sync.WaitGroup

	stderr := &cappedBuffer{limit: sshStderrLimit}
	session.Stderr = stderr

	if stdin != nil {
		pw, pipeErr := session.StdinPipe()
		if pipeErr != nil {
			return fmt.Errorf("ssh stdin pipe: %w", pipeErr)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer pw.Close()
			io.Copy(pw, stdin) //nolint:errcheck
		}()
	}

	if stdout != nil {
		pr, pipeErr := session.StdoutPipe()
		if pipeErr != nil {
			return fmt.Errorf("ssh stdout pipe: %w", pipeErr)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			io.Copy(stdout, pr) //nolint:errcheck
		}()
	}

	runErr := session.Run(cmd)
	wg.Wait()
	if runErr != nil {
		return &SSHRunError{Cmd: cmd, Stderr: stderr.String(), Err: runErr}
	}
	return nil
}

// =============================================================================
// SSHRunError — remote command failure with its stderr
// =============================================================================

// sshStderrLimit bounds the stderr kept per command. A failing CLI can write
// one line or a flood of them (mongorestore reports every rejected document),
// and the text only ever ends up in an error message, so the tail beyond this
// is dropped rather than held in memory.
const sshStderrLimit = 64 << 10

// SSHRunError reports a remote command that ran but exited non-zero, carrying
// what it wrote to stderr. Unwrap returns the underlying *ssh.ExitError, so
// callers that only care about the exit status keep working unchanged.
type SSHRunError struct {
	Cmd    string // command as sent to the remote shell
	Stderr string // captured stderr, truncated to sshStderrLimit
	Err    error  // underlying ssh error (usually *ssh.ExitError)
}

func (e *SSHRunError) Error() string {
	if s := strings.TrimSpace(e.Stderr); s != "" {
		return fmt.Sprintf("%v: %s", e.Err, s)
	}
	return e.Err.Error()
}

func (e *SSHRunError) Unwrap() error { return e.Err }

// SSHStderr returns the stderr captured for err, or "" when err did not come
// from a remote command failure. Drivers use it to fill OperationError.Output.
func SSHStderr(err error) string {
	var runErr *SSHRunError
	if errors.As(err, &runErr) {
		return strings.TrimSpace(runErr.Stderr)
	}
	return ""
}

// cappedBuffer accumulates up to limit bytes and silently discards the rest.
// Writes always report full acceptance: the remote command must not see a short
// write on stderr just because we stopped keeping its output.
type cappedBuffer struct {
	buf   []byte
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }

// =============================================================================
// Authentication
// =============================================================================

// parseAuthMethods returns the available ssh.AuthMethod list for cfg.
// Priority: inline PrivateKey → PrivateKeyPath file → SSH Agent → Password.
//
// When cfg names no source at all and an agent is reachable, the agent is added
// as a fallback. An empty result is returned rather than an error: a server
// configured for NoClientAuth accepts a connection that offers no method, and
// refusing to dial here would make that unreachable.
func parseAuthMethods(cfg *SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(normalizePrivateKey(cfg.PrivateKey)))
		if err != nil {
			return nil, fmt.Errorf("parse inline private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.PrivateKeyPath != "" {
		pemBytes, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key file %q: %w", cfg.PrivateKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key file %q: %w", cfg.PrivateKeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.UseAgent {
		// Explicitly requested, so an unavailable agent is an error rather than
		// something to quietly skip.
		socket := os.Getenv("SSH_AUTH_SOCK")
		if socket == "" {
			return nil, fmt.Errorf("SSH_AUTH_SOCK is not set: SSH agent is unavailable")
		}
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return nil, fmt.Errorf("dial SSH agent %q: %w", socket, err)
		}
		methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if len(methods) == 0 {
		// Nothing was configured. An agent, if one happens to be running, is the
		// conventional last resort; its absence is not an error here.
		if m := agentAuthIfAvailable(); m != nil {
			methods = append(methods, m)
		}
	}

	return methods, nil
}

// agentAuthIfAvailable returns an SSH agent auth method, or nil when no agent is
// reachable. Unlike the UseAgent branch above it never reports an error: it is
// only consulted as a fallback the caller did not ask for.
func agentAuthIfAvailable() ssh.AuthMethod {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers)
}

// parsePrivateKey returns the single highest-priority auth method, honoring the
// documented order PrivateKey > PrivateKeyPath > Agent. Each source is tried
// independently and short-circuits on the first one present, so an unavailable
// lower-priority source (e.g. a missing PrivateKeyPath file) never overrides a
// valid higher-priority one. Returns (nil, nil) when no key source is set.
func parsePrivateKey(cfg *SSHConfig) (ssh.AuthMethod, error) {
	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(normalizePrivateKey(cfg.PrivateKey)))
		if err != nil {
			return nil, fmt.Errorf("parse inline private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	}
	if cfg.PrivateKeyPath != "" {
		pemBytes, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key file %q: %w", cfg.PrivateKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key file %q: %w", cfg.PrivateKeyPath, err)
		}
		return ssh.PublicKeys(signer), nil
	}
	if cfg.UseAgent {
		socket := os.Getenv("SSH_AUTH_SOCK")
		if socket == "" {
			return nil, fmt.Errorf("SSH_AUTH_SOCK is not set: SSH agent is unavailable")
		}
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return nil, fmt.Errorf("dial SSH agent %q: %w", socket, err)
		}
		return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), nil
	}
	return nil, nil
}

// NormalizePrivateKey converts literal "\n" escape sequences to real newlines.
// Callers that hand a key to an external tool rather than to this package — the
// rsync executor writes one to a temporary file — need the same normalisation
// dial applies internally.
func NormalizePrivateKey(key string) string { return normalizePrivateKey(key) }

// normalizePrivateKey converts literal "\n" escape sequences to real newlines.
// A key that already contains real newlines is returned unchanged, so both a
// multi-line PEM and a single-line environment variable parse.
func normalizePrivateKey(key string) string {
	if strings.Contains(key, "\n") && !strings.Contains(key, "\\n") {
		return key
	}
	return strings.ReplaceAll(key, "\\n", "\n")
}
