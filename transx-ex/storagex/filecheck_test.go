package storagex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestListAndHashFiles_Local(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755))
	must(os.WriteFile(filepath.Join(root, "a.txt"), []byte("aa"), 0o644))
	must(os.WriteFile(filepath.Join(root, "sub", "deep", "b.txt"), []byte("b"), 0o644))
	when := time.Unix(1700000000, 500)
	must(os.Chtimes(filepath.Join(root, "a.txt"), when, when))
	if runtime.GOOS != "windows" {
		must(os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link")))
	}

	loc := DataLocation{StorageType: StorageTypeFilesystem, Path: root, Filesystem: &FilesystemAccess{AccessType: AccessTypeLocal}}
	files, err := ListFiles(context.Background(), loc)
	must(err)
	want := []FileInfo{{Path: "a.txt", Size: 2, ModTime: 1700000000}, {Path: "sub/deep/b.txt", Size: 1}}
	if len(files) != len(want) {
		t.Fatalf("ListFiles = %+v, want %+v (no link, no directory)", files, want)
	}
	for i, f := range files {
		if f.Path != want[i].Path || f.Size != want[i].Size || (want[i].ModTime != 0 && f.ModTime != want[i].ModTime) {
			t.Errorf("ListFiles[%d] = %+v, want %+v", i, f, want[i])
		}
	}

	var seen []string
	hashes, err := HashFiles(context.Background(), loc, []string{"a.txt", "sub/deep/b.txt"}, func(p string) { seen = append(seen, p) })
	must(err)
	sum := sha256.Sum256([]byte("aa"))
	if hashes["a.txt"] != hex.EncodeToString(sum[:]) || len(hashes) != 2 || len(seen) != 2 {
		t.Errorf("HashFiles = %v, onFile saw %v", hashes, seen)
	}
	if _, err := HashFiles(context.Background(), loc, []string{"gone.txt"}, nil); err == nil {
		t.Error("a file that cannot be read fails the hash on this machine")
	}
	if h, err := HashFiles(context.Background(), loc, nil, nil); err != nil || len(h) != 0 {
		t.Errorf("no paths = %v, %v", h, err)
	}
}

func TestFileCheckSSH_Refusals(t *testing.T) {
	for name, loc := range map[string]DataLocation{
		"object storage": {StorageType: StorageTypeObjectStorage, Filesystem: &FilesystemAccess{AccessType: AccessTypeLocal}},
		"no access":      {StorageType: StorageTypeFilesystem},
		"ssh without":    {Filesystem: &FilesystemAccess{AccessType: AccessTypeSSH}},
		"unknown access": {Filesystem: &FilesystemAccess{AccessType: "ftp"}},
	} {
		if _, err := ListFiles(context.Background(), loc); err == nil {
			t.Errorf("%s: ListFiles should refuse", name)
		}
	}
}

func TestParseFileRecord(t *testing.T) {
	f, ok := parseFileRecord("12\t1700000000.75\tdir/a\tb.txt")
	if !ok || f.Size != 12 || f.ModTime != 1700000000 || f.Path != "dir/a\tb.txt" {
		t.Errorf("parseFileRecord = %+v, %v", f, ok)
	}
	for _, bad := range []string{"", "12", "12\t1", "x\t1\tp", "1\tx\tp", "1\t1\t"} {
		if _, ok := parseFileRecord(bad); ok {
			t.Errorf("parseFileRecord(%q) should fail", bad)
		}
	}
}

func TestParseSHA256Line(t *testing.T) {
	cases := []struct{ line, hash, path string }{
		{"abc  dir/a b.txt", "abc", "dir/a b.txt"},
		{`\abc  new\nline\\x`, "abc", "new\nline\\x"},
	}
	for _, c := range cases {
		h, p, ok := parseSHA256Line(c.line)
		if !ok || h != c.hash || p != c.path {
			t.Errorf("parseSHA256Line(%q) = %q, %q, %v", c.line, h, p, ok)
		}
	}
	if _, _, ok := parseSHA256Line("abc"); ok {
		t.Error("a line without the separator is not a hash line")
	}
}

func TestTailWriter(t *testing.T) {
	w := &tailWriter{limit: 4}
	w.Write([]byte("abc"))
	w.Write([]byte("defg"))
	if got := w.String(); got != "defg" {
		t.Errorf("tailWriter = %q, want the last 4 bytes", got)
	}
}
