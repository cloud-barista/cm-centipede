package validation

import (
	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/core/migration"
	"github.com/cloud-barista/cm-centipede/transx-ex"
)

// filter.go teaches validation what the migration was told to leave behind.
//
// Both ends are re-read in full, so a file the filter dropped is absent from the
// destination by design. Compared without the filter it reads as "file missing
// in destination" — the same false positive compareSchemaObjects already avoids
// on the DBMS side, one per excluded file.
//
// The rules need no plumbing: the plan stores them per folder and per bucket
// (FSMigrationInfo.Rules / OSMigrationInfo.Rules) and they arrive here inside the
// migration model.
//
// ── The pipeline decides, and it decides once ───────────────────────────────
// Every verdict below is transx-ex's own PathFilterOption.MatchPath, reached
// through the same migration.ToTransxFilter the transfer uses.
//
// MatchPath is where rsync's semantics live: the path is relative to the
// migrated folder or prefix, an excluded directory takes its subtree, and size
// rules take no part in judging a directory. This file used to reproduce all
// three, reading executor-rsync.go from the outside to stay in step with it —
// which held only until the translation moved, and gave no help at all to
// inspect, which reproduced none of them. Asking the library the question is
// what keeps the transfer, this validation and inspect on one answer.
type migrationFilter struct {
	// files judges a file or object against the whole pipeline.
	files *transxex.PathFilterOption
	// sized records that a size rule is present, so a caller that would have to
	// pay for file sizes only pays when they change an outcome.
	sized bool
}

// newMigrationFilter builds the verdict helper for one folder's or bucket's
// rules. A nil filter (no rules) excludes nothing, so callers need no nil check
// beyond what the methods already do.
func newMigrationFilter(rules []commonmodel.PathFilterRule) (*migrationFilter, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	files, err := migration.ToTransxFilter(rules)
	if err != nil {
		return nil, err
	}

	f := &migrationFilter{files: files}
	for _, r := range rules {
		if r.Type == commonmodel.PathRuleSize {
			f.sized = true
			break
		}
	}
	return f, nil
}

// needsSize reports whether a size rule is in play. Without one the caller can
// skip collecting file sizes, which on the filesystem side is a second command
// on a remote host.
func (f *migrationFilter) needsSize() bool {
	return f != nil && f.sized
}

// excludesObject reports whether the filter dropped this object. relKey is the
// key with the migrated prefix removed, which is what a rule is written against.
func (f *migrationFilter) excludesObject(relKey string, size int64) bool {
	if f == nil || f.files == nil {
		return false
	}
	return !f.files.MatchPath(relKey, size, false)
}

// excludesFile reports whether the filter kept this file out of the transfer.
// relPath is relative to the migrated folder, which is the path rsync matches
// its patterns against.
func (f *migrationFilter) excludesFile(relPath string, size int64) bool {
	if f == nil || f.files == nil {
		return false
	}
	return !f.files.MatchPath(relPath, size, false)
}
