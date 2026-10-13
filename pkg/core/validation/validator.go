package validation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/core/migration"
	"github.com/cloud-barista/cm-centipede/transx-ex"
)

// ValidateFileSystem checks that each dst folder in m contains exactly the same files
// as the corresponding src folder. opt.Mode decides what "the same" means: size
// and modification time (ModeQuick), or size and SHA256 (ModeChecksum).
// Returns one ValidationDetailItem per mismatch (missing file, extra file, or a
// size, time or hash diff). Connection errors are reported as a failed item for
// the folder root.
//
// A folder that landed in object storage (cross-storage) is compared by path
// and size only, whatever opt.Mode says — see validateCrossStorage.
func ValidateFileSystem(ctx context.Context, m targetmodel.MigrationFileSystemModel, opt Options) ([]model.ValidationDetailItem, error) {
	cross := m.DstType == commonmodel.StorageTypeObjectStorage

	srcSSH, err := resolveSSHConfig(m.SrcConnection)
	if err != nil {
		return nil, fmt.Errorf("resolve src connection: %w", err)
	}
	var dstSSH *transxex.SSHConfig
	if !cross {
		if dstSSH, err = resolveSSHConfig(m.DstConnection); err != nil {
			return nil, fmt.Errorf("resolve dst connection: %w", err)
		}
	}

	var details []model.ValidationDetailItem
	for i, folder := range m.Folders {
		fopt := opt.within(itemLabel(i, len(m.Folders), folder.SrcPath))
		var folderDetails []model.ValidationDetailItem
		if cross {
			folderDetails, err = validateCrossStorage(
				func() (map[string]int64, error) { return folderSizes(ctx, srcSSH, folder.SrcPath) },
				func() (map[string]int64, error) { return bucketSizes(m.DstConnection, folder.DstPath) },
				folder.SrcPath, folder.DstPath, folder.Rules, "file", "object", fopt)
		} else {
			folderDetails, err = validateSSHFolder(ctx, srcSSH, dstSSH, folder.SrcPath, folder.DstPath, folder.Rules, fopt)
		}
		if err != nil {
			details = append(details, model.ValidationDetailItem{
				ItemPath: folder.SrcPath,
				Status:   "failed",
				Message:  fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		details = append(details, folderDetails...)
	}
	return details, nil
}

// validateSSHFolder reads both folders and returns a failed ValidationDetailItem
// for each discrepancy.
//
// Both ends are listed first — size, modification time and path, one find per
// host, seconds for any number of files — and both at once. In ModeChecksum the
// files are then hashed, again on both hosts at once and with every core each
// has, but only those whose hash can still decide something: a file the filter
// dropped, one missing from the destination or one already differing in size
// is reported from the listing alone.
//
// rules are the filter the migration applied to this folder. They change the
// source direction only: a file the filter dropped is absent from the
// destination on purpose, so it is not a miss. The destination direction keeps
// reading the WHOLE source listing — see the three outcomes in compareFileTrees.
func validateSSHFolder(
	ctx context.Context,
	srcCfg, dstCfg *transxex.SSHConfig,
	srcPath, dstPath string,
	rules []commonmodel.PathFilterRule,
	opt Options,
) ([]model.ValidationDetailItem, error) {
	flt, err := newMigrationFilter(rules)
	if err != nil {
		return nil, fmt.Errorf("build migration filter: %w", err)
	}
	mode := opt.mode()

	opt.report(mode + " · listing files")
	var srcFiles, dstFiles map[string]remoteFile
	err = bothSides(
		func() (err error) {
			if srcFiles, err = listFiles(ctx, srcCfg, srcPath); err != nil {
				return fmt.Errorf("list src files %s: %w", srcPath, err)
			}
			return nil
		},
		func() (err error) {
			if dstFiles, err = listFiles(ctx, dstCfg, dstPath); err != nil {
				return fmt.Errorf("list dst files %s: %w", dstPath, err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	if mode == ModeChecksum {
		paths := pathsToHash(srcFiles, dstFiles, flt)
		progress := newSideProgress(opt, mode+" · hashing", len(paths), "files")
		var srcHashes, dstHashes map[string]string
		err = bothSides(
			func() (err error) {
				srcHashes, err = hashFiles(ctx, srcCfg, srcPath, paths, func() { progress.advance(true) })
				if err != nil {
					return fmt.Errorf("compute src checksums %s: %w", srcPath, err)
				}
				return nil
			},
			func() (err error) {
				dstHashes, err = hashFiles(ctx, dstCfg, dstPath, paths, func() { progress.advance(false) })
				if err != nil {
					return fmt.Errorf("compute dst checksums %s: %w", dstPath, err)
				}
				return nil
			},
		)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			s, d := srcFiles[p], dstFiles[p]
			s.hash, d.hash = srcHashes[p], dstHashes[p]
			srcFiles[p], dstFiles[p] = s, d
		}
	}

	return compareFileTrees(srcFiles, dstFiles, flt, srcPath, dstPath, mode), nil
}

// pathsToHash lists the files whose hashes compareFileTrees will compare: the
// ones the migration carried that the destination also holds at the same size.
// Every other source file is already decided by the listing.
func pathsToHash(srcFiles, dstFiles map[string]remoteFile, flt *migrationFilter) []string {
	var paths []string
	for relPath, src := range srcFiles {
		if flt.excludesFile(relPath, src.size) {
			continue
		}
		if dst, ok := dstFiles[relPath]; ok && dst.size == src.size {
			paths = append(paths, relPath)
		}
	}
	sort.Strings(paths)
	return paths
}

// compareFileTrees is validateSSHFolder's verdict, with the remote reads
// already done. Separated so the comparison — which is where the filter changes
// what is reported — can be exercised without a host to SSH into.
func compareFileTrees(
	srcFiles, dstFiles map[string]remoteFile,
	flt *migrationFilter,
	srcPath, dstPath string,
	mode string,
) []model.ValidationDetailItem {
	found := newFindings(srcPath)

	// Check every src file against dst.
	srcPaths := make([]string, 0, len(srcFiles))
	for p := range srcFiles {
		srcPaths = append(srcPaths, p)
	}
	sort.Strings(srcPaths)

	excluded := make(map[string]bool)
	for _, relPath := range srcPaths {
		src := srcFiles[relPath]
		if flt.excludesFile(relPath, src.size) {
			excluded[relPath] = true
			continue
		}
		dst, ok := dstFiles[relPath]
		switch {
		case !ok:
			found.fail(srcPath+"/"+relPath, "file missing in destination")
		case src.size != dst.size:
			found.fail(srcPath+"/"+relPath,
				fmt.Sprintf("size differs: src=%d dst=%d bytes", src.size, dst.size))
		case mode == ModeQuick && src.mtime != dst.mtime:
			// Whole seconds: rsync -a carries the time across, but not every
			// filesystem keeps the fraction.
			found.fail(srcPath+"/"+relPath,
				fmt.Sprintf("modification time differs: src=%s dst=%s",
					time.Unix(src.mtime, 0).UTC().Format(time.RFC3339),
					time.Unix(dst.mtime, 0).UTC().Format(time.RFC3339)))
		case mode == ModeChecksum && (src.hash == "" || dst.hash == ""):
			// Listed but not hashed: the file went away between the two reads.
			found.fail(srcPath+"/"+relPath, "checksum unavailable: file changed during validation")
		case mode == ModeChecksum && src.hash != dst.hash:
			found.fail(srcPath+"/"+relPath,
				fmt.Sprintf("checksum mismatch: src=%s dst=%s", src.hash, dst.hash))
		}
	}

	// Check the destination against the FULL source listing, filter included.
	// Three outcomes, and the filter decides between the last two rather than
	// selecting what is looked up:
	//
	//	in source, not excluded  — the transfer put it there. Nothing to report.
	//	not in source at all     — failed, as before.
	//	in source but excluded   — warning. Saying "exists in destination but not
	//	                           in source" would be false: it is in the source,
	//	                           it was only not meant to travel. Unlike a DBMS
	//	                           target, a filesystem target is never verified
	//	                           empty (nothing here matches dbms.go's
	//	                           RequireEmpty) and rsync merges into whatever it
	//	                           finds, so a leftover from an earlier run reaches
	//	                           this line honestly — which is why it is a
	//	                           warning and not a failure of this migration.
	dstPaths := make([]string, 0, len(dstFiles))
	for p := range dstFiles {
		dstPaths = append(dstPaths, p)
	}
	sort.Strings(dstPaths)

	for _, relPath := range dstPaths {
		if _, ok := srcFiles[relPath]; !ok {
			found.fail(dstPath+"/"+relPath, "file exists in destination but not in source")
			continue
		}
		if excluded[relPath] {
			found.notice("file(s) excluded by the migration filter"+
				" but present in the destination", relPath)
		}
	}

	return found.summarise(len(excluded), "file(s) excluded by the migration filter")
}

// remoteFile is one regular file as both ends are read back: its size and
// modification time (whole seconds) from the listing, and its content hash
// when ModeChecksum hashed it.
type remoteFile struct {
	size  int64
	mtime int64
	hash  string
}

// listFiles reads every regular file under root on the host described by cfg,
// keyed by its path relative to root.
//
// One find for the whole tree. NUL-delimited, and the path printed last, so
// neither a newline nor a tab in a file name can shift the parse. -printf is
// GNU find, which the hosts this reaches already were assumed to have.
func listFiles(ctx context.Context, cfg *transxex.SSHConfig, root string) (map[string]remoteFile, error) {
	cmd := fmt.Sprintf("find %s -type f -printf '%%s\\t%%T@\\t%%P\\0'", shellQuote(root))
	files := make(map[string]remoteFile)
	err := runSSHStream(ctx, cfg, cmd, nil, 0, func(record string) {
		size, rest, ok := strings.Cut(record, "\t")
		if !ok {
			return
		}
		mtime, relPath, ok := strings.Cut(rest, "\t")
		if !ok || relPath == "" {
			return
		}
		n, err := strconv.ParseInt(size, 10, 64)
		if err != nil {
			return
		}
		sec, _, _ := strings.Cut(mtime, ".")
		t, err := strconv.ParseInt(sec, 10, 64)
		if err != nil {
			return
		}
		files[relPath] = remoteFile{size: n, mtime: t}
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// hashFiles returns the SHA256 of each of paths, relative to root, on the host
// described by cfg — as many files at once as the host has cores.
//
// The paths travel on stdin, NUL-delimited, so only the files that need a hash
// are read, and no command line grows with their number. onFile is called once
// for every file hashed, as its line arrives.
func hashFiles(ctx context.Context, cfg *transxex.SSHConfig, root string, paths []string, onFile func()) (map[string]string, error) {
	hashes := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return hashes, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}
	cmd := fmt.Sprintf(`cd %s && xargs -0 -r -P "$(nproc 2>/dev/null || echo 4)" -n 64 sha256sum --`,
		shellQuote(root))
	err := runSSHStream(ctx, cfg, cmd, &in, '\n', func(line string) {
		hash, relPath, ok := parseSHA256Line(line)
		if !ok {
			return
		}
		hashes[relPath] = hash
		onFile()
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
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

// runSSHStream runs command on the host described by cfg and hands its stdout
// to onRecord one record at a time, split at delim, as the records arrive — so
// a caller can follow a command that runs for minutes instead of waiting for
// all of it. stdin, when non-nil, is fed to the command. onRecord is called from
// this goroutine only.
//
// The connection is watched. A command reading 50 GB says nothing for minutes,
// and a peer that went away in the meantime would otherwise leave this waiting
// for good, with the validation reading "running" until the server restarts. A
// keepalive goes out every sshKeepaliveInterval, and sshKeepaliveMaxMissed
// unanswered in a row close the connection, which ends the command with an
// error. ctx ends it the same way.
func runSSHStream(
	ctx context.Context,
	cfg *transxex.SSHConfig,
	command string,
	stdin io.Reader,
	delim byte,
	onRecord func(string),
) error {
	client, addr, err := dialSSH(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

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

// dialSSH opens an SSH connection to the host described by cfg.
func dialSSH(cfg *transxex.SSHConfig) (*ssh.Client, string, error) {
	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("build SSH auth: %w", err)
	}
	if len(authMethods) == 0 {
		return nil, "", fmt.Errorf("no SSH authentication method available for %s", cfg.Host)
	}

	timeout := 30 * time.Second
	if cfg.ConnectTimeout > 0 {
		timeout = time.Duration(cfg.ConnectTimeout) * time.Second
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec — host key verification is handled at infrastructure level
		Timeout:         timeout,
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, "", fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	return client, addr, nil
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

// buildAuthMethods constructs SSH auth methods from cfg.
// Priority: PrivateKey string → PrivateKeyPath file → SSH agent.
func buildAuthMethods(cfg *transxex.SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if strings.TrimSpace(cfg.PrivateKey) != "" {
		key := normalizePrivateKey(cfg.PrivateKey)
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if len(methods) == 0 && strings.TrimSpace(cfg.PrivateKeyPath) != "" {
		keyBytes, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key file %s: %w", cfg.PrivateKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key file: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if len(methods) == 0 || cfg.UseAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			conn, err := net.Dial("unix", sock)
			if err == nil {
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		}
	}

	return methods, nil
}

// resolveSSHConfig maps a ConnectionRef to a transxex.SSHConfig.
// It delegates to the migration package so validation reaches a connection
// exactly the way the migration did: a source the executor accepts must not be
// rejected afterwards by the check that it worked.
func resolveSSHConfig(ref commonmodel.ConnectionRef) (*transxex.SSHConfig, error) {
	return migration.ResolveSSHConfig(ref)
}

// normalizePrivateKey converts escaped newline sequences to real newlines.
// Handles keys stored as single-line strings in YAML or environment variables.
func normalizePrivateKey(key string) string {
	if strings.Contains(key, "\n") && !strings.Contains(key, "\\n") {
		return key
	}
	return strings.ReplaceAll(key, "\\n", "\n")
}

// shellQuote wraps s in single quotes for safe shell interpolation,
// escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ── Object Storage Validation ─────────────────────────────────────────────────

// ValidateObjectStorage checks that each dst bucket in m contains exactly the same
// objects as the corresponding src bucket, with identical ETags.
// Returns one ValidationDetailItem per mismatch (missing object, extra object, or ETag diff).
// Connection errors are reported as a failed item for the bucket path.
//
// Objects are compared from their listings alone — nothing is downloaded — so
// opt.Mode does not apply here.
func ValidateObjectStorage(ctx context.Context, m targetmodel.MigrationObjectStorageModel, opt Options) ([]model.ValidationDetailItem, error) {
	// A bucket that landed on a file system (cross-storage) is compared by path
	// and size only — see validateCrossStorage.
	cross := m.DstType == commonmodel.StorageTypeFilesystem
	var dstSSH *transxex.SSHConfig
	if cross {
		var err error
		if dstSSH, err = resolveSSHConfig(m.DstConnection); err != nil {
			return nil, fmt.Errorf("resolve dst connection: %w", err)
		}
	}

	var details []model.ValidationDetailItem
	for i, bucket := range m.Buckets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bopt := opt.within(itemLabel(i, len(m.Buckets), bucket.SrcPath))
		var bucketDetails []model.ValidationDetailItem
		var err error
		if cross {
			bucketDetails, err = validateCrossStorage(
				func() (map[string]int64, error) { return bucketSizes(m.SrcConnection, bucket.SrcPath) },
				func() (map[string]int64, error) { return folderSizes(ctx, dstSSH, bucket.DstPath) },
				bucket.SrcPath, bucket.DstPath, bucket.Rules, "object", "file", bopt)
		} else {
			bucketDetails, err = validateObjectBucket(m.SrcConnection, m.DstConnection, bucket.SrcPath, bucket.DstPath, bucket.Rules, bopt)
		}
		if err != nil {
			details = append(details, model.ValidationDetailItem{
				ItemPath: bucket.SrcPath,
				Status:   "failed",
				Message:  fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		details = append(details, bucketDetails...)
	}
	return details, nil
}

// validateObjectBucket compares object ETags between src and dst for a single bucket pair.
//
// rules are the filter the migration applied to this bucket, read the same way
// validateSSHFolder reads a folder's: excluded objects are not misses on the way
// out, and an excluded object found in the destination is a warning rather than
// the false claim that the source does not have it.
func validateObjectBucket(srcRef, dstRef commonmodel.ConnectionRef, srcPath, dstPath string, rules []commonmodel.PathFilterRule, opt Options) ([]model.ValidationDetailItem, error) {
	flt, err := newMigrationFilter(rules)
	if err != nil {
		return nil, fmt.Errorf("build migration filter: %w", err)
	}

	srcLoc, err := resolveOSLoc(srcRef, srcPath)
	if err != nil {
		return nil, fmt.Errorf("resolve src location: %w", err)
	}
	dstLoc, err := resolveOSLoc(dstRef, dstPath)
	if err != nil {
		return nil, fmt.Errorf("resolve dst location: %w", err)
	}

	srcProvider, err := transxex.NewS3Provider(srcLoc)
	if err != nil {
		return nil, fmt.Errorf("create src S3 provider: %w", err)
	}
	defer transxex.CloseS3Provider(srcProvider) //nolint:errcheck
	dstProvider, err := transxex.NewS3Provider(dstLoc)
	if err != nil {
		return nil, fmt.Errorf("create dst S3 provider: %w", err)
	}
	defer transxex.CloseS3Provider(dstProvider) //nolint:errcheck

	_, srcPrefix := transxex.ParseBucketAndKey(srcPath)
	_, dstPrefix := transxex.ParseBucketAndKey(dstPath)

	// Both listings at once: they are different endpoints.
	opt.report("listing objects")
	var srcObjects, dstObjects []transxex.ObjectInfo
	err = bothSides(
		func() (err error) {
			if srcObjects, err = srcProvider.ListObjects(srcPrefix); err != nil {
				return fmt.Errorf("list src objects: %w", err)
			}
			return nil
		},
		func() (err error) {
			if dstObjects, err = dstProvider.ListObjects(dstPrefix); err != nil {
				return fmt.Errorf("list dst objects: %w", err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	opt.report(fmt.Sprintf("comparing %s src · %s dst objects", commas(len(srcObjects)), commas(len(dstObjects))))

	srcMap := buildObjectMap(srcObjects, srcPrefix)
	dstMap := buildObjectMap(dstObjects, dstPrefix)

	srcKeys := make([]string, 0, len(srcMap))
	for k := range srcMap {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)

	// Size first, ETag second. Size is already in the listing, so it costs
	// nothing, and a difference in it is decisive on its own. The ETag is the
	// stronger check but only where it means what it appears to mean — see
	// comparableETag — so an object it cannot speak for is reported as
	// size-matched-but-unverified rather than passed or failed.
	found := newFindings(srcPath)
	excluded := make(map[string]bool)
	for _, relKey := range srcKeys {
		src := srcMap[relKey]
		if flt.excludesObject(relKey, src.size) {
			excluded[relKey] = true
			continue
		}
		dst, ok := dstMap[relKey]
		if !ok {
			found.fail(srcPath+"/"+relKey, "object missing in destination")
			continue
		}
		if src.size != dst.size {
			found.fail(srcPath+"/"+relKey,
				fmt.Sprintf("size differs: src=%d dst=%d bytes", src.size, dst.size))
			continue
		}
		srcETag, dstETag := normalizeETag(src.etag), normalizeETag(dst.etag)
		if !comparableETag(srcETag) || !comparableETag(dstETag) {
			found.notice("object(s) matched by size but not content-verified"+
				" (multipart or non-MD5 ETag)", relKey)
			continue
		}
		if srcETag != dstETag {
			found.fail(srcPath+"/"+relKey,
				fmt.Sprintf("etag mismatch: src=%s dst=%s", src.etag, dst.etag))
		}
	}

	dstKeys := make([]string, 0, len(dstMap))
	for k := range dstMap {
		dstKeys = append(dstKeys, k)
	}
	sort.Strings(dstKeys)

	// The same three outcomes as compareFileTrees, against the full source
	// listing: present and expected, absent from the source, or excluded by the
	// filter and therefore already in the destination before this migration ran.
	for _, relKey := range dstKeys {
		if _, ok := srcMap[relKey]; !ok {
			found.fail(dstPath+"/"+relKey, "object exists in destination but not in source")
			continue
		}
		if excluded[relKey] {
			found.notice("object(s) excluded by the migration filter"+
				" but present in the destination", relKey)
		}
	}

	return found.summarise(len(excluded), "object(s) excluded by the migration filter"), nil
}

// remoteObject is one listed object, keyed in the maps below by its prefix-
// stripped key. The full key and the size are kept because the filter judges
// them: executor-s3.go matches on the whole key, not on the relative one.
type remoteObject struct {
	etag string
	size int64
}

// buildObjectMap converts a ListObjects result into a map of relative-key → object.
// prefix is stripped from each key so cross-bucket paths can be compared by relative path.
func buildObjectMap(objects []transxex.ObjectInfo, prefix string) map[string]remoteObject {
	m := make(map[string]remoteObject, len(objects))
	for _, obj := range objects {
		relKey := strings.TrimPrefix(obj.Key, prefix)
		relKey = strings.TrimPrefix(relKey, "/")
		if relKey == "" {
			continue
		}
		m[relKey] = remoteObject{etag: obj.ETag, size: obj.Size}
	}
	return m
}

// resolveOSLoc maps a ConnectionRef to a transxex.StorageLocation for object
// storage validation. It delegates to the migration package, as resolveSSHConfig
// does, so validation resolves a connection exactly the way the migration did
// and covers every source the migration accepts.
func resolveOSLoc(ref commonmodel.ConnectionRef, path string) (transxex.StorageLocation, error) {
	return migration.ResolveObjectStorageLoc(ref, path, nil)
}

// normalizeETag strips surrounding double quotes from an ETag value.
// MinIO and S3-compatible stores often return ETags quoted as `"abc123"`.
func normalizeETag(etag string) string {
	return strings.Trim(etag, `"`)
}

// ── Cross-Storage Validation ──────────────────────────────────────────────────

// validateCrossStorage compares a folder against a bucket prefix, or a bucket
// prefix against a folder: one end a file system, the other object storage.
//
// By path and size only, whatever Options.Mode asks for. The two ends share no
// cheap content fingerprint to set side by side — a file has no ETag, and an
// ETag is an MD5 only for an object uploaded in one part — and a modification
// time does not survive the move into object storage. A listing of each end
// still says what matters most: every file arrived, at its size, and nothing
// else is there.
//
// srcList and dstList each read one end, keyed by path relative to the migrated
// folder or prefix — the key the transfer wrote by and the filter judges.
// srcNoun and dstNoun name what each end holds, for the messages.
func validateCrossStorage(
	srcList, dstList func() (map[string]int64, error),
	srcPath, dstPath string,
	rules []commonmodel.PathFilterRule,
	srcNoun, dstNoun string,
	opt Options,
) ([]model.ValidationDetailItem, error) {
	flt, err := newMigrationFilter(rules)
	if err != nil {
		return nil, fmt.Errorf("build migration filter: %w", err)
	}

	opt.report("cross-storage · listing")
	var src, dst map[string]int64
	err = bothSides(
		func() (err error) {
			if src, err = srcList(); err != nil {
				return fmt.Errorf("list src %ss %s: %w", srcNoun, srcPath, err)
			}
			return nil
		},
		func() (err error) {
			if dst, err = dstList(); err != nil {
				return fmt.Errorf("list dst %ss %s: %w", dstNoun, dstPath, err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	opt.report(fmt.Sprintf("cross-storage · comparing %s %ss · %s %ss",
		commas(len(src)), srcNoun, commas(len(dst)), dstNoun))

	return compareCrossTrees(src, dst, flt, srcPath, dstPath, srcNoun, dstNoun), nil
}

// compareCrossTrees is validateCrossStorage's verdict, with both listings read.
// The outcomes are compareFileTrees' without the time and hash checks: missing,
// a different size, or present in the destination only — a warning rather than
// a failure when the source has it but the filter kept it out.
func compareCrossTrees(
	src, dst map[string]int64,
	flt *migrationFilter,
	srcPath, dstPath string,
	srcNoun, dstNoun string,
) []model.ValidationDetailItem {
	found := newFindings(srcPath)

	srcKeys := make([]string, 0, len(src))
	for k := range src {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)

	excluded := make(map[string]bool)
	for _, rel := range srcKeys {
		size := src[rel]
		if flt.excludesFile(rel, size) {
			excluded[rel] = true
			continue
		}
		dstSize, ok := dst[rel]
		switch {
		case !ok:
			found.fail(joinItem(srcPath, rel), srcNoun+" missing in destination")
		case dstSize != size:
			found.fail(joinItem(srcPath, rel),
				fmt.Sprintf("size differs: src=%d dst=%d bytes", size, dstSize))
		}
	}

	dstKeys := make([]string, 0, len(dst))
	for k := range dst {
		dstKeys = append(dstKeys, k)
	}
	sort.Strings(dstKeys)

	for _, rel := range dstKeys {
		if _, ok := src[rel]; !ok {
			found.fail(joinItem(dstPath, rel), dstNoun+" exists in destination but not in source")
			continue
		}
		if excluded[rel] {
			found.notice(srcNoun+"(s) excluded by the migration filter"+
				" but present in the destination", rel)
		}
	}

	return found.summarise(len(excluded), srcNoun+"(s) excluded by the migration filter")
}

// folderSizes lists every regular file under root on the host cfg describes,
// by size, keyed by its path relative to root.
func folderSizes(ctx context.Context, cfg *transxex.SSHConfig, root string) (map[string]int64, error) {
	files, err := listFiles(ctx, cfg, root)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(files))
	for rel, f := range files {
		sizes[rel] = f.size
	}
	return sizes, nil
}

// bucketSizes lists every object under the bucket path at ref, by size, keyed by
// its key relative to the path's prefix — the same key transx-ex wrote a file
// to, or read an object from, so it lines up with a folder's relative paths.
//
// A key ending in "/" is a folder marker some tools write: it names a
// directory, which a file system lists as no file, so it is left out.
func bucketSizes(ref commonmodel.ConnectionRef, bucketPath string) (map[string]int64, error) {
	loc, err := resolveOSLoc(ref, bucketPath)
	if err != nil {
		return nil, fmt.Errorf("resolve location: %w", err)
	}
	provider, err := transxex.NewS3Provider(loc)
	if err != nil {
		return nil, fmt.Errorf("create S3 provider: %w", err)
	}
	defer transxex.CloseS3Provider(provider) //nolint:errcheck

	_, prefix := transxex.ParseBucketAndKey(bucketPath)
	objects, err := provider.ListObjects(prefix)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(objects))
	for rel, o := range buildObjectMap(objects, prefix) {
		if strings.HasSuffix(rel, "/") {
			continue
		}
		sizes[rel] = o.size
	}
	return sizes, nil
}

// joinItem joins a folder or bucket path and a relative path for an ItemPath,
// without doubling the slash a bucket path keeps at its end.
func joinItem(root, rel string) string {
	return strings.TrimSuffix(root, "/") + "/" + rel
}

// ── DBMS Validation ───────────────────────────────────────────────────────────

// ValidateDBMS compares every database this model migrated: per-table row counts
// and the schema object inventories.
//
// Comparing data alone would miss anything without rows — a view, a function, a
// trigger — and those are exactly what fails quietly, most often because a
// MySQL/MariaDB DEFINER clause names a user the target does not have.
//
// A database that cannot be read is reported as one failed item and the next
// one is validated: one unreachable database does not erase what the others
// would have said. opt.Mode does not apply here.
func ValidateDBMS(ctx context.Context, m targetmodel.MigrationDBModel, opt Options) ([]model.ValidationDetailItem, error) {
	var details []model.ValidationDetailItem
	for i, d := range m.Databases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dopt := opt.within(itemLabel(i, len(m.Databases), d.SrcName))
		dbDetails, err := validateDatabase(ctx, m, d, dopt)
		if err != nil {
			details = append(details, model.ValidationDetailItem{
				ItemPath: d.SrcName,
				Status:   "failed",
				Message:  fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		details = append(details, dbDetails...)
	}
	return details, nil
}

// validateDatabase validates one database of m in two passes.
//
// The first inspects both sides at once without exact row counts: the table
// list, the schema objects and the character set, in seconds. The second
// replaces the row counts — statistics estimates until then — with exact ones,
// table by table through DescribeTable, which is what lets the progress line
// name how many tables are done. One Inspect counting every table itself says
// nothing until it has counted the last of them.
func validateDatabase(
	ctx context.Context,
	m targetmodel.MigrationDBModel,
	d targetmodel.DBMigrationInfo,
	opt Options,
) ([]model.ValidationDetailItem, error) {
	dbType := string(m.DBType)
	dstName := d.DstName
	if dstName == "" {
		dstName = d.SrcName
	}

	srcLoc, err := resolveDBLoc(m.SrcConnection, d.SrcName, dbType)
	if err != nil {
		return nil, fmt.Errorf("resolve src connection: %w", err)
	}
	dstLoc, err := resolveDBLoc(m.DstConnection, dstName, dbType)
	if err != nil {
		return nil, fmt.Errorf("resolve dst connection: %w", err)
	}

	// Both sides are inspected through the same schema selection the migration
	// ran under. Without it each side would report every schema in its database,
	// and a migration of one schema out of several would be compared against
	// tables it never touched.
	srcSchemas, dstSchemas := splitPgSchemaPairs(d.PgSchemas)
	srcLoc.PgSchema = srcSchemas
	dstLoc.PgSchema = dstSchemas
	if len(dstLoc.PgSchema) == 0 {
		dstLoc.PgSchema = srcSchemas
	}

	// Both sides are inspected with the same opt-in metrics; see validationMetric.
	metric := validationMetric(m.DBType)
	srcLoc.Metric = metric
	dstLoc.Metric = metric

	opt.report("inspecting tables and schema objects")
	var srcInfo, dstInfo *transxex.DBMSInfo
	err = bothSides(
		func() (err error) {
			if srcInfo, err = transxex.InspectDBMS(srcLoc); err != nil {
				return fmt.Errorf("inspect src database %q: %w", d.SrcName, err)
			}
			return nil
		},
		func() (err error) {
			if dstInfo, err = transxex.InspectDBMS(dstLoc); err != nil {
				return fmt.Errorf("inspect dst database %q: %w", dstName, err)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	renamed := len(dstSchemas) > 0
	var details []model.ValidationDetailItem
	details = append(details, countRows(ctx, srcLoc, dstLoc, m.DBType, srcInfo, dstInfo, renamed, d.Rules, opt)...)
	details = append(details, compareDBTables(srcInfo, dstInfo, renamed, d.Rules)...)
	details = append(details, compareSchemaObjects(srcInfo, dstInfo, d.Rules)...)
	details = append(details, compareCharsets(srcInfo, dstInfo)...)
	details = append(details, deprecatedForeignKeys(dstLoc, dstInfo)...)
	return details, nil
}

// deprecatedForeignKeys warns of the foreign keys a MySQL 8.4 target holds on a
// non-unique or partial key. The migration created them as the source had
// them, so they are not a failure; but 8.4 deprecates them and a later release
// is to refuse them, which the operator should know before upgrading. A target
// whose keys cannot be read is not reported on: this is a warning, and the
// validation stands without it.
func deprecatedForeignKeys(dstLoc transxex.DBMSLocation, dst *transxex.DBMSInfo) []model.ValidationDetailItem {
	if !transxex.RestrictsNonStandardForeignKeys(dstLoc.DBMSType, dst.ServerVersion) {
		return nil
	}
	keys, err := transxex.NonStandardForeignKeys(dstLoc)
	if err != nil {
		return nil
	}
	var details []model.ValidationDetailItem
	for _, k := range keys {
		details = append(details, model.ValidationDetailItem{
			ItemPath: dstLoc.Database + "/foreign_key:" + k,
			Status:   "warning",
			Message: fmt.Sprintf("refers to a non-unique or partial key, which MySQL %s deprecates; "+
				"a later release refuses it, so give the referenced columns a UNIQUE key before upgrading", dst.ServerVersion),
		})
	}
	return details
}

// rowCountConcurrency is how many tables are counted at once. Each count is a
// connection to either side, and a COUNT(*) scans its table, so this is kept
// well below what a server allows.
const rowCountConcurrency = 4

// countRows replaces the row counts in src and dst — statistics estimates, as
// Inspect left them — with exact ones, one DescribeTable per table on each side.
//
// Only the tables whose counts compareDBTables compares are counted: one the
// filter dropped, or one absent from either side, is decided without a count. A
// table whose count could not be read is reported and taken out of both
// listings, so an estimate never stands in for an exact count.
func countRows(
	ctx context.Context,
	srcLoc, dstLoc transxex.DBMSLocation,
	dbType commonmodel.DBMSType,
	src, dst *transxex.DBMSInfo,
	renamed bool,
	rules []commonmodel.DBMSFilterRule,
	opt Options,
) []model.ValidationDetailItem {
	key := tableKey(renamed)
	dropped, _ := filteredTables(rules)

	dstIdx := make(map[string]int, len(dst.Tables))
	for i, t := range dst.Tables {
		dstIdx[key(t)] = i
	}
	type pair struct{ si, di int }
	var pairs []pair
	for si, t := range src.Tables {
		k := key(t)
		if dropped[k] {
			continue
		}
		if di, ok := dstIdx[k]; ok {
			pairs = append(pairs, pair{si, di})
		}
	}

	metric := rowCountMetric(dbType)
	progress := newSideProgress(opt, "counting rows", len(pairs), "tables")
	errs := make([]error, len(pairs))
	sem := make(chan struct{}, rowCountConcurrency)
	var wg sync.WaitGroup
	for n, p := range pairs {
		if err := ctx.Err(); err != nil {
			errs[n] = err
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(n int, p pair) {
			defer wg.Done()
			defer func() { <-sem }()
			// Each goroutine writes only its own pair's entries, so the two
			// listings need no lock.
			errs[n] = bothSides(
				func() error {
					c, err := exactRowCount(srcLoc, metric, src.Tables[p.si])
					if err != nil {
						return fmt.Errorf("src: %w", err)
					}
					src.Tables[p.si].RowCount = c
					progress.advance(true)
					return nil
				},
				func() error {
					c, err := exactRowCount(dstLoc, metric, dst.Tables[p.di])
					if err != nil {
						return fmt.Errorf("dst: %w", err)
					}
					dst.Tables[p.di].RowCount = c
					progress.advance(false)
					return nil
				},
			)
		}(n, p)
	}
	wg.Wait()

	var details []model.ValidationDetailItem
	dropSrc, dropDst := map[int]bool{}, map[int]bool{}
	for n, err := range errs {
		if err == nil {
			continue
		}
		p := pairs[n]
		details = append(details, model.ValidationDetailItem{
			ItemPath: src.Database + "/" + key(src.Tables[p.si]),
			Status:   "failed",
			Message:  fmt.Sprintf("row count failed: %v", err),
		})
		dropSrc[p.si], dropDst[p.di] = true, true
	}
	src.Tables = withoutTables(src.Tables, dropSrc)
	dst.Tables = withoutTables(dst.Tables, dropDst)
	return details
}

// exactRowCount counts t's rows at loc. A PostgreSQL table is looked up in its
// own schema alone, so a name several schemas share is not ambiguous.
func exactRowCount(loc transxex.DBMSLocation, metric *transxex.DBMSMetricOption, t transxex.TableInfo) (int64, error) {
	loc.Metric = metric
	if t.PgSchema != "" {
		loc.PgSchema = []string{t.PgSchema}
	}
	info, err := transxex.DescribeTable(loc, t.Name)
	if err != nil {
		return 0, err
	}
	return info.RowCount, nil
}

// withoutTables returns tables less the entries whose index drop holds.
func withoutTables(tables []transxex.TableInfo, drop map[int]bool) []transxex.TableInfo {
	if len(drop) == 0 {
		return tables
	}
	out := make([]transxex.TableInfo, 0, len(tables)-len(drop))
	for i, t := range tables {
		if !drop[i] {
			out = append(out, t)
		}
	}
	return out
}

// splitPgSchemaPairs mirrors the migration adapter: name pairs in, the two
// positional slices out, with an empty destination when nothing is renamed.
func splitPgSchemaPairs(pairs []commonmodel.PgSchemaMapping) (src, dst []string) {
	if len(pairs) == 0 {
		return nil, nil
	}
	renamed := false
	for _, p := range pairs {
		to := p.DstName
		if to == "" {
			to = p.SrcName
		}
		if to != p.SrcName {
			renamed = true
		}
		src = append(src, p.SrcName)
		dst = append(dst, to)
	}
	if !renamed {
		return src, nil
	}
	return src, dst
}

// schemaObjectKinds pairs each inventory with the name reported for it. An
// inventory that was not collected is nil on both sides and compared as absent,
// which is why "not requested" and "empty" have to stay distinguishable: a nil
// source list is skipped rather than read as "the target should hold nothing".
func schemaObjectKinds(i *transxex.DBMSInfo) map[string][]string {
	return map[string][]string{
		"foreignKey":       i.ForeignKeys,
		"view":             i.Views,
		"function":         i.Functions,
		"procedure":        i.Procedures,
		"trigger":          i.Triggers,
		"event":            i.Events,
		"materializedView": i.MaterializedViews,
		"sequence":         i.Sequences,
		"type":             i.Types,
		"extension":        i.Extensions,
		"rule":             i.Rules,
		"validator":        i.Validators,
	}
}

// compareSchemaObjects reports the schema objects the source holds and the
// target does not, and vice versa.
//
// An object the migration was told to exclude is not a miss: comparing without
// the filter would turn every deliberate exclusion into a false positive. The
// reverse direction needs no such allowance — the target is verified empty
// before a migration starts, so anything in it came from this migration.
func compareSchemaObjects(src, dst *transxex.DBMSInfo, rules []commonmodel.DBMSFilterRule) []model.ValidationDetailItem {
	excluded := excludedObjects(rules)
	srcKinds := schemaObjectKinds(src)
	dstKinds := schemaObjectKinds(dst)

	kinds := make([]string, 0, len(srcKinds))
	for k := range srcKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	var details []model.ValidationDetailItem
	for _, kind := range kinds {
		srcList, dstList := srcKinds[kind], dstKinds[kind]
		if srcList == nil && dstList == nil {
			continue // not collected on either side
		}

		dstSet := make(map[string]bool, len(dstList))
		for _, n := range dstList {
			dstSet[n] = true
		}
		srcSet := make(map[string]bool, len(srcList))
		for _, n := range srcList {
			srcSet[n] = true
			if dstSet[n] || excluded[kind+":"+n] {
				continue
			}
			details = append(details, model.ValidationDetailItem{
				ItemPath: fmt.Sprintf("%s/%s:%s", src.Database, kind, n),
				Status:   "failed",
				Message:  fmt.Sprintf("schema object missing on target: %s %s", kind, n),
			})
		}
		for _, n := range dstList {
			if srcSet[n] {
				continue
			}
			details = append(details, model.ValidationDetailItem{
				ItemPath: fmt.Sprintf("%s/%s:%s", dst.Database, kind, n),
				Status:   "failed",
				Message:  fmt.Sprintf("schema object exists on target but not in source: %s %s", kind, n),
			})
		}
	}
	return details
}

// excludedObjects indexes the object_exclude rules as "kind:name", so a
// deliberately dropped object is not reported as missing.
func excludedObjects(rules []commonmodel.DBMSFilterRule) map[string]bool {
	out := map[string]bool{}
	for _, r := range rules {
		if r.Type == commonmodel.DBMSRuleObjectExclude && r.Kind != "" && r.Name != "" {
			out[r.Kind+":"+r.Name] = true
		}
	}
	return out
}

// compareCharsets reports text-handling differences as warnings rather than
// failures. A managed service can impose its own server defaults, so a
// difference here is not on its own evidence that the migration went wrong.
func compareCharsets(src, dst *transxex.DBMSInfo) []model.ValidationDetailItem {
	var details []model.ValidationDetailItem
	add := func(what, a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		details = append(details, model.ValidationDetailItem{
			ItemPath: src.Database,
			Status:   "warning",
			Message:  fmt.Sprintf("%s differs: src=%s dst=%s", what, a, b),
		})
	}
	add("characterSet", src.CharacterSet, dst.CharacterSet)
	add("collation", src.Collation, dst.Collation)
	add("ctype", src.Ctype, dst.Ctype)
	return details
}

// validationMetric selects what the first inspection must collect for
// validation: the schema object inventories. They are opt-in, and
// compareSchemaObjects can only compare what was collected: without them a view
// or event missing from the target passes as two empty lists.
//
// Row counts are left to rowCountMetric and countRows, table by table.
func validationMetric(dbType commonmodel.DBMSType) *transxex.DBMSMetricOption {
	t := func() *bool { b := true; return &b }
	switch dbType {
	case commonmodel.DBMSTypeMySQL:
		return &transxex.DBMSMetricOption{MySQL: &transxex.MySQLMetric{
			ForeignKeys: t(), Views: t(),
			Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case commonmodel.DBMSTypeMariaDB:
		return &transxex.DBMSMetricOption{MariaDB: &transxex.MariaDBMetric{
			ForeignKeys: t(), Views: t(),
			Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case commonmodel.DBMSTypePostgreSQL:
		return &transxex.DBMSMetricOption{PostgreSQL: &transxex.PostgreSQLMetric{
			ForeignKeys: t(), Views: t(), MaterializedViews: t(),
			Functions: t(), Procedures: t(), Triggers: t(),
			Sequences: t(), Types: t(), Extensions: t(), Rules: t(),
		}}
	case commonmodel.DBMSTypeMongoDB:
		return &transxex.DBMSMetricOption{MongoDB: &transxex.MongoDBMetric{
			Views: t(), Validators: t(),
		}}
	default:
		return nil
	}
}

// rowCountMetric asks DescribeTable for an exact row count.
//
// Row counts must be exact. Without it the engine's statistics estimate is all
// there is, which InnoDB gives as 0 for a table restored moments ago — read as a
// row count that is every migrated table looking empty. MongoDB's DescribeTable
// always counts exactly.
func rowCountMetric(dbType commonmodel.DBMSType) *transxex.DBMSMetricOption {
	t := func() *bool { b := true; return &b }
	switch dbType {
	case commonmodel.DBMSTypeMySQL:
		return &transxex.DBMSMetricOption{MySQL: &transxex.MySQLMetric{RowCountExact: t()}}
	case commonmodel.DBMSTypeMariaDB:
		return &transxex.DBMSMetricOption{MariaDB: &transxex.MariaDBMetric{RowCountExact: t()}}
	case commonmodel.DBMSTypePostgreSQL:
		return &transxex.DBMSMetricOption{PostgreSQL: &transxex.PostgreSQLMetric{RowCountExact: t()}}
	case commonmodel.DBMSTypeMongoDB:
		return &transxex.DBMSMetricOption{MongoDB: &transxex.MongoDBMetric{RowCountExact: t()}}
	default:
		return nil
	}
}

// tableKey says how a table is identified when the two sides are matched up —
// see compareDBTables for why renamed decides it.
func tableKey(renamed bool) func(transxex.TableInfo) string {
	return func(t transxex.TableInfo) string {
		if renamed || t.PgSchema == "" {
			return t.Name
		}
		return t.PgSchema + "." + t.Name
	}
}

// compareDBTables compares per-table/collection row counts between src and dst.
// Works for MySQL/MariaDB/PostgreSQL (tables) and MongoDB (collections).
//
// renamed says whether the migration gave the target schemas different names. It
// decides how a table is identified here: normally by schema and name together,
// so that two same-named tables in different PostgreSQL schemas stay apart, but
// by name alone once the schemas have been renamed — the schema is then expected
// to differ on the two sides, and keying on it would report every table as both
// missing and extra.
//
// rules are the exclude pipeline this database migrated under, and two of the
// three kinds change what the counts should say — see filteredTables. Without
// them a table the plan deliberately dropped reads as "missing in destination",
// which is the same false positive compareSchemaObjects has always avoided on the
// schema side.
func compareDBTables(src, dst *transxex.DBMSInfo, renamed bool, rules []commonmodel.DBMSFilterRule) []model.ValidationDetailItem {
	key := tableKey(renamed)
	srcMap := make(map[string]int64, len(src.Tables))
	for _, t := range src.Tables {
		srcMap[key(t)] = t.RowCount
	}
	dstMap := make(map[string]int64, len(dst.Tables))
	for _, t := range dst.Tables {
		dstMap[key(t)] = t.RowCount
	}

	dropped, rowFiltered := filteredTables(rules)
	found := newFindings(src.Database)
	skipped := 0

	srcNames := make([]string, 0, len(srcMap))
	for name := range srcMap {
		srcNames = append(srcNames, name)
	}
	sort.Strings(srcNames)

	for _, name := range srcNames {
		if dropped[name] {
			// The migration was told not to carry this table, so its absence is
			// the plan working, not a miss.
			skipped++
			continue
		}
		srcCount := srcMap[name]
		dstCount, ok := dstMap[name]
		switch {
		case !ok:
			found.fail(src.Database+"/"+name, "table missing in destination")
		case srcCount == dstCount:
			// Equal is equal, whether or not a row filter applied to this table:
			// the predicate simply matched nothing.
		case rowFiltered[name] && dstCount < srcCount:
			// A row_exclude rule dropped rows on purpose, and how many it dropped
			// is only knowable by running the predicate — which validation does
			// not do. Fewer rows is therefore expected and unquantified, so it is
			// reported as not comparable rather than counted as a loss.
			found.notice("table(s) with rows excluded by the migration filter,"+
				" so the row count is not comparable", name)
		default:
			found.fail(src.Database+"/"+name,
				fmt.Sprintf("row count mismatch: src=%d dst=%d", srcCount, dstCount))
		}
	}

	dstNames := make([]string, 0, len(dstMap))
	for name := range dstMap {
		dstNames = append(dstNames, name)
	}
	sort.Strings(dstNames)

	for _, name := range dstNames {
		if _, ok := srcMap[name]; ok {
			continue
		}
		found.fail(dst.Database+"/"+name, "table exists in destination but not in source")
	}

	return found.summarise(skipped, "table(s) excluded by the migration filter")
}

// filteredTables indexes the rules that change what the row counts should say.
//
//	dropped      a table_column rule naming no column excludes the whole table,
//	             so the target is not meant to have it at all.
//	rowFiltered  a row_exclude rule removes rows the predicate matches, so the
//	             target legitimately holds fewer than the source.
//
// A table_column rule that DOES name columns is absent from both: dropping
// columns leaves the table and its row count alone, so nothing here changes.
func filteredTables(rules []commonmodel.DBMSFilterRule) (dropped, rowFiltered map[string]bool) {
	dropped, rowFiltered = map[string]bool{}, map[string]bool{}
	for _, r := range rules {
		if r.Table == "" {
			continue
		}
		switch r.Type {
		case commonmodel.DBMSRuleTableColumn:
			if len(r.Columns) == 0 {
				dropped[r.Table] = true
			}
		case commonmodel.DBMSRuleRowExclude:
			rowFiltered[r.Table] = true
		}
	}
	return dropped, rowFiltered
}

// resolveDBLoc maps a ConnectionRef to a transxex.DBMSLocation for DBMS validation.
// Delegates to the migration package resolver so validation and execution share
// one source-of-truth for every connection source (honeybee/beetleDb/db).
func resolveDBLoc(ref commonmodel.ConnectionRef, database, dbType string) (transxex.DBMSLocation, error) {
	return migration.ResolveDBMSLocation(ref, database, dbType)
}
