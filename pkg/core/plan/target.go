package plan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex"
	"github.com/rs/zerolog/log"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	sourcemodel "github.com/cloud-barista/cm-centipede/dmdl/source-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/client/beetle"
	"github.com/cloud-barista/cm-centipede/pkg/client/honeybee"
	"github.com/cloud-barista/cm-centipede/pkg/core/migration"
)

// ValidationError is returned for logical plan validation failures (400).
// External-system errors (honeybee/beetle unreachable) are returned as plain errors (500).
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func validationErr(format string, args ...any) *ValidationError {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// dataKind is the normalised connection-type category used for type-match validation.
type dataKind string

const (
	kindSSH           dataKind = "ssh"
	kindObjectStorage dataKind = "objectStorage"
	kindDBMS          dataKind = "dbms"
)

// dstKind resolves the dataKind of the destination ConnectionRef.
// For honeybee destinations the honeybee API is called each time.
func dstKind(ref commonmodel.ConnectionRef) (dataKind, error) {
	switch ref.Source {
	case commonmodel.ConnectionSourceBeetleSSH, commonmodel.ConnectionSourceSSH:
		return kindSSH, nil
	case commonmodel.ConnectionSourceBeetleObjectStorage, commonmodel.ConnectionSourceMinio:
		return kindObjectStorage, nil
	case commonmodel.ConnectionSourceBeetleDB, commonmodel.ConnectionSourceDB:
		return kindDBMS, nil
	case commonmodel.ConnectionSourceHoneybee:
		cfg, err := honeybee.GetConnectionInfo(*ref.Honeybee)
		if err != nil {
			return "", fmt.Errorf("get honeybee connection info: %w", err)
		}
		switch cfg.ConnType {
		case honeybee.ConnTypeSSH:
			return kindSSH, nil
		case honeybee.ConnTypeObjectStorage:
			return kindObjectStorage, nil
		case honeybee.ConnTypeDBMS:
			return kindDBMS, nil
		default:
			return "", fmt.Errorf("unrecognised honeybee connection type %q", cfg.ConnType)
		}
	}
	return "", fmt.Errorf("unknown connection source %q", ref.Source)
}

// dbmsIdentity holds the normalised DBMS engine type needed for validation.
type dbmsIdentity struct {
	dbType string // "mysql" | "mariadb" | "postgresql" | "mongodb"
}

// dbmsIdentityOf extracts the DBMS identity (engine type) from a DBMS
// ConnectionRef. honeybee carries the engine in its config; beetleDb is a
// managed cloud RDB whose engine comes from cm-beetle; db is inline.
func dbmsIdentityOf(ref commonmodel.ConnectionRef) (dbmsIdentity, error) {
	switch ref.Source {
	case commonmodel.ConnectionSourceHoneybee:
		cfg, err := honeybee.GetConnectionInfo(*ref.Honeybee)
		if err != nil {
			return dbmsIdentity{}, fmt.Errorf("get honeybee config for DB: %w", err)
		}
		return dbmsIdentity{dbType: cfg.DBType}, nil

	case commonmodel.ConnectionSourceBeetleDB:
		if ref.BeetleDB == nil {
			return dbmsIdentity{}, fmt.Errorf("beetleDb ref is nil")
		}
		acc, err := beetle.GetRDBMSAccessInfo(*ref.BeetleDB)
		if err != nil {
			return dbmsIdentity{}, err
		}
		return dbmsIdentity{dbType: acc.DBType}, nil

	case commonmodel.ConnectionSourceDB:
		if ref.DB == nil {
			return dbmsIdentity{}, fmt.Errorf("db config is nil")
		}
		return dbmsIdentity{dbType: ref.DB.DBMSType}, nil
	}
	return dbmsIdentity{}, fmt.Errorf("connection source %q is not a DBMS type", ref.Source)
}

// validateDBMSEngines rejects a heterogeneous pair up front. transx-ex refuses
// one too, but saying so at plan time costs nothing and reads better.
func validateDBMSEngines(srcRef, dstRef commonmodel.ConnectionRef) (string, error) {
	srcID, err := dbmsIdentityOf(srcRef)
	if err != nil {
		return "", fmt.Errorf("source DB identity: %w", err)
	}
	dstID, err := dbmsIdentityOf(dstRef)
	if err != nil {
		return "", fmt.Errorf("target DB identity: %w", err)
	}
	if srcID.dbType != "" && dstID.dbType != "" && srcID.dbType != dstID.dbType {
		return "", validationErr("dbType mismatch: source=%q target=%q (cross-DBMS migration is not supported)",
			srcID.dbType, dstID.dbType)
	}
	return srcID.dbType, nil
}

// validateDBMSVersion asks transx-ex whether the target can accept a dump taken
// from the source. transx-ex runs the same check for itself just before a
// migration starts; calling it here only moves the answer forward to plan time.
//
// A pair transx-ex cannot compare is reported rather than judged: a version it
// could not parse is not evidence of anything, so the plan proceeds.
func validateDBMSVersion(srcRef, dstRef commonmodel.ConnectionRef, database, dbType string) error {
	srcLoc, err := migration.ResolveDBMSLocation(srcRef, database, dbType)
	if err != nil {
		return fmt.Errorf("resolve source DB location: %w", err)
	}
	dstLoc, err := migration.ResolveDBMSLocation(dstRef, database, dbType)
	if err != nil {
		return fmt.Errorf("resolve target DB location: %w", err)
	}

	vc, err := transxex.CheckVersion(srcLoc, dstLoc)
	if err != nil {
		log.Warn().Err(err).Msg("cannot read both server versions; skipping the version check")
		return nil
	}
	if vc.Downgrade {
		return validationErr(
			"target DB version %q is older than source %q (downgrade migration is not supported)",
			vc.Target, vc.Source)
	}
	if !vc.Comparable {
		log.Debug().
			Str("srcVer", vc.Source).Str("dstVer", vc.Target).
			Msg("server versions are not comparable; skipping the version check")
	}
	return nil
}

// validateDBMSTargetReachable checks, at plan time, that every target database
// this plan needs either exists already or can be created (see
// migration.EnsureTargetDatabase). Creation is centipede's responsibility, and
// the conditions on it are knowable before the migration runs.
func validateDBMSTargetReachable(dstRef commonmodel.ConnectionRef, dstLoc transxex.DBMSLocation) error {
	switch dstRef.Source {
	case commonmodel.ConnectionSourceBeetleDB, commonmodel.ConnectionSourceDB:
	default:
		// honeybee is a source-only connection.
		return validationErr("connection source %q cannot be a DBMS migration target", dstRef.Source)
	}

	exists, err := migration.TargetDatabaseExists(dstRef, dstLoc)
	if err != nil {
		log.Warn().Err(err).Msg("cannot list target databases; deferring the creation check to execution")
		return nil
	}
	if exists {
		return nil // already provisioned — nothing to create
	}

	// MongoDB is exempt: there is no empty database to provision, and no way to
	// ask for one. A MongoDB database begins to exist when its first collection is
	// written, so it cannot be listed beforehand and cannot be "provisioned on the
	// provider first" - a managed MongoDB offers no create-database API (NCP has
	// none), and neither does the wire protocol. transx-ex says the same thing
	// from its own side: CreateDatabase for MongoDB logs "no-op without a
	// collection to create" and returns, so even the onprem branch below would
	// create nothing. The restore brings the database into existence by writing
	// into it, which is the only way there is.
	if dstLoc.DBMSType == transxex.DBMSTypeMongoDB {
		return nil
	}

	switch dstRef.Source {
	case commonmodel.ConnectionSourceBeetleDB:
		// cm-beetle creates a logical database inside the managed instance, so
		// an absent one is not an error here.
	case commonmodel.ConnectionSourceDB:
		if dstRef.DB == nil || !strings.EqualFold(dstRef.DB.ProviderName, commonmodel.ProviderOnPrem) {
			return validationErr(
				"target database %q does not exist; centipede creates one only for providerName %q — provision it on the provider first",
				dstLoc.Database, commonmodel.ProviderOnPrem)
		}
	}
	return nil
}

// selectMigrationPath resolves the single (srcPath, dstPath) to migrate for one
// source entry. scanRoot is the FSInfo/ObjectStorageInfo.Path (topmost folder);
// folders are the child folder/bucket paths listed under it.
//
//   - tm == nil            → migrate the scan root (topmost) to the same path.
//   - tm.SrcName matches   → migrate that path; DstName empty falls back to SrcName.
//     (matched against the scan root or any listed folder)
//   - no match             → ok=false; the caller reports a 400.
func selectMigrationPath(scanRoot string, folders []string, tm *commonmodel.TargetMapping) (src, dst string, ok bool) {
	if tm == nil {
		return scanRoot, scanRoot, true
	}
	candidates := append([]string{scanRoot}, folders...)
	for _, c := range candidates {
		if c == tm.SrcName {
			if tm.DstName != "" {
				return tm.SrcName, tm.DstName, true
			}
			return tm.SrcName, tm.SrcName, true
		}
	}
	return "", "", false
}

// resolveObjectStorageDstPath returns the DstPath an object storage destination
// should carry, given the one the mapping produced.
//
// For a beetleObjectStorage destination the bucket is decided by osId, and the
// first segment of DstPath is discarded: transx-ex builds its Tumblebug provider
// from the nsId/osId pair and never passes that segment to it (NewS3Provider
// checks only that it is non-empty, then calls NewTumblebugProvider without it),
// while the executor uses what follows it as the key prefix.
//
// Left as it comes out of the mapping, that segment is the *source* bucket's
// name — a bucket nothing writes to, printed in the plan and stored in the
// migration record as though it were the target. So an unmapped destination
// takes the osId instead.
//
// The transfer is unchanged: "raw-data/" and "cpbt-aws-bucket" both parse to an
// empty key prefix, and validation reads DstPath the same way transfer does.
// What changes is that the plan names the bucket the objects land in, and that a
// caller wanting a prefix writes "<osId>/archive/" rather than a source bucket
// name it does not mean.
//
// Two things are deliberately left alone:
//   - a DstName the caller supplied — the prefix is theirs to choose;
//   - every other destination — for minio and honeybee targets the first
//     segment IS the bucket, so rewriting it would retarget the transfer.
func resolveObjectStorageDstPath(dst commonmodel.ConnectionRef, tm *commonmodel.TargetMapping, dstPath string) string {
	if dst.Source != commonmodel.ConnectionSourceBeetleObjectStorage || dst.BeetleOS == nil {
		return dstPath
	}
	if tm != nil && strings.TrimSpace(tm.DstName) != "" {
		return dstPath
	}
	return dst.BeetleOS.OsID
}

// validateStorageMapping rejects a mapping that names nothing, or that carries
// PostgreSQL schema pairs where they have no meaning.
func validateStorageMapping(domain string, tm *commonmodel.TargetMapping) error {
	if tm == nil {
		return nil
	}
	if strings.TrimSpace(tm.SrcName) == "" {
		return validationErr("%s targetMapping.srcName is required when a filter is supplied", domain)
	}
	if len(tm.PgSchemas) > 0 {
		return validationErr("%s targetMapping.pgSchemas is DBMS-only", domain)
	}
	return nil
}

// validateRsyncCompatibleRules rejects path-filter rules that cannot be expressed
// as native rsync arguments. filesystem→objectstorage transfers apply the source
// filter on the rsync (ssh→local) relay step, where rsync supports size filters
// only as an "exclude" with a >/>=/</<= threshold; glob rules are always supported.
// (objectstorage→filesystem is unaffected — its filter runs on the S3 side.)
// validateSizeGateOrdering rejects a filesystem rule list whose result would
// depend on an ordering rsync does not have.
//
// The pipeline is first-match-wins: an "include" stops evaluation, so a rule
// below it never runs. rsync has no such precedence for size — --max-size and
// --min-size are GLOBAL gates applied to every file, independent of the
// --include/--exclude list and of where the rule sat. So a pipeline like
//
//	include glob  *.keep
//	exclude size  > 1048576
//
// keeps a 2 MB "backup.keep" when the pipeline is read in order, and drops it
// when rsync runs it. The plan would say one thing and the transfer do another,
// and validation — which reads the plan — would then report the file as missing
// from the destination.
//
// Only an include ABOVE a size rule is ambiguous. Below one it is unreachable
// for anything the size rule already matched, which is what rsync does too, so
// the two agree and the order is left alone.
func validateSizeGateOrdering(rules []commonmodel.PathFilterRule) error {
	seenInclude := false
	for _, r := range rules {
		if r.Action == commonmodel.FilterActionInclude {
			seenInclude = true
			continue
		}
		if seenInclude && r.Type == commonmodel.PathRuleSize {
			return validationErr(
				"fileSystemFilter: a size rule below an include rule is ambiguous — rsync applies size as a global gate, so the include cannot keep a file the size rule matches. Move the size rule above every include, or express the limit as a glob rule")
		}
	}
	return nil
}

// validateIncludeHasEffect rejects an include rule that cannot change what is
// transferred.
//
// The pipeline keeps anything no rule matched, so an include only ever means
// something when an exclude below it would otherwise have dropped the file. With
// no exclude below, every item reaching that include is kept either way — by the
// include if it matches, by the default if it does not — and deleting the rule
// would transfer exactly the same files.
//
// The mistake this catches is a whitelist written without its catch-all:
//
//	include glob  *.go            ← "only the .go files"
//
// which silently copies the whole folder. "exclude glob *" at the end is what
// makes the include a whitelist, and nothing adds it for the caller: an include
// above an exclude is also how a single file is carved back OUT of a broader
// exclude ("include keep.log" then "exclude *.log"), and the two intentions
// cannot be told apart from the rules alone. So the caller has to say which one
// it meant, and this reports the case where it said neither.
func validateIncludeHasEffect(domain string, rules []commonmodel.PathFilterRule) error {
	lastExclude := -1
	for i, r := range rules {
		if r.Action == commonmodel.FilterActionExclude {
			lastExclude = i
		}
	}
	for i, r := range rules {
		if r.Action == commonmodel.FilterActionInclude && i > lastExclude {
			return validationErr(
				"%s: the include rule at position %d has no effect — the pipeline keeps whatever no rule matched, so with no exclude rule below it every file is kept either way. To transfer only what it names, add {\"action\":\"exclude\",\"type\":\"glob\",\"pattern\":\"*\"} as the last rule; to carve an exception out of a broader exclude, move that exclude below this include",
				domain, i+1)
		}
	}
	return nil
}

func validateRsyncCompatibleRules(rules []commonmodel.PathFilterRule) error {
	for _, r := range rules {
		if r.Type != "size" {
			continue
		}
		if r.Action != "exclude" {
			return validationErr(
				"filesystem→objectstorage does not support a size filter with action %q (rsync supports size only as 'exclude'); use glob or an objectstorage source", r.Action)
		}
		switch r.Op {
		case ">", ">=", "<", "<=":
		default:
			return validationErr(
				"filesystem→objectstorage does not support size filter op %q (rsync supports only > >= < <=)", r.Op)
		}
	}
	return nil
}

// buildDBMigrationInfos turns the request's per-database filter entries into the
// execution items for one source connection's databases.
//
// With no filter every source database migrates under its own name with no
// rules. With one, only the databases it names migrate — the entries are a
// selection as much as a rule set — and every name must match a database the
// source actually holds.
func buildDBMigrationInfos(
	available []string,
	dbType string,
	filter *commonmodel.DBMSFilter,
) ([]targetmodel.DBMigrationInfo, error) {

	if filter == nil || len(filter.Databases) == 0 {
		out := make([]targetmodel.DBMigrationInfo, 0, len(available))
		for i, name := range available {
			out = append(out, targetmodel.DBMigrationInfo{
				Order:   i + 1,
				SrcName: name,
				DstName: name,
			})
		}
		return out, nil
	}

	have := make(map[string]bool, len(available))
	for _, n := range available {
		have[n] = true
	}

	seenSrc := make(map[string]bool, len(filter.Databases))
	seenDst := make(map[string]bool, len(filter.Databases))
	out := make([]targetmodel.DBMigrationInfo, 0, len(filter.Databases))

	for _, e := range filter.Databases {
		srcName := strings.TrimSpace(e.TargetMapping.SrcName)
		if srcName == "" {
			return nil, validationErr("dbmsFilter.databases[].targetMapping.srcName is required")
		}
		if !have[srcName] {
			// Only report a mismatch against a connection that actually lists
			// databases; a source that listed none is filtered elsewhere.
			return nil, validationErr("targetMapping.srcName %q matches no database in the source model", srcName)
		}
		if seenSrc[srcName] {
			return nil, validationErr("duplicate targetMapping.srcName %q in dbmsFilter.databases", srcName)
		}
		seenSrc[srcName] = true

		dstName := strings.TrimSpace(e.TargetMapping.DstName)
		if dstName == "" {
			dstName = srcName
		}
		if seenDst[dstName] {
			return nil, validationErr(
				"duplicate targetMapping.dstName %q in dbmsFilter.databases (merging databases is not supported)", dstName)
		}
		seenDst[dstName] = true

		if err := validatePgSchemas(dbType, srcName, e.TargetMapping.PgSchemas); err != nil {
			return nil, err
		}

		out = append(out, targetmodel.DBMigrationInfo{
			Order:     len(out) + 1,
			SrcName:   srcName,
			DstName:   dstName,
			PgSchemas: e.TargetMapping.PgSchemas,
			Rules:     e.Rules,
		})
	}
	return out, nil
}

// validatePgSchemas checks the PostgreSQL schema pairs of one database entry.
// The names themselves are matched against the source inventory by the caller's
// discovery result only when one is available; what is always checkable is the
// engine and the shape of the pairing.
func validatePgSchemas(dbType, database string, pairs []commonmodel.PgSchemaMapping) error {
	if len(pairs) == 0 {
		return nil
	}
	if dbType != string(commonmodel.DBMSTypePostgreSQL) {
		return validationErr("pgSchemas is PostgreSQL-only, but database %q is %s", database, dbType)
	}
	seenSrc := make(map[string]bool, len(pairs))
	seenDst := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		src := strings.TrimSpace(p.SrcName)
		if src == "" {
			return validationErr("pgSchemas[].srcName is required (database %q)", database)
		}
		if seenSrc[src] {
			return validationErr("duplicate pgSchemas[].srcName %q (database %q)", src, database)
		}
		seenSrc[src] = true

		dst := strings.TrimSpace(p.DstName)
		if dst == "" {
			dst = src
		}
		if seenDst[dst] {
			return validationErr(
				"duplicate pgSchemas[].dstName %q (database %q); merging schemas is not supported", dst, database)
		}
		seenDst[dst] = true
	}
	return nil
}

// sourceEntry is one entry of the request's source model, found by the honeybee
// connection id that produced it. Exactly one of the three pointers is non-nil,
// and kind says which.
type sourceEntry struct {
	kind dataKind
	fs   *sourcemodel.SourceFileSystemModel
	os   *sourcemodel.SourceObjectStorageModel
	db   *sourcemodel.SourceDBModel
}

// indexSource maps every entry of the source model by its connection id, which
// is how a plan entry names one.
//
// A honeybee connection has exactly one ConnType, so an id belongs to exactly
// one of the three domains and the index settles which without asking honeybee.
// An id in two of them is a source model no discovery produced, and it is
// reported rather than resolved: either answer would be a guess, and the guess
// decides which filter fields are legal.
func indexSource(src sourcemodel.SourceDataMigrationProperty) (map[string]sourceEntry, error) {
	index := make(map[string]sourceEntry,
		len(src.FileSystems)+len(src.ObjectStorages)+len(src.Databases))

	add := func(key string, e sourceEntry) error {
		if prev, ok := index[key]; ok {
			return validationErr(
				"source connection %q appears in more than one domain of the source model (%s and %s)",
				key, prev.kind, e.kind)
		}
		index[key] = e
		return nil
	}

	for i := range src.FileSystems {
		e := &src.FileSystems[i]
		if err := add(SrcConnKey(e.Connection), sourceEntry{kind: kindSSH, fs: e}); err != nil {
			return nil, err
		}
	}
	for i := range src.ObjectStorages {
		e := &src.ObjectStorages[i]
		if err := add(SrcConnKey(e.Connection), sourceEntry{kind: kindObjectStorage, os: e}); err != nil {
			return nil, err
		}
	}
	for i := range src.Databases {
		e := &src.Databases[i]
		if err := add(SrcConnKey(e.Connection), sourceEntry{kind: kindDBMS, db: e}); err != nil {
			return nil, err
		}
	}
	return index, nil
}

// validateEntryDomain checks that a plan entry sets only what its source domain
// gives meaning to.
//
// An ignored filter is worse than a rejected one: the caller reads back a plan
// that migrated everything and believes their filter ran. The same goes for a
// strategy, which only a filesystem transfer has anywhere to apply.
func validateEntryDomain(kind dataKind, e model.PlanEntry) string {
	given := []struct {
		name string
		set  bool
	}{
		{"fileSystemFilter", e.FileSystemFilter != nil},
		{"objectStorageFilter", e.ObjectStorageFilter != nil},
		{"dbmsFilter", e.DBMSFilter != nil},
		{"fileSystemStrategy", e.FileSystemStrategy != ""},
	}

	var allowed map[string]bool
	switch kind {
	case kindSSH:
		allowed = map[string]bool{"fileSystemFilter": true, "fileSystemStrategy": true}
	case kindObjectStorage:
		allowed = map[string]bool{"objectStorageFilter": true}
	case kindDBMS:
		allowed = map[string]bool{"dbmsFilter": true}
	}

	for _, f := range given {
		if f.set && !allowed[f.name] {
			return fmt.Sprintf("%s is not valid for a %s source", f.name, kind)
		}
	}
	return ""
}

// buildFileSystemEntry turns one plan entry naming a filesystem source into its
// target model.
func buildFileSystemEntry(
	e model.PlanEntry,
	srcFS sourcemodel.SourceFileSystemModel,
	dstType dataKind,
) (targetmodel.MigrationFileSystemModel, error) {

	// Normalise the filesystem transfer strategy; empty defaults to relay.
	strategy := e.FileSystemStrategy
	if strategy == "" {
		strategy = commonmodel.StrategyRelay
	}

	var rules []commonmodel.PathFilterRule
	var mapping *commonmodel.TargetMapping
	if f := e.FileSystemFilter; f != nil {
		rules = f.Rules
		mapping = &f.TargetMapping
		if err := validateStorageMapping("fileSystemFilter", mapping); err != nil {
			return targetmodel.MigrationFileSystemModel{}, err
		}
		// Every filesystem source transfers with rsync, whichever destination it
		// lands on, so this one is checked before the destination is even known.
		if err := validateSizeGateOrdering(rules); err != nil {
			return targetmodel.MigrationFileSystemModel{}, err
		}
		if err := validateIncludeHasEffect("fileSystemFilter", rules); err != nil {
			return targetmodel.MigrationFileSystemModel{}, err
		}
	}

	// Destination must be filesystem (SSH→SSH) or object storage (filesystem→S3
	// cross-storage). DBMS destinations are invalid for filesystem sources.
	var fsDstType string
	switch dstType {
	case kindSSH:
		fsDstType = commonmodel.StorageTypeFilesystem
	case kindObjectStorage:
		fsDstType = commonmodel.StorageTypeObjectStorage
		// filesystem→objectstorage applies the source filter on the rsync
		// (ssh→local) relay step, which cannot express every size rule.
		if err := validateRsyncCompatibleRules(rules); err != nil {
			return targetmodel.MigrationFileSystemModel{}, err
		}
	default:
		return targetmodel.MigrationFileSystemModel{}, validationErr(
			"a filesystem source requires an SSH or object storage destination, but dstConnection is %q", dstType)
	}

	folderPaths := make([]string, 0, len(srcFS.Folders))
	for _, folder := range srcFS.Folders {
		folderPaths = append(folderPaths, folder.Path)
	}
	srcPath, dstPath, ok := selectMigrationPath(srcFS.Path, folderPaths, mapping)
	if !ok {
		return targetmodel.MigrationFileSystemModel{}, validationErr(
			"fileSystemFilter.targetMapping.srcName %q matches no folder of this source", mapping.SrcName)
	}
	// filesystem→objectstorage lands in a bucket too, so an unmapped DstPath
	// would otherwise carry the source *directory* ("/testdata") into the
	// bucket-name position. Same treatment, same reasons.
	dstPath = resolveObjectStorageDstPath(e.DstConnection, mapping, dstPath)

	return targetmodel.MigrationFileSystemModel{
		PlanEntryID:   newPlanEntryID(),
		SrcConnection: srcFS.Connection,
		DstConnection: e.DstConnection,
		DstType:       fsDstType,
		Strategy:      strategy,
		Folders: []targetmodel.FSMigrationInfo{{
			Order:   1,
			SrcPath: srcPath,
			DstPath: dstPath,
			Rules:   rules,
		}},
	}, nil
}

// buildObjectStorageEntry turns one plan entry naming an object storage source
// into its target model.
func buildObjectStorageEntry(
	e model.PlanEntry,
	srcOS sourcemodel.SourceObjectStorageModel,
	dstType dataKind,
) (targetmodel.MigrationObjectStorageModel, error) {

	var rules []commonmodel.PathFilterRule
	var mapping *commonmodel.TargetMapping
	if f := e.ObjectStorageFilter; f != nil {
		rules = f.Rules
		mapping = &f.TargetMapping
		if err := validateStorageMapping("objectStorageFilter", mapping); err != nil {
			return targetmodel.MigrationObjectStorageModel{}, err
		}
		// Not rsync-specific: the default-keep pipeline is shared, so a
		// whitelist missing its catch-all misfires on a bucket just as it does
		// on a folder.
		if err := validateIncludeHasEffect("objectStorageFilter", rules); err != nil {
			return targetmodel.MigrationObjectStorageModel{}, err
		}
	}

	// Destination must be object storage (S3→S3) or filesystem (objectstorage→
	// filesystem cross-storage). DBMS destinations are invalid.
	var osDstType string
	switch dstType {
	case kindObjectStorage:
		osDstType = commonmodel.StorageTypeObjectStorage
	case kindSSH:
		// objectstorage→filesystem applies the source filter on the S3 download
		// step (generic matcher), so all glob/size rules are supported.
		osDstType = commonmodel.StorageTypeFilesystem
	default:
		return targetmodel.MigrationObjectStorageModel{}, validationErr(
			"an object storage source requires an object storage or SSH destination, but dstConnection is %q", dstType)
	}

	bucketKeys := make([]string, 0, len(srcOS.Folders))
	for _, folder := range srcOS.Folders {
		bucketKeys = append(bucketKeys, folder.Key)
	}
	srcPath, dstPath, ok := selectMigrationPath(srcOS.Path, bucketKeys, mapping)
	if !ok {
		return targetmodel.MigrationObjectStorageModel{}, validationErr(
			"objectStorageFilter.targetMapping.srcName %q matches no bucket of this source", mapping.SrcName)
	}
	dstPath = resolveObjectStorageDstPath(e.DstConnection, mapping, dstPath)

	return targetmodel.MigrationObjectStorageModel{
		PlanEntryID:   newPlanEntryID(),
		SrcConnection: srcOS.Connection,
		DstConnection: e.DstConnection,
		DstType:       osDstType,
		Buckets: []targetmodel.ObjectMigrationInfo{{
			Order:   1,
			SrcPath: srcPath,
			DstPath: dstPath,
			Rules:   rules,
		}},
	}, nil
}

// buildDBEntry turns one plan entry naming a DBMS source into its target model.
//
// A source model already pairs one connection with the databases read through
// it, which is the shape MigrationDBModel wants, so no regrouping is needed.
func buildDBEntry(
	e model.PlanEntry,
	srcDB sourcemodel.SourceDBModel,
	dstType dataKind,
) (targetmodel.MigrationDBModel, error) {

	if dstType != kindDBMS {
		return targetmodel.MigrationDBModel{}, validationErr(
			"a DBMS source requires a DBMS destination, but dstConnection is %q", dstType)
	}

	dbType, err := validateDBMSEngines(srcDB.Connection, e.DstConnection)
	if err != nil {
		return targetmodel.MigrationDBModel{}, err
	}

	available := make([]string, 0, len(srcDB.Databases))
	for _, info := range srcDB.Databases {
		available = append(available, info.Database)
	}

	items, err := buildDBMigrationInfos(available, dbType, e.DBMSFilter)
	if err != nil {
		return targetmodel.MigrationDBModel{}, err
	}
	if len(items) == 0 {
		return targetmodel.MigrationDBModel{}, validationErr(
			"no database selected for migration; dbmsFilter.databases names none of this source's databases")
	}

	for _, it := range items {
		if err := validateDBMSVersion(srcDB.Connection, e.DstConnection, it.SrcName, dbType); err != nil {
			return targetmodel.MigrationDBModel{}, err
		}
		dstLoc, err := migration.ResolveDBMSLocation(e.DstConnection, it.DstName, dbType)
		if err != nil {
			return targetmodel.MigrationDBModel{}, fmt.Errorf("resolve target DB location: %w", err)
		}
		if err := validateDBMSTargetReachable(e.DstConnection, dstLoc); err != nil {
			return targetmodel.MigrationDBModel{}, err
		}
	}

	return targetmodel.MigrationDBModel{
		PlanEntryID:   newPlanEntryID(),
		SrcConnection: srcDB.Connection,
		DstConnection: e.DstConnection,
		DBType:        commonmodel.DBMSType(dbType),
		Databases:     items,
	}, nil
}

// BuildTargetDataMigrationModel validates the plan request and builds the
// TargetDataMigrationModel from it.
//
// One plan entry produces one target model entry. The request pairs each entry
// of the source model with its own destination and its own filter, so a
// group-level source model — several connections discovered under one source
// group — describes several migrations rather than several sources crowding
// onto one destination.
//
// Validation errors (→ 400 in the controller):
//   - a srcConnection naming no entry of the source model, or a source entry no
//     plan entry names
//   - a filter or strategy that the entry's source domain gives no meaning to
//   - unsupported source/target type combination (DBMS is same-type only;
//     filesystem↔objectstorage cross-storage is allowed)
//   - a targetMapping.srcName matching nothing in the source entry it applies to
//   - DBMS engine mismatch, or a target older than the source
//   - a target database that neither exists nor can be created
//   - filesystem→objectstorage with an rsync-incompatible size filter
//   - two entries writing to the same destination
//   - a file system entry migrating "/" to "/"
//
// External-system errors (→ 500 in the controller) are wrapped and returned as-is.
func BuildTargetDataMigrationModel(req model.TargetPlanReq) (targetmodel.TargetDataMigrationModel, error) {
	index, err := indexSource(req.Source.SourceDataMigrationModel)
	if err != nil {
		return targetmodel.TargetDataMigrationModel{}, err
	}

	var result targetmodel.TargetDataMigrationProperty
	referenced := make(map[string]bool, len(index))

	for i, entry := range req.Plans {
		key := SrcConnKey(entry.SrcConnection)
		src, ok := index[key]
		if !ok {
			return targetmodel.TargetDataMigrationModel{}, validationErr(
				"plans[%d].srcConnection: honeybee connection %q names no entry of the source model", i, key)
		}
		referenced[key] = true

		if msg := validateEntryDomain(src.kind, entry); msg != "" {
			return targetmodel.TargetDataMigrationModel{}, validationErr("plans[%d]: %s", i, msg)
		}

		// Resolved per entry rather than once: each entry names its own
		// destination, and for a honeybee destination this asks honeybee.
		dstType, err := dstKind(entry.DstConnection)
		if err != nil {
			return targetmodel.TargetDataMigrationModel{}, fmt.Errorf(
				"plans[%d].dstConnection: resolve destination connection type: %w", i, err)
		}

		switch src.kind {
		case kindSSH:
			built, err := buildFileSystemEntry(entry, *src.fs, dstType)
			if err != nil {
				return targetmodel.TargetDataMigrationModel{}, wrapEntryErr(i, err)
			}
			result.FileSystems = append(result.FileSystems, built)

		case kindObjectStorage:
			built, err := buildObjectStorageEntry(entry, *src.os, dstType)
			if err != nil {
				return targetmodel.TargetDataMigrationModel{}, wrapEntryErr(i, err)
			}
			result.ObjectStorages = append(result.ObjectStorages, built)

		case kindDBMS:
			built, err := buildDBEntry(entry, *src.db, dstType)
			if err != nil {
				return targetmodel.TargetDataMigrationModel{}, wrapEntryErr(i, err)
			}
			result.Databases = append(result.Databases, built)
		}
	}

	// A source entry nothing names is reported rather than skipped. Skipping it
	// is what the previous shape did, and a source that was discovered, sent and
	// then silently left behind is the failure this request shape exists to end.
	for key := range index {
		if !referenced[key] {
			return targetmodel.TargetDataMigrationModel{}, validationErr(
				"source connection %q is in the source model but no plans entry names it", key)
		}
	}

	out := targetmodel.TargetDataMigrationModel{TargetDataMigrationModel: result}
	if msg := DuplicateDestination(out); msg != "" {
		return targetmodel.TargetDataMigrationModel{}, validationErr("%s", msg)
	}
	if msg := RootToRoot(out); msg != "" {
		return targetmodel.TargetDataMigrationModel{}, validationErr("%s", msg)
	}
	return out, nil
}

// wrapEntryErr prefixes an entry's error with which entry it came from, keeping
// a *ValidationError one so the controller still answers 400.
func wrapEntryErr(i int, err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return validationErr("plans[%d]: %s", i, ve.Error())
	}
	return fmt.Errorf("plans[%d]: %w", i, err)
}
