package validation

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
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
// as the corresponding src folder, with identical SHA256 checksums.
// Returns one ValidationDetailItem per mismatch (missing file, extra file, or hash diff).
// Connection errors are reported as a failed item for the folder root.
func ValidateFileSystem(m targetmodel.MigrationFileSystemModel) ([]model.ValidationDetailItem, error) {
	// Cross-storage (filesystem→objectstorage) compares heterogeneous endpoints
	// (SSH checksums vs S3 ETags) and is not yet supported; report it as skipped
	// rather than failing to resolve the object-storage destination as SSH.
	if m.DstType == commonmodel.StorageTypeObjectStorage {
		return []model.ValidationDetailItem{{
			ItemPath: "",
			Status:   "skipped",
			Message:  "cross-storage validation (filesystem→objectstorage) is not supported",
		}}, nil
	}

	srcSSH, err := resolveSSHConfig(m.SrcConnection)
	if err != nil {
		return nil, fmt.Errorf("resolve src connection: %w", err)
	}
	dstSSH, err := resolveSSHConfig(m.DstConnection)
	if err != nil {
		return nil, fmt.Errorf("resolve dst connection: %w", err)
	}

	var details []model.ValidationDetailItem
	for _, folder := range m.Folders {
		folderDetails, err := validateSSHFolder(srcSSH, dstSSH, folder.SrcPath, folder.DstPath, folder.Rules)
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

// validateSSHFolder computes per-file SHA256 checksums on both hosts and
// returns a failed ValidationDetailItem for each discrepancy.
//
// rules are the filter the migration applied to this folder. They change the
// source direction only: a file the filter dropped is absent from the
// destination on purpose, so it is not a miss. The destination direction keeps
// reading the WHOLE source listing — see the three outcomes below.
func validateSSHFolder(srcCfg, dstCfg *transxex.SSHConfig, srcPath, dstPath string, rules []commonmodel.PathFilterRule) ([]model.ValidationDetailItem, error) {
	flt, err := newMigrationFilter(rules)
	if err != nil {
		return nil, fmt.Errorf("build migration filter: %w", err)
	}

	srcFiles, err := remoteFiles(srcCfg, srcPath, flt.needsSize())
	if err != nil {
		return nil, fmt.Errorf("compute src checksums %s: %w", srcPath, err)
	}
	// Sizes are only ever read to answer a size rule against the source, which is
	// the side the filter was applied to. The destination never needs them.
	dstFiles, err := remoteFiles(dstCfg, dstPath, false)
	if err != nil {
		return nil, fmt.Errorf("compute dst checksums %s: %w", dstPath, err)
	}

	return compareFileTrees(srcFiles, dstFiles, flt, srcPath, dstPath), nil
}

// compareFileTrees is validateSSHFolder's verdict, with the two remote reads
// already done. Separated so the comparison — which is where the filter changes
// what is reported — can be exercised without a host to SSH into.
func compareFileTrees(
	srcFiles, dstFiles map[string]remoteFile,
	flt *migrationFilter,
	srcPath, dstPath string,
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
		if !ok {
			found.fail(srcPath+"/"+relPath, "file missing in destination")
		} else if src.hash != dst.hash {
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

// remoteFile is one regular file as both ends are read back: its content hash,
// and its size when a size rule made the size worth fetching.
type remoteFile struct {
	hash string
	size int64
}

// remoteFiles returns a map of relative-path → remoteFile for every regular file
// found recursively under root on the host described by cfg.
// The relative path is stripped of the leading root prefix.
//
// withSize adds a second command that reads the sizes. It is asked for only when
// the folder's filter carries a size rule, because that is the only thing sizes
// decide here — the comparison itself is by hash, and an extra walk of the tree
// on a remote host is not worth paying for a field nothing reads.
func remoteFiles(cfg *transxex.SSHConfig, root string, withSize bool) (map[string]remoteFile, error) {
	// Use print0/xargs -0 to handle filenames with spaces or special characters.
	cmd := fmt.Sprintf(
		"find %s -type f -print0 2>/dev/null | sort -z | xargs -0 sha256sum 2>/dev/null",
		shellQuote(root),
	)
	out, err := runSSHCommand(cfg, cmd)
	if err != nil {
		return nil, err
	}

	result := make(map[string]remoteFile)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// sha256sum output: "<hash>  <absolute path>" (two spaces)
		idx := strings.Index(line, "  ")
		if idx < 0 {
			continue
		}
		hash := strings.TrimSpace(line[:idx])
		relPath := relativeTo(root, line[idx+2:])
		if relPath == "" || hash == "" {
			continue
		}
		result[relPath] = remoteFile{hash: hash}
	}

	if !withSize {
		return result, nil
	}
	if err := addRemoteSizes(cfg, root, result); err != nil {
		return nil, err
	}
	return result, nil
}

// addRemoteSizes fills in the size of every file already listed in files.
//
// NUL-delimited like the hash command above, so a newline in a file name cannot
// shift the parse. -printf is GNU find, which is the same family the surrounding
// find -print0 / sort -z / xargs -0 already assumes.
func addRemoteSizes(cfg *transxex.SSHConfig, root string, files map[string]remoteFile) error {
	cmd := fmt.Sprintf(
		"find %s -type f -printf '%%s\\t%%p\\0' 2>/dev/null",
		shellQuote(root),
	)
	out, err := runSSHCommand(cfg, cmd)
	if err != nil {
		return err
	}

	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		size, fullPath, found := strings.Cut(record, "\t")
		if !found {
			continue
		}
		relPath := relativeTo(root, fullPath)
		entry, ok := files[relPath]
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
		if err != nil {
			continue
		}
		entry.size = n
		files[relPath] = entry
	}
	return nil
}

// relativeTo strips root and the separator that follows it from an absolute path.
func relativeTo(root, fullPath string) string {
	return strings.TrimPrefix(strings.TrimPrefix(fullPath, root), "/")
}

// runSSHCommand opens an SSH session to the host described by cfg, executes command,
// and returns the combined stdout+stderr output as a string.
func runSSHCommand(cfg *transxex.SSHConfig, command string) (string, error) {
	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return "", fmt.Errorf("build SSH auth: %w", err)
	}
	if len(authMethods) == 0 {
		return "", fmt.Errorf("no SSH authentication method available for %s", cfg.Host)
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
		return "", fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("SSH new session: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(command)
	if err != nil {
		return "", fmt.Errorf("SSH command failed on %s: %w", addr, err)
	}
	return string(out), nil
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
func ValidateObjectStorage(m targetmodel.MigrationObjectStorageModel) ([]model.ValidationDetailItem, error) {
	// Cross-storage (objectstorage→filesystem) compares heterogeneous endpoints
	// (S3 ETags vs SSH checksums) and is not yet supported; report it as skipped
	// rather than failing to resolve the SSH destination as object storage.
	if m.DstType == commonmodel.StorageTypeFilesystem {
		return []model.ValidationDetailItem{{
			ItemPath: "",
			Status:   "skipped",
			Message:  "cross-storage validation (objectstorage→filesystem) is not supported",
		}}, nil
	}

	var details []model.ValidationDetailItem
	for _, bucket := range m.Buckets {
		bucketDetails, err := validateObjectBucket(m.SrcConnection, m.DstConnection, bucket.SrcPath, bucket.DstPath, bucket.Rules)
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
func validateObjectBucket(srcRef, dstRef commonmodel.ConnectionRef, srcPath, dstPath string, rules []commonmodel.PathFilterRule) ([]model.ValidationDetailItem, error) {
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

	srcObjects, err := srcProvider.ListObjects(srcPrefix)
	if err != nil {
		return nil, fmt.Errorf("list src objects: %w", err)
	}
	dstObjects, err := dstProvider.ListObjects(dstPrefix)
	if err != nil {
		return nil, fmt.Errorf("list dst objects: %w", err)
	}

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
		if flt.excludesObject(src.key, src.size) {
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
	key  string
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
		m[relKey] = remoteObject{etag: obj.ETag, key: obj.Key, size: obj.Size}
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

// ── DBMS Validation ───────────────────────────────────────────────────────────

// ValidateDBMS compares every database this model migrated: per-table row counts
// and the schema object inventories.
//
// Comparing data alone would miss anything without rows — a view, a function, a
// trigger — and those are exactly what fails quietly, most often because a
// MySQL/MariaDB DEFINER clause names a user the target does not have.
func ValidateDBMS(m targetmodel.MigrationDBModel) ([]model.ValidationDetailItem, error) {
	dbType := string(m.DBType)
	metric := validationMetric(m.DBType)

	var details []model.ValidationDetailItem
	for _, d := range m.Databases {
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
		srcLoc.Metric = metric
		dstLoc.Metric = metric

		srcInfo, err := transxex.InspectDBMS(srcLoc)
		if err != nil {
			return nil, fmt.Errorf("inspect src database %q: %w", d.SrcName, err)
		}
		dstInfo, err := transxex.InspectDBMS(dstLoc)
		if err != nil {
			return nil, fmt.Errorf("inspect dst database %q: %w", dstName, err)
		}

		details = append(details, compareDBTables(srcInfo, dstInfo, len(dstSchemas) > 0, d.Rules)...)
		details = append(details, compareSchemaObjects(srcInfo, dstInfo, d.Rules)...)
		details = append(details, compareCharsets(srcInfo, dstInfo)...)
	}
	return details, nil
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

// validationMetric selects what an inspection must collect for validation.
//
// Row counts must be exact. A default inspect reports the engine's statistics
// estimate, which InnoDB gives as 0 for a table restored moments ago — read as
// a row count that is every migrated table looking empty.
//
// The schema object inventories are opt-in the same way, and compareSchemaObjects
// can only compare what was collected: without them a view or event missing from
// the target passes as two empty lists.
func validationMetric(dbType commonmodel.DBMSType) *transxex.DBMSMetricOption {
	t := func() *bool { b := true; return &b }
	switch dbType {
	case commonmodel.DBMSTypeMySQL:
		return &transxex.DBMSMetricOption{MySQL: &transxex.MySQLMetric{
			RowCountExact: t(), ForeignKeys: t(), Views: t(),
			Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case commonmodel.DBMSTypeMariaDB:
		return &transxex.DBMSMetricOption{MariaDB: &transxex.MariaDBMetric{
			RowCountExact: t(), ForeignKeys: t(), Views: t(),
			Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case commonmodel.DBMSTypePostgreSQL:
		return &transxex.DBMSMetricOption{PostgreSQL: &transxex.PostgreSQLMetric{
			RowCountExact: t(), ForeignKeys: t(), Views: t(), MaterializedViews: t(),
			Functions: t(), Procedures: t(), Triggers: t(),
			Sequences: t(), Types: t(), Extensions: t(), Rules: t(),
		}}
	case commonmodel.DBMSTypeMongoDB:
		return &transxex.DBMSMetricOption{MongoDB: &transxex.MongoDBMetric{
			RowCountExact: t(), Views: t(), Validators: t(),
		}}
	default:
		return nil
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
	key := func(t transxex.TableInfo) string {
		if renamed || t.PgSchema == "" {
			return t.Name
		}
		return t.PgSchema + "." + t.Name
	}
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
