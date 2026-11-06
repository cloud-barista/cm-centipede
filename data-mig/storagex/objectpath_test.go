package storagex

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectLocalPath(t *testing.T) {
	root := filepath.FromSlash("/dst")
	for _, tc := range []struct {
		prefix, key string
		want        string // slash-separated under root; "" with ok=false for a skip
		folder      bool
		ok          bool
	}{
		{"", "a/b.txt", "/dst/a/b.txt", false, true},
		{"data/", "data/x/y.txt", "/dst/x/y.txt", false, true},
		{"data", "data/x/y.txt", "/dst/x/y.txt", false, true},
		{"data", "datax/y.txt", "", false, false},                      // a sibling the listing also matched
		{"dir/file.txt", "dir/file.txt", "/dst/file.txt", false, true}, // the location names one object
		{"dir/file.txt", "dir/file.txt.bak", "", false, false},
		{"data/", "data/sub/", "/dst/sub", true, true}, // a folder marker
		{"data/", "data/", "/dst", true, true},         // the prefix's own marker
		{"data/", "data/a/./b", "/dst/a/b", false, true},
		{"data/", "data/a/../b", "/dst/b", false, true}, // climbs, but stays inside
		{"", "/abs", "/dst/abs", false, true},
	} {
		p, ok, err := objectLocalPath(root, tc.prefix, tc.key)
		if err != nil {
			t.Errorf("%q under %q: %v", tc.key, tc.prefix, err)
			continue
		}
		if ok != tc.ok {
			t.Errorf("%q under %q: ok = %v, want %v", tc.key, tc.prefix, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if want := filepath.FromSlash(tc.want); p.Local != want || p.Folder != tc.folder {
			t.Errorf("%q under %q = %+v, want {%s %v}", tc.key, tc.prefix, p, want, tc.folder)
		}
	}
}

func TestObjectLocalPath_RefusesEscape(t *testing.T) {
	for _, key := range []string{"data/../x", "data/a/../../x", "data/.."} {
		if _, _, err := objectLocalPath("/dst", "data/", key); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%q: err = %v, want the outside-destination refusal", key, err)
		}
	}
}

func TestObjectLocalPaths(t *testing.T) {
	root := filepath.FromSlash("/dst")
	keys := []string{"p/a.txt", "p/sub/", "p/sub/b.txt", "px/other", "p/"}
	paths, skip, err := objectLocalPaths(root, "p", keys)
	if err != nil {
		t.Fatal(err)
	}
	if !skip[3] || skip[0] || skip[1] || skip[2] || skip[4] {
		t.Errorf("skip = %v, want only px/other skipped", skip)
	}
	if !paths[1].Folder || paths[1].Local != filepath.Join(root, "sub") {
		t.Errorf("marker = %+v", paths[1])
	}
}

func TestObjectLocalPaths_Refuses(t *testing.T) {
	for _, keys := range [][]string{
		{"p/a/b", "p/a/./b"},               // the same file twice
		{"p/data/sub", "p/data/sub/x.txt"}, // a file where a folder is needed
		{"p/data/sub", "p/data/sub/"},      // a file where a marker makes a folder
		{"p/ok", "p/../escape"},
	} {
		if _, _, err := objectLocalPaths("/dst", "p/", keys); err == nil {
			t.Errorf("%q should be refused", keys)
		}
	}
}

func TestObjectLocalPaths_ReportsAll(t *testing.T) {
	var keys []string
	for i := 0; i < 12; i++ {
		keys = append(keys, "p/../x"+strings.Repeat("y", i))
	}
	_, _, err := objectLocalPaths("/dst", "p/", keys)
	if err == nil || !strings.Contains(err.Error(), "12 object key(s)") || !strings.Contains(err.Error(), "and 2 more") {
		t.Errorf("err = %v", err)
	}
}
