package storagex

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// =============================================================================
// Object keys as local paths
// =============================================================================

// An object key is any UTF-8 string, and a download turns it into a path on
// local disk. Several keys a bucket happily holds make no sense as one:
//
//   - a/../../x climbs out of the destination directory, on any OS;
//   - data/sub/ is the 0-byte marker a console leaves for a folder, not a file;
//   - a/b and a/./b are the same file.
//
// Each was either an error deep into the transfer or, worse, a file written
// somewhere else or over another one without a word. They are now resolved, or
// refused together, before the destination is touched.

// objectPath is where one object of a download lands.
type objectPath struct {
	Local  string // path under the destination root
	Folder bool   // a folder marker: a directory to create, no file
}

// objectLocalPath maps key, listed under prefix, to its place under root. It
// returns ok=false for a key that the listing matched but that does not belong
// to prefix: a listing is by string prefix, so the location bucket/file.txt
// also lists file.txt.bak, and bucket/dir also lists dirx/y.
//
// A prefix that names one object (key == prefix) puts that object directly
// under root by its base name, as the stream transfer does (streamDstKey).
func objectLocalPath(root, prefix, key string) (p objectPath, ok bool, err error) {
	if !keyUnderPrefix(prefix, key) {
		return objectPath{}, false, nil
	}
	var rel string
	if key == prefix && !strings.HasSuffix(prefix, "/") {
		rel = path.Base(key)
	} else {
		rel = strings.TrimLeft(strings.TrimPrefix(key, prefix), "/")
	}

	folder := strings.HasSuffix(rel, "/")
	if rel == "" {
		// The prefix itself, or the marker of the folder it names.
		return objectPath{Local: root, Folder: true}, true, nil
	}

	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return objectPath{}, false, fmt.Errorf("key %q leads outside the destination directory", key)
	}
	return objectPath{Local: filepath.Join(root, filepath.FromSlash(clean)), Folder: folder}, true, nil
}

// keyUnderPrefix reports whether key belongs to the location prefix names: the
// object prefix itself, or one below it as below a folder. A listing matches by
// string prefix, so for "dir" it also returns "dirx/y", and for "file.txt" also
// "file.txt.bak"; neither is part of the location.
func keyUnderPrefix(prefix, key string) bool {
	if prefix == "" || strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(key, prefix)
	}
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

// objectLocalPaths resolves every key in keys, in order, and refuses the lot if
// any cannot be placed: a key that cannot be a local path, or two keys that
// land on the same file. paths[i] is the place of keys[i], and skip[i] is set
// for a key that does not belong to prefix.
func objectLocalPaths(root, prefix string, keys []string) (paths []objectPath, skip []bool, err error) {
	paths = make([]objectPath, len(keys))
	skip = make([]bool, len(keys))
	owner := make(map[string]string, len(keys))
	var problems []string

	for i, key := range keys {
		p, ok, perr := objectLocalPath(root, prefix, key)
		if perr != nil {
			problems = append(problems, perr.Error())
			continue
		}
		if !ok {
			skip[i] = true
			continue
		}
		paths[i] = p
		if p.Folder {
			continue
		}
		if other, dup := owner[p.Local]; dup {
			problems = append(problems, fmt.Sprintf("keys %q and %q land on the same file %s", other, key, p.Local))
			continue
		}
		owner[p.Local] = key
	}

	// A file and a folder of the same name cannot both exist: data/sub as an
	// object and data/sub/x, or the marker data/sub/, under it.
	for i, p := range paths {
		if skip[i] || p.Local == "" {
			continue
		}
		if p.Folder {
			if other, clash := owner[p.Local]; clash && p.Local != root {
				problems = append(problems, fmt.Sprintf("key %q marks %s as a folder, but key %q is a file there", keys[i], p.Local, other))
			}
		}
		for dir := filepath.Dir(p.Local); len(dir) > len(root); dir = filepath.Dir(dir) {
			if other, clash := owner[dir]; clash {
				problems = append(problems, fmt.Sprintf("key %q needs %s as a folder, but key %q is a file there", keys[i], dir, other))
				break
			}
		}
	}

	if len(problems) > 0 {
		const shown = 10
		msg := strings.Join(problems[:min(len(problems), shown)], "; ")
		if len(problems) > shown {
			msg += fmt.Sprintf("; and %d more", len(problems)-shown)
		}
		return nil, nil, fmt.Errorf("%d object key(s) cannot be downloaded: %s", len(problems), msg)
	}
	return paths, skip, nil
}
