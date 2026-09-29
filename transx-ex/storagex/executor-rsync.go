package storagex

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/filter"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ============================================================================
// Transfer Mode
// ============================================================================

// TransferMode defines how rsync transfer is executed.
type TransferMode string

const (
	// TransferModePull pulls data from remote source to local.
	// Direction: remote-source → local
	TransferModePull TransferMode = "pull"

	// TransferModePush pushes data from local to remote destination.
	// Direction: local → remote-destination
	TransferModePush TransferMode = "push"

	// TransferModeAgentForward uses SSH Agent Forwarding to execute rsync
	// on the source server, transferring directly to destination.
	// Direction: remote-source → remote-destination (via source server)
	TransferModeAgentForward TransferMode = "agent-forward"
)

// ============================================================================
// RsyncExecutor
// ============================================================================

// RsyncExecutor implements Executor using rsync for file transfers.
// Supports three transfer modes: Pull, Push, and Agent Forwarding.
type RsyncExecutor struct {
	Mode             TransferMode // Transfer mode (pull, push, agent-forward)
	DeleteExtraneous bool         // --delete: remove extraneous files from destination
	DryRun           bool         // --dry-run: perform trial run without changes
	Verbose          bool         // -v: increase verbosity
	AdditionalArgs   []string     // Additional rsync arguments

	tempKeyFile string // Temporary key file path (for PrivateKey content)
}

// NewRsyncExecutor creates a new RsyncExecutor with automatically determined transfer mode:
//   - SSH → SSH: AgentForward
//   - SSH → Local: Pull
//   - Local → SSH: Push
//   - Local → Local: not supported (returns error)
func NewRsyncExecutor(src, dst DataLocation) (*RsyncExecutor, error) {
	mode, err := determineTransferMode(src, dst)
	if err != nil {
		return nil, err
	}

	exec := &RsyncExecutor{
		Mode:             mode,
		DeleteExtraneous: false,
		DryRun:           false,
		Verbose:          false,
	}

	// Apply rsync options from source, then merge the destination's additively:
	// either endpoint may ask for a dry run or extra verbosity.
	for _, loc := range []DataLocation{src, dst} {
		if loc.Filesystem == nil || loc.Filesystem.Rsync == nil {
			continue
		}
		opt := loc.Filesystem.Rsync
		exec.DeleteExtraneous = exec.DeleteExtraneous || opt.Delete
		exec.Verbose = exec.Verbose || opt.Verbose
		exec.DryRun = exec.DryRun || opt.DryRun
	}

	return exec, nil
}

// determineTransferMode selects transfer mode based on endpoint types.
func determineTransferMode(src, dst DataLocation) (TransferMode, error) {
	srcIsRemote := src.Filesystem != nil && src.Filesystem.SSH != nil
	dstIsRemote := dst.Filesystem != nil && dst.Filesystem.SSH != nil

	switch {
	case srcIsRemote && dstIsRemote:
		return TransferModeAgentForward, nil
	case srcIsRemote && !dstIsRemote:
		return TransferModePull, nil
	case !srcIsRemote && dstIsRemote:
		return TransferModePush, nil
	default:
		return "", fmt.Errorf("local-to-local transfer not supported: at least one endpoint must be remote (SSH)")
	}
}

// Execute performs rsync transfer from source to destination.
func (e *RsyncExecutor) Execute(ctx context.Context, source, destination DataLocation) error {
	switch e.Mode {
	case TransferModePull:
		return e.executePull(ctx, source, destination)
	case TransferModePush:
		return e.executePush(ctx, source, destination)
	case TransferModeAgentForward:
		return e.executeAgentForward(ctx, source, destination)
	default:
		return fmt.Errorf("unknown transfer mode: %s", e.Mode)
	}
}

// ============================================================================
// Pull Mode: Remote Source → Local
// ============================================================================

// executePull pulls data from remote source to local destination.
func (e *RsyncExecutor) executePull(ctx context.Context, source, destination DataLocation) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	// Cleanup temporary key file after execution
	defer e.cleanupTempKeyFile()

	if err := os.MkdirAll(destination.Path, 0755); err != nil {
		return fmt.Errorf("failed to create local destination directory: %w", err)
	}

	args, err := e.buildLocalRsyncArgs(source, destination)
	if err != nil {
		return err
	}

	if err := e.runLocal(ctx, args, "pull"); err != nil {
		return err
	}

	return nil
}

// ============================================================================
// Push Mode: Local → Remote Destination
// ============================================================================

// executePush pushes data from local source to remote destination.
func (e *RsyncExecutor) executePush(ctx context.Context, source, destination DataLocation) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	// Cleanup temporary key file after execution
	defer e.cleanupTempKeyFile()

	if destination.Filesystem != nil && destination.Filesystem.SSH != nil {
		if err := e.ensureRemoteDir(destination.Filesystem.SSH, destination.Path); err != nil {
			return fmt.Errorf("failed to prepare remote destination directory: %w", err)
		}
	}

	args, err := e.buildLocalRsyncArgs(source, destination)
	if err != nil {
		return err
	}

	if err := e.runLocal(ctx, args, "push"); err != nil {
		return err
	}

	return nil
}

// ============================================================================
// Agent Forward Mode: Remote Source → Remote Destination
// ============================================================================

// executeAgentForward uses SSH Agent Forwarding to execute rsync on source server.
// The source server runs rsync to transfer data directly to destination server.
func (e *RsyncExecutor) executeAgentForward(ctx context.Context, source, destination DataLocation) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if source.Filesystem == nil || source.Filesystem.SSH == nil {
		return fmt.Errorf("source SSH config is required for agent-forward mode")
	}
	if destination.Filesystem == nil || destination.Filesystem.SSH == nil {
		return fmt.Errorf("destination SSH config is required for agent-forward mode")
	}

	srcSSH := source.Filesystem.SSH
	dstSSH := destination.Filesystem.SSH

	// Ensure the destination directory exists — rsync's receiving side only
	// auto-creates the final path component when its parent already exists,
	// which fails outright against a blank destination root (e.g. an empty
	// target container).
	if err := e.ensureRemoteDir(dstSSH, destination.Path); err != nil {
		return fmt.Errorf("failed to prepare remote destination directory: %w", err)
	}

	// Load private key data
	keyData, err := e.loadPrivateKeyData(srcSSH)
	if err != nil {
		return fmt.Errorf("failed to load source private key data: %w", err)
	}

	// Create SSH signer
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	// Parse raw private key for agent (supports RSA, ECDSA, Ed25519)
	rawKey, err := ssh.ParseRawPrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("failed to parse raw private key: %w", err)
	}

	// Create in-memory SSH agent with the private key
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: rawKey}); err != nil {
		return fmt.Errorf("failed to add key to agent: %w", err)
	}

	// SSH client config for source server
	config := &ssh.ClientConfig{
		User: srcSSH.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Implement host key verification when system-level key management is available
	}

	// Connect to source server
	srcAddr := e.formatSSHAddress(srcSSH)
	client, err := ssh.Dial("tcp", srcAddr, config)
	if err != nil {
		return fmt.Errorf("failed to connect to source server: %w", err)
	}
	defer client.Close()

	// Setup agent forwarding to allow source server to authenticate to destination
	if err := agent.ForwardToAgent(client, keyring); err != nil {
		return fmt.Errorf("failed to setup agent forwarding: %w", err)
	}

	// Create session
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Request Agent Forwarding for the session
	if err := agent.RequestAgentForwarding(session); err != nil {
		return fmt.Errorf("failed to request agent forwarding: %w", err)
	}

	// Build rsync command to run on source server
	rsyncCmd, err := e.buildRemoteRsyncCommand(source, destination, dstSSH)
	if err != nil {
		return err
	}

	// Execute rsync on source server, reading its progress as it goes.
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("rsync agent-forward: open stdout: %w", err)
	}
	tail := newTailBuffer(rsyncTailBytes)
	session.Stderr = tail

	if err := session.Start(rsyncCmd); err != nil {
		return fmt.Errorf("rsync agent-forward: start: %w", err)
	}
	// Read to EOF before Wait: the session's output has to be consumed for the
	// remote command to finish writing it.
	moved := streamRsync(ctx, stdout, tail)
	if err := session.Wait(); err != nil {
		return fmt.Errorf("rsync agent-forward failed: %w\nOutput: %s", err, tail.String())
	}
	e.reportFinal(ctx, moved, tail)

	return nil
}

// runLocal runs rsync as a local process, reporting each file it sends.
//
// StdoutPipe rather than CombinedOutput: the output is read while rsync writes
// it, which is the whole point. stderr is kept separately — it is where rsync
// puts the failure, and with the streams split it would otherwise be discarded.
func (e *RsyncExecutor) runLocal(ctx context.Context, args []string, mode string) error {
	cmd := exec.Command("rsync", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("rsync %s: open stdout: %w", mode, err)
	}
	tail := newTailBuffer(rsyncTailBytes)
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("rsync %s: start: %w", mode, err)
	}
	moved := streamRsync(ctx, stdout, tail)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rsync %s failed: %w\nOutput: %s", mode, err, tail.String())
	}
	e.reportFinal(ctx, moved, tail)

	return nil
}

// reportFinal settles the figure for a finished run.
//
// --stats is preferred over the streamed sum: the two agree file for file, but a
// run that transferred nothing prints no progress lines at all, and the stats
// block is then the only statement that the transfer moved nothing rather than
// that nobody was counting.
func (e *RsyncExecutor) reportFinal(ctx context.Context, moved int64, tail *tailBuffer) {
	if stats := parseRsyncStats(tail.String()); stats >= 0 {
		moved = stats
	}
	core.ReporterFrom(ctx).Report(core.Update{Bytes: moved, Files: -1})
}

// loadPrivateKeyData returns private key bytes from PrivateKey content or PrivateKeyPath file.
func (e *RsyncExecutor) loadPrivateKeyData(cfg *SSHConfig) ([]byte, error) {
	if cfg.PrivateKey != "" {
		return []byte(normalizePrivateKey(cfg.PrivateKey)), nil
	}
	if cfg.PrivateKeyPath != "" {
		return os.ReadFile(cfg.PrivateKeyPath)
	}
	return nil, fmt.Errorf("no private key provided")
}

// formatSSHAddress formats SSH address as host:port.
func (e *RsyncExecutor) formatSSHAddress(sshCfg *SSHConfig) string {
	host := sshCfg.Host
	port := sshCfg.Port

	// Check if host already contains port
	if strings.Contains(host, ":") {
		return host
	}

	if port == 0 {
		port = 22
	}

	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

// ensureRemoteDir creates dirPath (and any missing parents) on the host described by
// cfg via a short-lived SSH session. rsync's receiving side only auto-creates the
// final path component when its parent already exists, so a blank destination root
// (e.g. an empty target container) needs this explicit "mkdir -p" beforehand.
func (e *RsyncExecutor) ensureRemoteDir(cfg *SSHConfig, dirPath string) error {
	keyData, err := e.loadPrivateKeyData(cfg)
	if err != nil {
		return fmt.Errorf("failed to load private key for mkdir: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("failed to parse private key for mkdir: %w", err)
	}

	client, err := ssh.Dial("tcp", e.formatSSHAddress(cfg), &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Implement host key verification when system-level key management is available
	})
	if err != nil {
		return fmt.Errorf("failed to connect for mkdir: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to open session for mkdir: %w", err)
	}
	defer session.Close()

	cmd := fmt.Sprintf("mkdir -p %s", shellQuote(dirPath))
	if output, err := session.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("remote mkdir -p failed: %w\nOutput: %s", err, string(output))
	}
	return nil
}

// buildRemoteRsyncCommand constructs the rsync command executed on the source server.
func (e *RsyncExecutor) buildRemoteRsyncCommand(source, destination DataLocation, dstSSH *SSHConfig) (string, error) {
	var parts []string
	// --stats as in buildLocalRsyncArgs: the transferred-bytes line comes back on
	// the session output this executor already reads.
	parts = append(parts, "rsync", "-avz", "--stats", shellQuote(rsyncOutFormat))

	if e.DeleteExtraneous {
		parts = append(parts, "--delete")
	}
	if e.DryRun {
		parts = append(parts, "--dry-run")
	}
	if e.Verbose {
		parts = append(parts, "-v")
	}

	// SSH options for destination connection
	sshOpts := e.buildRemoteSSHOptions(dstSSH)
	if sshOpts != "" {
		parts = append(parts, "-e", fmt.Sprintf("'%s'", sshOpts))
	}

	// Filter arguments — the command runs in a remote shell, so each flag is
	// single-quoted to protect glob characters from shell expansion.
	filterArgs, err := rsyncFilterArgs(source.Filter)
	if err != nil {
		return "", err
	}
	for _, a := range filterArgs {
		parts = append(parts, shellQuote(a))
	}

	// Additional arguments
	parts = append(parts, e.AdditionalArgs...)

	// Source path (local on source server)
	srcPath := source.Path
	if !strings.HasSuffix(srcPath, "/") && isDirectoryPath(srcPath) {
		srcPath += "/"
	}
	parts = append(parts, srcPath)

	// Destination path (remote from source server's perspective)
	dstUser := dstSSH.Username
	if dstUser == "" {
		dstUser = "root"
	}
	dstHost := dstSSH.Host
	if idx := strings.Index(dstHost, ":"); idx != -1 {
		dstHost = dstHost[:idx]
	}
	dstPath := destination.Path
	parts = append(parts, fmt.Sprintf("%s@%s:%s", dstUser, dstHost, dstPath))

	return strings.Join(parts, " "), nil
}

// buildRemoteSSHOptions constructs SSH options for rsync's -e flag.
// No identity file is needed; the forwarded agent handles authentication.
func (e *RsyncExecutor) buildRemoteSSHOptions(dstSSH *SSHConfig) string {
	parts := []string{"ssh"}

	// Port
	port := dstSSH.Port
	if port == 0 {
		if idx := strings.Index(dstSSH.Host, ":"); idx != -1 {
			fmt.Sscanf(dstSSH.Host[idx+1:], "%d", &port)
		}
	}
	if port > 0 && port != 22 {
		parts = append(parts, fmt.Sprintf("-p %d", port))
	}

	// [Note] No -i option needed: SSH Agent Forwarding provides authentication
	// The forwarded agent from the host will authenticate to destination

	// Disable strict host key checking
	parts = append(parts, "-o StrictHostKeyChecking=no")
	parts = append(parts, "-o UserKnownHostsFile=/dev/null")

	return strings.Join(parts, " ")
}

// ============================================================================
// Common Helper Functions
// ============================================================================

// rsyncStatsLine is the line --stats prints for what actually crossed the wire,
// as opposed to "Total file size", which is the size of everything considered.
const rsyncStatsLine = "Total transferred file size:"

// parseRsyncStats returns the bytes rsync --stats reported transferring, or -1
// when the output does not carry the line.
//
// The output is already captured for the error message, so reading it costs
// nothing. A format that does not match is reported as unknown rather than as
// zero: rsync moved the data either way, and only the count is missing.
func parseRsyncStats(output string) int64 {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), rsyncStatsLine) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), rsyncStatsLine))
		if len(fields) == 0 {
			return -1
		}
		var n int64
		if _, err := fmt.Sscanf(strings.ReplaceAll(fields[0], ",", ""), "%d", &n); err != nil {
			return -1
		}
		return n
	}
	return -1
}

// buildLocalRsyncArgs constructs rsync command arguments for local execution.
func (e *RsyncExecutor) buildLocalRsyncArgs(source, destination DataLocation) ([]string, error) {
	// --stats for the transferred-bytes line. rsync prints it on the output this
	// executor already captures, so it is the transfer's own figure rather than
	// one assembled by walking the tree again.
	args := []string{"-avz", "--stats", rsyncOutFormat} // archive, verbose, compress

	if e.DeleteExtraneous {
		args = append(args, "--delete")
	}
	if e.DryRun {
		args = append(args, "--dry-run")
	}
	if e.Verbose {
		args = append(args, "-v")
	}

	// SSH options
	sshCmd := e.buildSSHCommand(source, destination)
	if sshCmd != "" {
		args = append(args, "-e", sshCmd)
	}

	// Filter options
	srcFilter, err := rsyncFilterArgs(source.Filter)
	if err != nil {
		return nil, err
	}
	args = append(args, srcFilter...)
	dstFilter, err := rsyncFilterArgs(destination.Filter)
	if err != nil {
		return nil, err
	}
	args = append(args, dstFilter...)

	// Additional arguments
	args = append(args, e.AdditionalArgs...)

	// Source and destination paths
	args = append(args, e.buildPath(source))
	args = append(args, e.buildPath(destination))

	return args, nil
}

// buildPath returns the rsync path (user@host:path for SSH, or local path).
func (e *RsyncExecutor) buildPath(loc DataLocation) string {
	path := loc.Path

	// Ensure trailing slash for directories
	if !strings.HasSuffix(path, "/") && isDirectoryPath(path) {
		path += "/"
	}

	// Remote path (SSH)
	if loc.Filesystem != nil && loc.Filesystem.SSH != nil {
		cfg := loc.Filesystem.SSH
		user := cfg.Username
		if user == "" {
			user = "root"
		}

		host := cfg.Host
		if idx := strings.Index(host, ":"); idx != -1 {
			host = host[:idx] // Port is handled in -e option
		}
		return fmt.Sprintf("%s@%s:%s", user, host, path)
	}

	// Local path
	return path
}

// buildSSHCommand constructs the SSH command for rsync's -e option.
func (e *RsyncExecutor) buildSSHCommand(source, destination DataLocation) string {
	var cfg *SSHConfig
	switch e.Mode {
	case TransferModePull:
		// Pull: remote source → local, use source SSH config
		if source.Filesystem != nil {
			cfg = source.Filesystem.SSH
		}
	case TransferModePush:
		// Push: local → remote destination, use destination SSH config
		if destination.Filesystem != nil {
			cfg = destination.Filesystem.SSH
		}
	}

	if cfg == nil {
		return ""
	}

	parts := []string{"ssh"}

	port := cfg.Port
	if port == 0 {
		// Extract port from host if specified
		if idx := strings.Index(cfg.Host, ":"); idx != -1 {
			fmt.Sscanf(cfg.Host[idx+1:], "%d", &port)
		}
	}
	if port > 0 && port != 22 {
		parts = append(parts, fmt.Sprintf("-p %d", port))
	}

	// Priority: PrivateKey > PrivateKeyPath
	if cfg.PrivateKey != "" {
		// Create temporary file for PrivateKey content
		tempPath, err := e.createTempKeyFile(cfg.PrivateKey)
		if err == nil {
			parts = append(parts, fmt.Sprintf("-i %s", tempPath))
		}
	} else if cfg.PrivateKeyPath != "" {
		parts = append(parts, fmt.Sprintf("-i %s", cfg.PrivateKeyPath))
	}

	// Disable strict host key checking for automation
	parts = append(parts, "-o StrictHostKeyChecking=no", "-o UserKnownHostsFile=/dev/null")

	// Fail instead of waiting: rsync runs unattended, so a rejected key must not
	// drop ssh into an interactive password prompt, where it would block forever.
	parts = append(parts, "-o BatchMode=yes", "-o ConnectTimeout=10")

	return strings.Join(parts, " ")
}

// RsyncFilterArgs returns the native rsync arguments a filter option translates
// to, or an error when a rule cannot be expressed as one.
//
// Exported so a caller can put the translation and PathFilterOption.MatchPath
// side by side and check that they agree — the two halves of one contract, one
// read by rsync and one by Go. Nothing in a transfer needs it.
func RsyncFilterArgs(opt *FilterOption) ([]string, error) {
	return rsyncFilterArgs(opt)
}

// rsyncFilterArgs translates a filter Option into native rsync arguments — the
// (a) "native subset" strategy. The pipeline is translated in order via
// RsyncTranslator; a matcher that cannot be expressed as native rsync flags
// (e.g. custom types) returns an error, since rsync runs the transfer and
// cannot evaluate arbitrary Go predicates.
//
// The rules come from EffectiveRules, the same normalised list Option.Match
// evaluates — including the traversal rule it inserts before a catch-all. That
// is what keeps a filesystem transfer and the Go matcher judging it in step:
// there is one pipeline, read twice, rather than two lists built side by side.
// Building the order here independently is what let the simple Include/Exclude
// form come out reversed against the matcher.
//
// Returned args are unquoted, suitable for exec.Command. For a remote shell
// command the caller must quote each arg (see buildRemoteRsyncCommand).
func rsyncFilterArgs(opt *FilterOption) ([]string, error) {
	if opt == nil {
		return nil, nil
	}
	var args []string
	for _, r := range opt.EffectiveRules() {
		t, ok := r.Matcher.(filter.RsyncTranslator)
		if !ok {
			typ := "<nil>"
			if r.Matcher != nil {
				typ = r.Matcher.Type()
			}
			return nil, fmt.Errorf("rsync transfer does not support filter type %q; use it with inspect/object-storage or drop it", typ)
		}
		a, err := t.RsyncArgs(r.Action)
		if err != nil {
			return nil, fmt.Errorf("rsync filter: %w", err)
		}
		args = append(args, a...)
	}
	return args, nil
}

// isDirectoryPath returns true if path appears to be a directory (no file extension).
func isDirectoryPath(path string) bool {
	// Heuristic: paths without extension are likely directories
	base := path
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		base = path[idx+1:]
	}
	return !strings.Contains(base, ".")
}

// createTempKeyFile writes PrivateKey content to a temporary file with secure permissions.
// Returns the temporary file path. Caller must call cleanupTempKeyFile() after use.
// Uses transx-specific directory under system temp for isolation and easy cleanup.
func (e *RsyncExecutor) createTempKeyFile(privateKey string) (string, error) {
	// Use transx-specific directory under system temp (cross-platform)
	transxTempDir := filepath.Join(os.TempDir(), "transx")

	// Create directory with secure permissions (owner only)
	if err := os.MkdirAll(transxTempDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create transx temp directory: %w", err)
	}

	// Pattern includes PID for debugging; CreateTemp adds random suffix for uniqueness
	pattern := fmt.Sprintf("key-%d-*", os.Getpid())
	tmpFile, err := os.CreateTemp(transxTempDir, pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp key file: %w", err)
	}

	// Set secure permissions (owner read-only)
	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to set temp key file permissions: %w", err)
	}

	// Write normalized private key content. OpenSSH rejects a PEM file whose
	// final line has no terminating newline ("error in libcrypto") and then
	// falls back to another auth method, so restore it if the caller trimmed it.
	normalizedKey := normalizePrivateKey(privateKey)
	if !strings.HasSuffix(normalizedKey, "\n") {
		normalizedKey += "\n"
	}
	if _, err := tmpFile.WriteString(normalizedKey); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp key file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to close temp key file: %w", err)
	}

	e.tempKeyFile = tmpFile.Name()
	return e.tempKeyFile, nil
}

// cleanupTempKeyFile removes the temporary key file if it exists.
func (e *RsyncExecutor) cleanupTempKeyFile() {
	if e.tempKeyFile != "" {
		os.Remove(e.tempKeyFile)
		e.tempKeyFile = ""
	}
}
