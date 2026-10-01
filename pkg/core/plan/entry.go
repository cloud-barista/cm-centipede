package plan

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
)

// entry.go holds what identifies a plan entry: the key a source connection is
// matched by, the name an entry carries, and the two checks that a plan does not
// name the same entry or the same destination twice, nor migrate "/" to "/".
//
// They live apart from the builder because CreateMigration needs them too. A
// plan can arrive there without having been issued by POST /plans/target —
// edited, hand-written, issued by another instance — and the same answers have
// to hold for it.

// SrcConnKey returns the value a source ConnectionRef is matched by.
//
// The source model is produced by cm-honeybee, so a source ref is always a
// honeybee ref and the connection id identifies it completely. Matching is by
// this key alone — never by comparing the structs, whose other sub-fields are
// rejected on a source ref and would only add ways for an equal connection to
// compare unequal: a port left at zero rather than 22, a tlsMode spelled
// "" rather than "disable", a password with a typo in it.
//
// A ref that is not a honeybee one yields "", which matches no source entry.
// The controller rejects those before they reach here, so this returning ""
// means the plan was built without that check rather than that "" is a key.
func SrcConnKey(ref commonmodel.ConnectionRef) string {
	if ref.Honeybee == nil {
		return ""
	}
	return ref.Honeybee.ConnectionID
}

// newPlanEntryID returns a fresh name for a plan entry.
//
// Eight hex characters, not a derived value. A deterministic id — hashing the
// source, destination and path — would let two runs of the same request produce
// the same plan, which nothing here compares, and would have to decide what part
// of a destination to hash: including the credentials makes a password change a
// different entry, excluding them needs an identity function of its own. It
// would also invite folding DuplicateDestination into an id collision, and that
// check reads better comparing the destinations themselves.
func newPlanEntryID() string { return uuid.New().String()[:8] }

// AssignMissingEntryIDs fills in the PlanEntryID of every entry that has none,
// in place.
//
// POST /plans/target names every entry it builds, so this is a no-op for a plan
// used as it was issued. It is not one for a plan that was edited or written by
// hand, which CreateMigration accepts on purpose — naming those entries before
// the record is stored means execution, logging and retry all read one thing,
// and the plan the caller can fetch back says what its logs point at.
func AssignMissingEntryIDs(p *targetmodel.TargetDataMigrationModel) {
	prop := &p.TargetDataMigrationModel
	for i := range prop.FileSystems {
		if prop.FileSystems[i].PlanEntryID == "" {
			prop.FileSystems[i].PlanEntryID = newPlanEntryID()
		}
	}
	for i := range prop.ObjectStorages {
		if prop.ObjectStorages[i].PlanEntryID == "" {
			prop.ObjectStorages[i].PlanEntryID = newPlanEntryID()
		}
	}
	for i := range prop.Databases {
		if prop.Databases[i].PlanEntryID == "" {
			prop.Databases[i].PlanEntryID = newPlanEntryID()
		}
	}
}

// DuplicateEntryID returns the first PlanEntryID carried by more than one entry
// of p, or "" when they are all distinct.
//
// Two entries under one name put their log lines, validation details and retry
// decisions on top of each other, which is the whole thing the name exists to
// prevent. Ids this server generates never collide; a hand-written plan that
// copied an entry can.
func DuplicateEntryID(p targetmodel.TargetDataMigrationModel) string {
	prop := p.TargetDataMigrationModel
	seen := make(map[string]bool,
		len(prop.FileSystems)+len(prop.ObjectStorages)+len(prop.Databases))

	check := func(id string) string {
		if id == "" {
			return "" // not yet named; AssignMissingEntryIDs handles those
		}
		if seen[id] {
			return id
		}
		seen[id] = true
		return ""
	}
	for _, e := range prop.FileSystems {
		if dup := check(e.PlanEntryID); dup != "" {
			return dup
		}
	}
	for _, e := range prop.ObjectStorages {
		if dup := check(e.PlanEntryID); dup != "" {
			return dup
		}
	}
	for _, e := range prop.Databases {
		if dup := check(e.PlanEntryID); dup != "" {
			return dup
		}
	}
	return ""
}

// dstKey is the identity of a destination for the purpose of spotting two
// entries that would write to the same place.
//
// It names the target, not the credentials used to reach it: two entries that
// differ only by password are the same destination, and a password does not
// belong in the error message this key ends up in either.
//
// The comparison it feeds is deliberately conservative. Two spellings of one
// destination — a port left at zero beside the same port written out — produce
// different keys and go unreported, which costs a duplicate nobody catches.
// Treating them as equal would cost a legitimate plan refused, and that is the
// worse of the two.
func dstKey(ref commonmodel.ConnectionRef) string {
	switch ref.Source {
	case commonmodel.ConnectionSourceHoneybee:
		if ref.Honeybee != nil {
			return "honeybee:" + ref.Honeybee.ConnectionID
		}
	case commonmodel.ConnectionSourceBeetleSSH:
		if r := ref.BeetleSSH; r != nil {
			return fmt.Sprintf("beetleSsh:%s/%s/%s", r.NsID, r.InfraID, r.NodeID)
		}
	case commonmodel.ConnectionSourceBeetleObjectStorage:
		if r := ref.BeetleOS; r != nil {
			return fmt.Sprintf("beetleObjectStorage:%s/%s", r.NsID, r.OsID)
		}
	case commonmodel.ConnectionSourceBeetleDB:
		if r := ref.BeetleDB; r != nil {
			return fmt.Sprintf("beetleDb:%s/%s", r.NsID, r.RdbmsID)
		}
	case commonmodel.ConnectionSourceSSH:
		if r := ref.SSH; r != nil {
			return fmt.Sprintf("ssh:%s@%s:%d", r.Username, r.Host, r.Port)
		}
	case commonmodel.ConnectionSourceMinio:
		if r := ref.Minio; r != nil {
			return fmt.Sprintf("minio:%s/%s", r.ProviderName, r.Endpoint)
		}
	case commonmodel.ConnectionSourceDB:
		if r := ref.DB; r != nil {
			return fmt.Sprintf("db:%s/%s:%d", r.DBMSType, r.Host, r.Port)
		}
	}
	// A ref whose sub-struct does not match its source: structural validation
	// reports that better than this would, so give it a key of its own rather
	// than one every malformed ref shares.
	return "unresolved:" + string(ref.Source)
}

// DuplicateDestination returns a description of the first destination two
// entries of p share, or "" when every one of them is distinct.
//
// Sharing a destination is how a multi-source plan quietly loses data. The
// entries run one after another, so the second overwrites what the first wrote;
// the validation that follows reads the destination back against one source at
// a time and reports the other source's files as not belonging there; and for
// DBMS a failure in the second entry can roll back or drop a database the first
// one filled.
//
// The destination is the connection and the path together, not the connection
// alone: a file system or object storage filter selects a single folder or
// bucket, so two entries naming the same source and destination with different
// paths are how one connection migrates more than one of them.
func DuplicateDestination(p targetmodel.TargetDataMigrationModel) string {
	prop := p.TargetDataMigrationModel
	seen := map[string]string{} // key → the entry that claimed it

	claim := func(entryID, key, path, what string) string {
		full := key + "|" + path
		if prev, ok := seen[full]; ok {
			return fmt.Sprintf("%s %q of plan entry %q is already the destination of plan entry %q",
				what, path, entryID, prev)
		}
		seen[full] = entryID
		return ""
	}

	for _, e := range prop.FileSystems {
		key := dstKey(e.DstConnection)
		for _, f := range e.Folders {
			if msg := claim(e.PlanEntryID, key, f.DstPath, "destination path"); msg != "" {
				return msg
			}
		}
	}
	for _, e := range prop.ObjectStorages {
		key := dstKey(e.DstConnection)
		for _, b := range e.Buckets {
			if msg := claim(e.PlanEntryID, key, b.DstPath, "destination path"); msg != "" {
				return msg
			}
		}
	}
	for _, e := range prop.Databases {
		key := dstKey(e.DstConnection)
		for _, d := range e.Databases {
			if msg := claim(e.PlanEntryID, key, d.DstName, "target database"); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// RootToRoot returns a description of the first file system entry of p that
// migrates "/" to "/", or "" when none does.
//
// That pair copies the source host's whole tree over the target's, /etc, /usr
// and /boot included, and leaves a target that no longer matches its own
// kernel, disks or network. There is no rollback for it, so it is refused
// rather than run.
func RootToRoot(p targetmodel.TargetDataMigrationModel) string {
	isRoot := func(p string) bool {
		trimmed := strings.TrimSpace(p)
		return trimmed != "" && path.Clean(trimmed) == "/"
	}

	for _, e := range p.TargetDataMigrationModel.FileSystems {
		// An empty DstType is a file system here, as MigrateFileSystem reads it.
		if e.DstType != "" && e.DstType != commonmodel.StorageTypeFilesystem {
			continue
		}
		for _, f := range e.Folders {
			if isRoot(f.SrcPath) && isRoot(f.DstPath) {
				return fmt.Sprintf("plan entry %q migrates %q to %q, which would overwrite the target's "+
					"system files; choose a source or destination directory below the root",
					e.PlanEntryID, f.SrcPath, f.DstPath)
			}
		}
	}
	return ""
}
