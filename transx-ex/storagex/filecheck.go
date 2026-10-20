package storagex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// =============================================================================
// File listing and hashing — for checking a transfer file by file
// =============================================================================

// FileInfo is one regular file of a tree ListFiles read: its path under the
// root, "/"-separated, its size, and its modification time in whole seconds —
// rsync and SFTP carry the time across, but not every filesystem keeps the
// fraction.
type FileInfo struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

// ListFiles reads every regular file under loc.Path, sorted by path. Links and
// other special files are not listed.
//
// It is the per-file counterpart of InspectFilesystem, whose folders carry only
// totals: what a check that a transfer arrived intact compares. A remote tree is
// read by one find for the whole tree, so its size costs seconds, not a round
// trip per folder. loc.Filter is not applied: a check needs the files the filter
// left behind as much as the ones it let through.
func ListFiles(ctx context.Context, loc DataLocation) ([]FileInfo, error) {
	cfg, err := fileCheckSSH(loc)
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	add := func(f FileInfo) { files = append(files, f) }
	if cfg == nil {
		err = listLocalTree(ctx, loc.Path, add)
	} else {
		err = listRemoteTree(ctx, cfg, loc.Path, add)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// HashFiles returns the SHA-256, lower-case hex, of each of paths — relative to
// loc.Path, as ListFiles gives them — keyed by path. A remote host hashes as
// many files at once as it has cores, and only the files named are read.
//
// onFile, when non-nil, is called once for every file hashed, as its hash
// arrives. A file that can no longer be read fails the call.
func HashFiles(ctx context.Context, loc DataLocation, paths []string, onFile func(path string)) (map[string]string, error) {
	cfg, err := fileCheckSSH(loc)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return hashes, nil
	}
	add := func(path, hash string) {
		hashes[path] = hash
		if onFile != nil {
			onFile(path)
		}
	}
	if cfg == nil {
		err = hashLocalTree(ctx, loc.Path, paths, add)
	} else {
		err = hashRemoteTree(ctx, cfg, loc.Path, paths, add)
	}
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

// fileCheckSSH returns the SSH host loc names, or nil for this machine.
func fileCheckSSH(loc DataLocation) (*SSHConfig, error) {
	if loc.StorageType != "" && loc.StorageType != StorageTypeFilesystem {
		return nil, fmt.Errorf("file listing needs a filesystem location, got %q", loc.StorageType)
	}
	if loc.Filesystem == nil {
		return nil, fmt.Errorf("filesystem access config required")
	}
	switch loc.Filesystem.AccessType {
	case AccessTypeLocal:
		return nil, nil
	case AccessTypeSSH:
		if loc.Filesystem.SSH == nil {
			return nil, fmt.Errorf("SSH config required for ssh access")
		}
		return loc.Filesystem.SSH, nil
	default:
		return nil, fmt.Errorf("unsupported filesystem access type: %s", loc.Filesystem.AccessType)
	}
}

// -----------------------------------------------------------------------------
// This machine
// -----------------------------------------------------------------------------

// listLocalTree walks root on this machine. A directory that cannot be read
// fails the listing, as find's does on a remote host.
func listLocalTree(ctx context.Context, root string, add func(FileInfo)) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		add(FileInfo{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime().Unix()})
		return nil
	})
}

// hashLocalTree hashes paths under root on this machine, a file per core at a
// time. add is called from this goroutine only.
func hashLocalTree(ctx context.Context, root string, paths []string, add func(path, hash string)) error {
	type result struct {
		path, hash string
		err        error
	}
	jobs := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				h, err := hashLocalFile(filepath.Join(root, filepath.FromSlash(p)))
				results <- result{p, h, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range paths {
			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("hash %s: %w", r.path, r.err)
			}
			continue
		}
		add(r.path, r.hash)
	}
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func hashLocalFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// -----------------------------------------------------------------------------
// An SSH host
// -----------------------------------------------------------------------------

// listRemoteTree reads the tree at root on the host cfg describes.
//
// One find for the whole tree. NUL-delimited, and the path printed last, so
// neither a newline nor a tab in a file name can shift the parse. -printf is
// GNU find, which the Linux hosts a migration reaches have.
func listRemoteTree(ctx context.Context, cfg *SSHConfig, root string, add func(FileInfo)) error {
	cmd := fmt.Sprintf("find %s -type f -printf '%%s\\t%%T@\\t%%P\\0'", shellQuote(root))
	return runSSHStream(ctx, cfg, cmd, nil, 0, func(record string) {
		if f, ok := parseFileRecord(record); ok {
			add(f)
		}
	})
}

// parseFileRecord parses one "<size>\t<mtime>\t<path>" record of the listing.
// The time may carry a fraction, which is dropped.
func parseFileRecord(record string) (FileInfo, bool) {
	size, rest, ok := strings.Cut(record, "\t")
	if !ok {
		return FileInfo{}, false
	}
	mtime, relPath, ok := strings.Cut(rest, "\t")
	if !ok || relPath == "" {
		return FileInfo{}, false
	}
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return FileInfo{}, false
	}
	sec, _, _ := strings.Cut(mtime, ".")
	t, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return FileInfo{}, false
	}
	return FileInfo{Path: relPath, Size: n, ModTime: t}, true
}

// hashRemoteTree hashes paths under root on the host cfg describes.
//
// The paths travel on stdin, NUL-delimited, so only the files that need a hash
// are read, and no command line grows with their number.
func hashRemoteTree(ctx context.Context, cfg *SSHConfig, root string, paths []string, add func(path, hash string)) error {
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}
	cmd := fmt.Sprintf(`cd %s && xargs -0 -r -P "$(nproc 2>/dev/null || echo 4)" -n 64 sha256sum --`,
		shellQuote(root))
	return runSSHStream(ctx, cfg, cmd, &in, '\n', func(line string) {
		if hash, relPath, ok := parseSHA256Line(line); ok {
			add(relPath, hash)
		}
	})
}

// parseSHA256Line splits one line of sha256sum output, "<hash>  <path>".
//
// sha256sum escapes a name holding a backslash, newline or carriage return and
// leads that line with a backslash; the escape is undone so the path matches
// the one find listed.
func parseSHA256Line(line string) (hash, path string, ok bool) {
	escaped := strings.HasPrefix(line, `\`)
	if escaped {
		line = line[1:]
	}
	hash, path, ok = strings.Cut(line, "  ")
	if !ok || hash == "" || path == "" {
		return "", "", false
	}
	if escaped {
		path = unescapeSHA256Name(path)
	}
	return hash, path, true
}

// unescapeSHA256Name reverses sha256sum's escaping of a file name.
func unescapeSHA256Name(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

const (
	// sshKeepaliveInterval is how often runSSHStream checks that the host is
	// still there, and sshKeepaliveMaxMissed how many checks in a row may go
	// unanswered before the connection is given up on.
	sshKeepaliveInterval  = 30 * time.Second
	sshKeepaliveMaxMissed = 3
	// sshStderrLimit is how much of a command's stderr is kept for its error.
	sshStderrLimit = 4 << 10
)

// runSSHStream runs command, as the remote shell takes it, on the host cfg
// describes and hands its stdout to onRecord one record at a time, split at
// delim, as the records arrive — so a caller can follow a command that runs for
// minutes instead of waiting for all of it. stdin, when non-nil, is fed to the
// command. onRecord is called from this goroutine only.
//
// The connection is watched. Hashing 50 GB says nothing for minutes, and a peer
// that went away in the meantime would otherwise leave this waiting for good. A
// keepalive goes out every sshKeepaliveInterval, and sshKeepaliveMaxMissed
// unanswered in a row close the connection, which ends the command with an
// error. ctx ends it the same way.
func runSSHStream(
	ctx context.Context,
	cfg *SSHConfig,
	command string,
	stdin io.Reader,
	delim byte,
	onRecord func(string),
) error {
	client, err := core.Dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	var lost atomic.Bool
	stop := make(chan struct{})
	defer close(stop)
	go watchSSH(ctx, client, stop, &lost)

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("SSH new session: %w", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("SSH stdout pipe: %w", err)
	}
	// Kept apart from stdout, which is parsed: a "Permission denied" from find
	// belongs in the error, not in the listing.
	stderr := &tailWriter{limit: sshStderrLimit}
	session.Stderr = stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	if err := session.Start(command); err != nil {
		return fmt.Errorf("SSH command start on %s: %w", addr, err)
	}
	r := bufio.NewReaderSize(stdout, 64<<10)
	for {
		record, readErr := r.ReadString(delim)
		if record = strings.TrimSuffix(record, string(delim)); record != "" {
			onRecord(record)
		}
		if readErr != nil {
			break
		}
	}
	err = session.Wait()

	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case lost.Load():
		return fmt.Errorf("SSH connection to %s lost: %d keepalives went unanswered",
			addr, sshKeepaliveMaxMissed)
	case err != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("SSH command failed on %s: %w: %s", addr, err, msg)
		}
		return fmt.Errorf("SSH command failed on %s: %w", addr, err)
	}
	return nil
}

// watchSSH sends keepalives on client until stop closes, and closes the client
// when ctx ends or the host stops answering — setting lost in the second case,
// so the caller can say why its command ended.
func watchSSH(ctx context.Context, client *ssh.Client, stop <-chan struct{}, lost *atomic.Bool) {
	ticker := time.NewTicker(sshKeepaliveInterval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			client.Close()
			return
		case <-ticker.C:
		}

		// Any reply proves the peer alive, a refusal included: OpenSSH answers
		// a global request it does not know with a failure, and that is all this
		// asks for.
		reply := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			reply <- err
		}()
		select {
		case <-stop:
			return
		case <-ctx.Done():
			client.Close()
			return
		case err := <-reply:
			if err != nil {
				missed++
			} else {
				missed = 0
			}
		case <-time.After(sshKeepaliveInterval):
			missed++
		}
		if missed >= sshKeepaliveMaxMissed {
			lost.Store(true)
			client.Close()
			return
		}
	}
}

// tailWriter keeps the last limit bytes written to it.
type tailWriter struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = w.buf[len(w.buf)-w.limit:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
