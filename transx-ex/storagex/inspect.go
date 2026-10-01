package storagex

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/storagex/filter"
)

// FSEntry represents a filesystem directory entry.
type FSEntry struct {
	Path        string `json:"path"`                  // Absolute path (always populated)
	IsMount     bool   `json:"isMount"`               // Whether Path is a mount point of a separate filesystem (always populated)
	ModTime     string `json:"modTime,omitempty"`     // RFC3339 timestamp (only when FilesystemMetric.FolderModTime)
	Permissions string `json:"permissions,omitempty"` // Permission string (only when FilesystemMetric.FolderPerms)
	FileCount   *int   `json:"fileCount,omitempty"`   // Direct (non-recursive) file count (only when FilesystemMetric.FolderFileCount)
	FileSize    *int64 `json:"fileSize,omitempty"`    // Direct (non-recursive) file size sum in bytes (only when FilesystemMetric.FolderFileSize)
}

// FSInspectResult is the result of InspectFilesystem. Folders holds the
// directory listing (subject to Filter and MaxDepth); TotalSize, FileCount and
// ExtensionCounts are path-wide aggregates populated only when requested via
// FilesystemMetric and computed over the whole path recursively (ignoring MaxDepth).
type FSInspectResult struct {
	Path            string         `json:"path"`                      // Scan root (always populated)
	IsMount         bool           `json:"isMount"`                   // Whether the scan root is a mount point (always populated)
	TotalSize       *int64         `json:"totalSize,omitempty"`       // Total recursive size in bytes (FilesystemMetric.TotalSize)
	FileCount       *int           `json:"fileCount,omitempty"`       // Total recursive file count (FilesystemMetric.FileCount)
	ExtensionCounts map[string]int `json:"extensionCounts,omitempty"` // Per-extension file counts (FilesystemMetric.ExtensionCount)
	Folders         []FSEntry      `json:"folders"`                   // Directory listing
}

// OSEntry represents an object storage folder prefix entry.
type OSEntry struct {
	Key          string `json:"key"`                    // Folder prefix "bucket/prefix/" (always populated)
	LastModified string `json:"lastModified,omitempty"` // Latest LastModified of direct objects (only when ObjectStorageMetric.PrefixModTime)
	ObjectCount  *int   `json:"objectCount,omitempty"`  // Direct (non-recursive) object count (only when ObjectStorageMetric.PrefixObjectCount)
	Size         *int64 `json:"size,omitempty"`         // Direct (non-recursive) object size sum in bytes (only when ObjectStorageMetric.PrefixObjectSize)
}

// OSInspectResult is the result of InspectObjectStorage. Folders holds the
// folder prefix listing (subject to Filter and MaxDepth); TotalSize, ObjectCount
// and ExtensionCounts are aggregates over all objects under the scanned
// bucket/prefix, populated only when requested via ObjectStorageMetric.
type OSInspectResult struct {
	Path            string         `json:"path"`                      // Scan root "bucket/prefix/" (always populated)
	TotalSize       *int64         `json:"totalSize,omitempty"`       // Total object size in bytes (ObjectStorageMetric.TotalSize)
	ObjectCount     *int           `json:"objectCount,omitempty"`     // Total object count (ObjectStorageMetric.ObjectCount)
	ExtensionCounts map[string]int `json:"extensionCounts,omitempty"` // Per-extension object counts (ObjectStorageMetric.ExtensionCount)
	Folders         []OSEntry      `json:"folders"`                   // Folder prefix listing
}

// VirtualFSPaths lists Linux pseudo/virtual filesystem mount points excluded
// from filesystem inspection. Override to customise the exclusion list.
var VirtualFSPaths = []string{
	"/proc",
	"/sys",
	"/dev",
	"/run",
}

func isVirtualFS(path string) bool {
	for _, vp := range VirtualFSPaths {
		if path == vp {
			return true
		}
	}
	return false
}

// buildFindPruneExpr returns a find expression fragment that prunes VirtualFSPaths.
// Returns empty string when VirtualFSPaths is empty.
func buildFindPruneExpr() string {
	if len(VirtualFSPaths) == 0 {
		return ""
	}
	parts := make([]string, len(VirtualFSPaths))
	for i, p := range VirtualFSPaths {
		parts[i] = "-path " + shellQuote(p)
	}
	return `\( ` + strings.Join(parts, " -o ") + ` \) -prune -o `
}

// shellQuote returns a single-quoted shell-safe version of s.
// Single quotes in s are escaped by ending the quote, inserting a literal
// single quote, and reopening the quote:
//
//	it's  ->  'it'\''s'
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// InspectFilesystem inspects directories at the given DataLocation.
// Supports local and SSH filesystem access types.
// loc.Filter controls include/exclude patterns and MaxDepth scan depth for the
// folder listing. loc.Metric.Filesystem selects which information to extract;
// when absent it defaults to showing folder mtime and permissions (no aggregates).
func InspectFilesystem(loc DataLocation) (*FSInspectResult, error) {
	if loc.Filesystem == nil {
		return nil, fmt.Errorf("filesystem access config required")
	}

	m := fsMetric(loc)
	switch loc.Filesystem.AccessType {
	case AccessTypeLocal:
		return inspectLocalFilesystem(loc.Path, loc.Filter, m)
	case AccessTypeSSH:
		if loc.Filesystem.SSH == nil {
			return nil, fmt.Errorf("SSH config required for ssh access")
		}
		return inspectSSHFilesystem(loc.Path, loc.Filesystem.SSH, loc.Filter, m)
	default:
		return nil, fmt.Errorf("unsupported filesystem access type: %s", loc.Filesystem.AccessType)
	}
}

// fsMetricResolved is the effective filesystem metric selection with every flag
// resolved to a concrete value (defaults applied).
type fsMetricResolved struct {
	totalSize       bool
	fileCount       bool
	extensionCount  bool
	folderModTime   bool
	folderPerms     bool
	folderFileCount bool
	folderFileSize  bool
}

// needFileScan reports whether any requested metric requires walking files.
func (m fsMetricResolved) needFileScan() bool {
	return m.totalSize || m.fileCount || m.extensionCount || m.folderFileCount || m.folderFileSize
}

// fsMetric resolves loc.Metric.Filesystem into concrete flags using partial
// merge: an unset (nil) field keeps its default, an explicitly set field
// overrides it. Defaults: FolderModTime/FolderPerms on, everything else off.
func fsMetric(loc DataLocation) fsMetricResolved {
	r := fsMetricResolved{folderModTime: true, folderPerms: true}
	if loc.Metric == nil || loc.Metric.Filesystem == nil {
		return r
	}
	m := loc.Metric.Filesystem
	if m.TotalSize != nil {
		r.totalSize = *m.TotalSize
	}
	if m.FileCount != nil {
		r.fileCount = *m.FileCount
	}
	if m.ExtensionCount != nil {
		r.extensionCount = *m.ExtensionCount
	}
	if m.FolderModTime != nil {
		r.folderModTime = *m.FolderModTime
	}
	if m.FolderPerms != nil {
		r.folderPerms = *m.FolderPerms
	}
	if m.FolderFileCount != nil {
		r.folderFileCount = *m.FolderFileCount
	}
	if m.FolderFileSize != nil {
		r.folderFileSize = *m.FolderFileSize
	}
	return r
}

// fileAgg accumulates direct (non-recursive) file stats for a single folder.
type fileAgg struct {
	count int
	size  int64
}

func inspectLocalFilesystem(root string, flt *FilterOption, m fsMetricResolved) (*FSInspectResult, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	rootDepth := strings.Count(absRoot, "/")
	needFileScan := m.needFileScan()

	// Device id of the scan root's parent, for detecting whether the root itself
	// is a mount point.
	rootParentDev, rootParentOK := statDev(filepath.Dir(absRoot))

	// Device id per directory (parent is visited before its children by Walk),
	// used to detect mount points: a dir whose device differs from its parent is one.
	devByPath := make(map[string]uint64)
	mountOf := func(abs string, dev uint64, devOK bool) bool {
		if abs == "/" {
			return true
		}
		if !devOK {
			return false
		}
		if abs == absRoot {
			return rootParentOK && dev != rootParentDev
		}
		pdev, ok := devByPath[filepath.Dir(abs)]
		return ok && dev != pdev
	}

	result := &FSInspectResult{Path: absRoot}
	var total int64
	var fileTotal int
	counts := map[string]int{}
	perFolder := map[string]fileAgg{}

	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return absErr
		}

		if !info.IsDir() {
			// Only regular files contribute to aggregates and per-folder direct
			// counts, matching `find -type f` on the SSH path: symlinks, sockets,
			// FIFOs and device nodes are not transferable payload.
			if !info.Mode().IsRegular() {
				return nil
			}
			sz := info.Size()
			if m.totalSize {
				total += sz
			}
			if m.fileCount {
				fileTotal++
			}
			if m.extensionCount {
				counts[extKey(abs)]++
			}
			if m.folderFileCount || m.folderFileSize {
				d := filepath.Dir(abs)
				pf := perFolder[d]
				pf.count++
				pf.size += sz
				perFolder[d] = pf
			}
			return nil
		}

		// Skip virtual/pseudo filesystems unless the scan root itself is one.
		if abs != absRoot && isVirtualFS(abs) {
			return filepath.SkipDir
		}
		dev, devOK := devFromInfo(info)
		if devOK {
			devByPath[abs] = dev
		}

		depth := strings.Count(abs, "/") - rootDepth
		if flt != nil && flt.MaxDepth > 0 && depth > flt.MaxDepth {
			// MaxDepth bounds the folder listing only. File metrics scan the whole
			// tree, so keep descending when any is requested; otherwise prune.
			if needFileScan {
				return nil
			}
			return filepath.SkipDir
		}
		if !flt.MatchPath(filter.Rel(absRoot, abs), info.Size(), true) {
			return nil
		}

		entry := FSEntry{Path: abs, IsMount: mountOf(abs, dev, devOK)}
		if m.folderModTime {
			entry.ModTime = info.ModTime().UTC().Format(time.RFC3339)
		}
		if m.folderPerms {
			entry.Permissions = lsPermString(info.Mode())
		}
		result.Folders = append(result.Folders, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if rootDev, ok := devByPath[absRoot]; ok {
		result.IsMount = mountOf(absRoot, rootDev, true)
	}
	if m.totalSize {
		result.TotalSize = &total
	}
	if m.fileCount {
		result.FileCount = &fileTotal
	}
	if m.extensionCount {
		result.ExtensionCounts = counts
	}
	attachFolderFileStats(result.Folders, perFolder, m)
	return result, nil
}

// attachFolderFileStats fills FileCount/FileSize on each listed folder from the
// accumulated per-folder direct file stats, when the corresponding metric is set.
func attachFolderFileStats(folders []FSEntry, perFolder map[string]fileAgg, m fsMetricResolved) {
	if !m.folderFileCount && !m.folderFileSize {
		return
	}
	for i := range folders {
		pf := perFolder[folders[i].Path]
		if m.folderFileCount {
			c := pf.count
			folders[i].FileCount = &c
		}
		if m.folderFileSize {
			s := pf.size
			folders[i].FileSize = &s
		}
	}
}

// inspectSSHFilesystem inspects a remote filesystem over SSH. The remote host
// must provide GNU findutils, since the scan relies on `find -printf`.
// root must be absolute: a relative path would resolve against the remote login
// directory and break the absolute-path contract of FSEntry.Path.
func inspectSSHFilesystem(root string, cfg *SSHConfig, flt *FilterOption, m fsMetricResolved) (*FSInspectResult, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("path must be absolute for ssh access: %q", root)
	}
	root = filepath.Clean(root)

	var result *FSInspectResult
	// All commands share one connection: the folder listing, the scan root's
	// parent device lookup and the optional file scan.
	err := withSSHClient(cfg, func(run sshRunner) error {
		var scanErr error
		result, scanErr = scanSSHFilesystem(run, root, flt, m)
		return scanErr
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// sshDirInfo holds the stat fields `find` reports for one remote directory.
type sshDirInfo struct {
	size    int64
	modTime time.Time
	perms   string
	dev     uint64
	devOK   bool
}

func scanSSHFilesystem(run sshRunner, root string, flt *FilterOption, m fsMetricResolved) (*FSInspectResult, error) {
	pruneExpr := buildFindPruneExpr()

	// --- Folder listing: respects MaxDepth and include/exclude filter ---
	maxdepth := ""
	if flt != nil && flt.MaxDepth > 0 {
		maxdepth = fmt.Sprintf("-maxdepth %d ", flt.MaxDepth)
	}
	dirCmd := fmt.Sprintf("find %s %s%s-type d -printf '%%D\\t%%s\\t%%T@\\t%%M\\t%%p\\0'", shellQuote(root), maxdepth, pruneExpr)
	out, err := run(dirCmd)
	if err != nil {
		return nil, fmt.Errorf("SSH inspect failed: %w", err)
	}

	dirs, order := parseSSHDirRecords(out)
	// `find` emits directory-traversal order; sort to match the lexical order
	// filepath.Walk produces on the local path.
	sort.Slice(order, func(i, j int) bool { return pathLess(order[i], order[j]) })

	// Device id of the scan root's parent, for detecting whether the root is a mount point.
	rootParentDev, rootParentOK := sshDev(run, filepath.Dir(root))
	mountOf := func(p string, di sshDirInfo) bool {
		if p == "/" {
			return true
		}
		if !di.devOK {
			return false
		}
		if pd, ok := dirs[filepath.Dir(p)]; ok {
			return pd.devOK && di.dev != pd.dev
		}
		// Parent is outside the scan set, so p is the scan root.
		return rootParentOK && di.dev != rootParentDev
	}

	result := &FSInspectResult{Path: root}
	for _, p := range order {
		di := dirs[p]
		if !flt.MatchPath(filter.Rel(root, p), di.size, true) {
			continue
		}
		entry := FSEntry{Path: p, IsMount: mountOf(p, di)}
		if m.folderModTime {
			entry.ModTime = di.modTime.UTC().Format(time.RFC3339)
		}
		if m.folderPerms {
			entry.Permissions = di.perms
		}
		result.Folders = append(result.Folders, entry)
	}
	if di, ok := dirs[root]; ok {
		result.IsMount = mountOf(root, di)
	}

	// --- File metrics: full recursive file scan, ignoring MaxDepth ---
	// printf emits the full path (%p) so both the extension (basename) and the
	// containing folder (dir) can be derived from a single scan.
	if m.needFileScan() {
		fileCmd := fmt.Sprintf("find %s %s-type f -printf '%%s\\t%%p\\0'", shellQuote(root), pruneExpr)
		fout, ferr := run(fileCmd)
		if ferr != nil {
			return nil, fmt.Errorf("SSH inspect (files) failed: %w", ferr)
		}
		var total int64
		var fileTotal int
		counts := map[string]int{}
		perFolder := map[string]fileAgg{}
		for _, rec := range strings.Split(string(fout), "\x00") {
			if rec == "" {
				continue
			}
			parts := strings.SplitN(rec, "\t", 2)
			if len(parts) != 2 {
				continue
			}
			size, serr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			if serr != nil {
				continue
			}
			p := parts[1]
			if m.totalSize {
				total += size
			}
			if m.fileCount {
				fileTotal++
			}
			if m.extensionCount {
				counts[extKey(p)]++
			}
			if m.folderFileCount || m.folderFileSize {
				d := filepath.Dir(p)
				pf := perFolder[d]
				pf.count++
				pf.size += size
				perFolder[d] = pf
			}
		}
		if m.totalSize {
			result.TotalSize = &total
		}
		if m.fileCount {
			result.FileCount = &fileTotal
		}
		if m.extensionCount {
			result.ExtensionCounts = counts
		}
		attachFolderFileStats(result.Folders, perFolder, m)
	}

	return result, nil
}

// parseSSHDirRecords decodes the NUL-terminated "%D\t%s\t%T@\t%M\t%p" records
// emitted by the directory scan into a path-keyed map plus the paths in
// encounter order. Records are NUL-terminated and carry the path last, so
// names containing tabs or newlines cannot break field splitting. Malformed
// records are skipped.
func parseSSHDirRecords(out []byte) (map[string]sshDirInfo, []string) {
	dirs := make(map[string]sshDirInfo)
	var order []string
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 5)
		if len(parts) != 5 {
			continue
		}
		dev, devErr := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		p := parts[4]
		dirs[p] = sshDirInfo{
			size:    size,
			modTime: parseFindEpoch(parts[2]),
			perms:   parts[3],
			dev:     dev,
			devOK:   devErr == nil,
		}
		order = append(order, p)
	}
	return dirs, order
}

// parseFindEpoch converts find's "%T@" (seconds since epoch with a fractional
// part) into a time. A zero time is returned when the value is unparseable.
func parseFindEpoch(s string) time.Time {
	epoch, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return time.Time{}
	}
	sec := int64(epoch)
	nsec := int64((epoch - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}

// pathLess orders paths the way filepath.Walk visits them: lexically per path
// segment, so a parent always precedes its children. Comparing with the
// separator mapped to NUL — a byte no path segment can contain — makes a
// separator sort before every other character.
func pathLess(a, b string) bool {
	return strings.ReplaceAll(a, "/", "\x00") < strings.ReplaceAll(b, "/", "\x00")
}

// extKey returns the file extension (including the dot, e.g. ".txt") used as the
// ExtensionCounts map key, or "(none)" for files without an extension.
func extKey(name string) string {
	ext := filepath.Ext(filepath.Base(name))
	if ext == "" {
		return "(none)"
	}
	return ext
}

// lsPermString renders mode in the ls style used by `find -printf '%M'`
// (e.g. "drwxr-xr-x", "drwxrwxrwt"), so local and SSH inspection report the
// same permission string. Go's FileMode.String uses a different layout and
// spells the setuid/setgid/sticky bits as separate leading letters.
func lsPermString(mode os.FileMode) string {
	b := []byte("----------")
	switch {
	case mode&os.ModeDir != 0:
		b[0] = 'd'
	case mode&os.ModeSymlink != 0:
		b[0] = 'l'
	case mode&os.ModeSocket != 0:
		b[0] = 's'
	case mode&os.ModeNamedPipe != 0:
		b[0] = 'p'
	case mode&os.ModeCharDevice != 0:
		b[0] = 'c'
	case mode&os.ModeDevice != 0:
		b[0] = 'b'
	}
	const rwx = "rwxrwxrwx"
	perm := mode.Perm()
	for i := 0; i < 9; i++ {
		if perm&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	// setuid/setgid/sticky replace the execute bit of their triplet.
	if mode&os.ModeSetuid != 0 {
		b[3] = specialPermChar(b[3] == 'x', 's')
	}
	if mode&os.ModeSetgid != 0 {
		b[6] = specialPermChar(b[6] == 'x', 's')
	}
	if mode&os.ModeSticky != 0 {
		b[9] = specialPermChar(b[9] == 'x', 't')
	}
	return string(b)
}

// specialPermChar returns c when the triplet's execute bit is set and its
// uppercase form when it is not, as ls reports setuid/setgid/sticky bits.
func specialPermChar(hasExec bool, c byte) byte {
	if hasExec {
		return c
	}
	return c - ('a' - 'A')
}

// devFromInfo returns the device id (st_dev) from a FileInfo, or false when the
// underlying platform data is unavailable.
func devFromInfo(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// statDev returns the device id of a local path.
func statDev(path string) (uint64, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	return devFromInfo(info)
}

// sshDev returns the device id (st_dev) of a remote path via `stat`.
func sshDev(run sshRunner, remotePath string) (uint64, bool) {
	out, err := run("stat -c '%d' " + shellQuote(remotePath))
	if err != nil {
		return 0, false
	}
	dev, perr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if perr != nil {
		return 0, false
	}
	return dev, true
}

// osMetricResolved is the effective object storage metric selection with every
// flag resolved to a concrete value (defaults applied).
type osMetricResolved struct {
	totalSize         bool
	objectCount       bool
	extensionCount    bool
	prefixModTime     bool
	prefixObjectCount bool
	prefixObjectSize  bool
}

// needObjectScan reports whether any requested metric requires iterating objects.
func (m osMetricResolved) needObjectScan() bool {
	return m.totalSize || m.objectCount || m.extensionCount ||
		m.prefixModTime || m.prefixObjectCount || m.prefixObjectSize
}

// osMetric resolves loc.Metric.ObjectStorage into concrete flags using partial
// merge. Every field defaults to false (an object storage prefix has no
// intrinsic metadata, so all metrics are opt-in).
func osMetric(loc DataLocation) osMetricResolved {
	var r osMetricResolved
	if loc.Metric == nil || loc.Metric.ObjectStorage == nil {
		return r
	}
	m := loc.Metric.ObjectStorage
	if m.TotalSize != nil {
		r.totalSize = *m.TotalSize
	}
	if m.ObjectCount != nil {
		r.objectCount = *m.ObjectCount
	}
	if m.ExtensionCount != nil {
		r.extensionCount = *m.ExtensionCount
	}
	if m.PrefixModTime != nil {
		r.prefixModTime = *m.PrefixModTime
	}
	if m.PrefixObjectCount != nil {
		r.prefixObjectCount = *m.PrefixObjectCount
	}
	if m.PrefixObjectSize != nil {
		r.prefixObjectSize = *m.PrefixObjectSize
	}
	return r
}

// objPrefixAgg accumulates direct (non-recursive) object stats for a single prefix.
type objPrefixAgg struct {
	count  int
	size   int64
	latest string // latest LastModified among direct objects
}

// directPrefix returns the "bucket/dir/" prefix that an object key belongs to
// directly (its immediate parent folder). A top-level object maps to "bucket/".
func directPrefix(bucket, key string) string {
	k := strings.Trim(key, "/")
	if idx := strings.LastIndex(k, "/"); idx >= 0 {
		return bucket + "/" + k[:idx] + "/"
	}
	return bucket + "/"
}

// laterMod returns whichever LastModified string is more recent. It prefers
// RFC3339 parsing and falls back to lexical comparison when parsing fails.
func laterMod(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ta, ea := time.Parse(time.RFC3339, a)
	tb, eb := time.Parse(time.RFC3339, b)
	if ea == nil && eb == nil {
		if tb.After(ta) {
			return b
		}
		return a
	}
	if b > a {
		return b
	}
	return a
}

// InspectObjectStorage lists unique folder prefixes in the given bucket/prefix.
// loc.Path must contain a bucket name (e.g. "bucket-name" or "bucket-name/prefix/").
// Key format is always "bucket-name/prefix/". Returns error if loc.Path is empty.
// loc.Metric.ObjectStorage selects which aggregates/per-prefix info to extract.
func InspectObjectStorage(loc DataLocation) (*OSInspectResult, error) {
	if strings.TrimSpace(loc.Path) == "" {
		return nil, fmt.Errorf("path must specify a bucket")
	}

	provider, err := NewS3Provider(loc)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 provider: %w", err)
	}
	defer CloseS3Provider(provider) //nolint:errcheck — a close failure cannot change the result

	bucket, prefix := ParseBucketAndKey(loc.Path)
	objects, err := provider.ListObjects(prefix)
	if err != nil {
		return nil, Obj{Kind: ObjectKindObject, Owner: bucket, Name: prefix}.Fail(ActionList, err)
	}

	m := osMetric(loc)

	// scanRoot is always included in results regardless of whether objects exist there.
	scanRoot := bucket + "/"
	if prefix != "" {
		scanRoot = bucket + "/" + strings.TrimSuffix(prefix, "/") + "/"
	}

	prefixes := extractFolderPrefixes(bucket, objects)

	seen := make(map[string]struct{}, len(prefixes)+1)
	seen[scanRoot] = struct{}{}
	for _, p := range prefixes {
		seen[p] = struct{}{}
	}

	all := make([]string, 0, len(seen))
	for p := range seen {
		all = append(all, p)
	}
	sort.Strings(all)

	// Aggregates and per-prefix direct stats, computed in a single pass.
	var total int64
	var objTotal int
	counts := map[string]int{}
	perPrefix := map[string]objPrefixAgg{}
	if m.needObjectScan() {
		for _, obj := range objects {
			if m.totalSize {
				total += obj.Size
			}
			if m.objectCount {
				objTotal++
			}
			if m.extensionCount {
				counts[extKey(obj.Key)]++
			}
			if m.prefixModTime || m.prefixObjectCount || m.prefixObjectSize {
				dp := directPrefix(bucket, obj.Key)
				pa := perPrefix[dp]
				pa.count++
				pa.size += obj.Size
				pa.latest = laterMod(pa.latest, obj.LastModified)
				perPrefix[dp] = pa
			}
		}
	}

	result := &OSInspectResult{Path: scanRoot}
	for _, fp := range all {
		if !loc.Filter.MatchPath(filter.Rel(scanRoot, fp), 0, true) {
			continue
		}
		if loc.Filter != nil && loc.Filter.MaxDepth > 0 {
			// depth 0 = bucket root, depth 1 = first level prefix, etc.
			rel := strings.TrimSuffix(strings.TrimPrefix(fp, bucket+"/"), "/")
			depth := 0
			if rel != "" {
				depth = strings.Count(rel, "/") + 1
			}
			if depth > loc.Filter.MaxDepth {
				continue
			}
		}
		pa := perPrefix[fp] // zero value when the prefix has no direct objects
		entry := OSEntry{Key: fp}
		if m.prefixModTime {
			entry.LastModified = pa.latest
		}
		if m.prefixObjectCount {
			c := pa.count
			entry.ObjectCount = &c
		}
		if m.prefixObjectSize {
			s := pa.size
			entry.Size = &s
		}
		result.Folders = append(result.Folders, entry)
	}

	if m.totalSize {
		result.TotalSize = &total
	}
	if m.objectCount {
		result.ObjectCount = &objTotal
	}
	if m.extensionCount {
		result.ExtensionCounts = counts
	}
	return result, nil
}

// extractFolderPrefixes returns unique folder prefixes derived from object keys,
// each formatted as "bucketName/path/to/folder/". The scan root is NOT added here —
// callers must add it explicitly. Prefixes are returned sorted.
func extractFolderPrefixes(bucketName string, objects []ObjectInfo) []string {
	seen := make(map[string]struct{})
	for _, obj := range objects {
		// Trim leading/trailing slashes to avoid empty segments.
		key := strings.Trim(obj.Key, "/")
		parts := strings.Split(key, "/")
		if len(parts) <= 1 {
			// Top-level file — no intermediate folder prefix.
			continue
		}
		// Remove last segment (filename); everything before it is a folder prefix.
		dirs := parts[:len(parts)-1]
		for i := range dirs {
			seen[bucketName+"/"+strings.Join(dirs[:i+1], "/")+"/"] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for fp := range seen {
		result = append(result, fp)
	}
	sort.Strings(result)
	return result
}
