package storagex

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

func TestLsPermString(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		want string
	}{
		{"dir 0755", os.ModeDir | 0o755, "drwxr-xr-x"},
		{"dir sticky 1777", os.ModeDir | os.ModeSticky | 0o777, "drwxrwxrwt"},
		{"dir sticky without exec", os.ModeDir | os.ModeSticky | 0o666, "drw-rw-rwT"},
		{"regular 0644", 0o644, "-rw-r--r--"},
		{"setuid 4755", os.ModeSetuid | 0o755, "-rwsr-xr-x"},
		{"setgid without exec", os.ModeSetgid | 0o640, "-rw-r-S---"},
		{"symlink", os.ModeSymlink | 0o777, "lrwxrwxrwx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lsPermString(c.mode); got != c.want {
				t.Errorf("lsPermString(%v) = %q, want %q", c.mode, got, c.want)
			}
		})
	}
}

// TestPathLess checks that sorting by pathLess reproduces filepath.Walk order:
// a parent precedes its children, and a separator sorts before any other
// character a name may start with.
func TestPathLess(t *testing.T) {
	paths := []string{"/data/a-b", "/data/a/c", "/data/a", "/data", "/data/b"}
	want := []string{"/data", "/data/a", "/data/a/c", "/data/a-b", "/data/b"}

	sort.Slice(paths, func(i, j int) bool { return pathLess(paths[i], paths[j]) })
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", paths, want)
		}
	}
}

func TestParseSSHDirRecords(t *testing.T) {
	// Records mirror `find -printf '%D\t%s\t%T@\t%M\t%p\0'`. The third record's
	// path contains a tab and a newline; the fourth is malformed noise.
	out := []byte(
		"2049\t4096\t1700000000.5\tdrwxr-xr-x\t/data\x00" +
			"2049\t4096\t1700000001\tdrwxrwxrwt\t/data/tmp\x00" +
			"2050\t8192\t1700000002\tdr-xr-xr-x\t/data/we\tird\nname\x00" +
			"find: '/data/secret': Permission denied\x00",
	)

	dirs, order := parseSSHDirRecords(out)

	wantOrder := []string{"/data", "/data/tmp", "/data/we\tird\nname"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %q, want %q", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %q, want %q", order, wantOrder)
		}
	}

	root := dirs["/data"]
	if !root.devOK || root.dev != 2049 {
		t.Errorf("root dev = %d (ok=%v), want 2049", root.dev, root.devOK)
	}
	if root.size != 4096 {
		t.Errorf("root size = %d, want 4096", root.size)
	}
	if root.perms != "drwxr-xr-x" {
		t.Errorf("root perms = %q, want %q", root.perms, "drwxr-xr-x")
	}
	if got := root.modTime.UTC().Format(time.RFC3339); got != "2023-11-14T22:13:20Z" {
		t.Errorf("root modTime = %q, want %q", got, "2023-11-14T22:13:20Z")
	}

	// The tab inside the path must stay part of the path, not split into a field.
	if odd := dirs["/data/we\tird\nname"]; odd.dev != 2050 {
		t.Errorf("dev of tab/newline path = %d, want 2050", odd.dev)
	}
}

func TestParseFindEpoch(t *testing.T) {
	if got := parseFindEpoch("1700000000.5").UTC().Format(time.RFC3339); got != "2023-11-14T22:13:20Z" {
		t.Errorf("parseFindEpoch = %q, want 2023-11-14T22:13:20Z", got)
	}
	if !parseFindEpoch("not-a-number").IsZero() {
		t.Error("parseFindEpoch of invalid input should be the zero time")
	}
}

// TestInspectFilesystemSSHRequiresAbsolutePath guards the absolute-path
// contract of FSEntry.Path: a relative path would resolve against the remote
// login directory, so it is rejected before any connection is attempted.
func TestInspectFilesystemSSHRequiresAbsolutePath(t *testing.T) {
	loc := DataLocation{
		StorageType: StorageTypeFilesystem,
		Path:        "relative/dir",
		Filesystem: &FilesystemAccess{
			AccessType: AccessTypeSSH,
			SSH:        &SSHConfig{Host: "127.0.0.1", Username: "nobody"},
		},
	}
	if _, err := InspectFilesystem(loc); err == nil {
		t.Fatal("expected an error for a relative ssh path")
	}
}

// TestInspectLocalFilesystemRegularFilesOnly verifies that only regular files
// feed the metrics, matching `find -type f` on the SSH path.
func TestInspectLocalFilesystemRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root+"/a.txt", root+"/link.txt"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	yes := true
	loc := DataLocation{
		StorageType: StorageTypeFilesystem,
		Path:        root,
		Filesystem:  &FilesystemAccess{AccessType: AccessTypeLocal},
		Metric: &MetricOption{
			Filesystem: &FilesystemMetric{
				TotalSize:       &yes,
				FileCount:       &yes,
				ExtensionCount:  &yes,
				FolderFileCount: &yes,
				FolderFileSize:  &yes,
			},
		},
	}

	res, err := InspectFilesystem(loc)
	if err != nil {
		t.Fatal(err)
	}
	if res.FileCount == nil || *res.FileCount != 1 {
		t.Errorf("FileCount = %v, want 1 (symlink excluded)", fmtIntPtr(res.FileCount))
	}
	if res.TotalSize == nil || *res.TotalSize != 5 {
		t.Errorf("TotalSize = %v, want 5", res.TotalSize)
	}
	if res.ExtensionCounts[".txt"] != 1 {
		t.Errorf("ExtensionCounts[.txt] = %d, want 1", res.ExtensionCounts[".txt"])
	}
	if len(res.Folders) != 1 {
		t.Fatalf("Folders = %+v, want the scan root only", res.Folders)
	}
	if res.Folders[0].FileCount == nil || *res.Folders[0].FileCount != 1 {
		t.Errorf("folder FileCount = %v, want 1", fmtIntPtr(res.Folders[0].FileCount))
	}
	if res.Folders[0].Permissions == "" || res.Folders[0].Permissions[0] != 'd' {
		t.Errorf("folder Permissions = %q, want an ls-style dir string", res.Folders[0].Permissions)
	}
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprint(*p)
}
