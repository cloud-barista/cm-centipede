// Command gendata generates test data into OpenTofu-prepared infrastructure:
// object storage buckets, VM filesystems, and databases.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/config"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/bucket"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/database"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/filesystem"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/generate"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/layout"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/progress"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatalf("gendata: %v", err)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "config/config.json", "path to config.json")
		targetFlag = flag.String("target", "all", "bucket|filesystem|database|all")
		provider   = flag.String("provider", "aws", "cloud provider: aws|ncp")
		engineFlag = flag.String("engine", "all", "database engine: mysql|mariadb|postgresql|mongodb|all")
		inputsFile = flag.String("inputs-file", "", "JSON with the connection info: a path, or - for stdin (required)")
		dryRun     = flag.Bool("dry-run", false, "generate only; skip upload/transfer/db load")
		force      = flag.Bool("force", false, "skip the pre-flight checks: existing data (allow overwrite/DROP) and capacity")
		cleanup    = flag.Bool("cleanup", false, "delete data instead of generating it; --target bucket only, and it empties the whole bucket")
		largeMB    = flag.Int("large-file-mb", 0, "size of each large file in MB, 0 for none (overrides "+config.EnvLargeFileMB+" and config.json)")
		largeN     = flag.Int("large-files", 0, "how many large files, 0 for none (overrides "+config.EnvLargeFiles+" and config.json)")
		largeOnly  = flag.Bool("large-only", false, "add only the large files, next to the data already there; bucket/filesystem only")
		envKeys    = flag.Bool("env-keys", false, "print the environment variables gendata reads, one per line, and exit")
		manifestTo = flag.String("manifest", filepath.Join("runs", "last-run.json"), "where the run summary is written")
	)
	flag.Parse()

	// Answered before anything else is required: ../scripts/gen-data.sh asks for
	// this list to check .env for misspelled GENDATA_ keys, and it has no
	// connection info to offer at that point - nor any need of one.
	if *envKeys {
		for _, k := range config.EnvKeys() {
			fmt.Println(k)
		}
		return nil
	}

	// The values are assembled by whichever environment prepared the
	// resources and piped in, so that the secrets among them never land in a
	// file. ../scripts/gen-data.sh does that.
	if *inputsFile == "" {
		return fmt.Errorf("--inputs-file is required (- reads stdin); run gendata through ./scripts/gen-data.sh")
	}

	prov, err := parseProvider(*provider)
	if err != nil {
		return err
	}
	targets, err := parseTargets(*targetFlag)
	if err != nil {
		return err
	}
	engines, err := parseEngines(*engineFlag)
	if err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	// config.json holds the defaults; .env, sourced by gen-data.sh and inherited
	// through the environment, holds what this deployment wants instead.
	origins, err := config.ApplyEnv(cfg)
	if err != nil {
		return err
	}
	// Only a flag given on the command line wins over .env; the defaults the flag
	// package fills in for the rest are not an answer from anyone.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "large-file-mb":
			cfg.Large.FileMB, origins.Large = *largeMB, "flag"
		case "large-files":
			cfg.Large.Files, origins.Large = *largeN, "flag"
		}
	})
	if cfg.Large.FileMB < 0 {
		return fmt.Errorf("--large-file-mb=%d is negative (0 for none)", cfg.Large.FileMB)
	}
	if cfg.Large.Files < 0 {
		return fmt.Errorf("--large-files=%d is negative (0 for none)", cfg.Large.Files)
	}
	if *largeOnly {
		if *cleanup {
			return fmt.Errorf("--large-only and --cleanup do opposite things; pick one")
		}
		if cfg.Large.FileMB == 0 || cfg.Large.Files == 0 {
			return fmt.Errorf("--large-only: nothing to add, %d files of %d MB (%s and %s in .env, or --large-file-mb and --large-files)",
				cfg.Large.Files, cfg.Large.FileMB, config.EnvLargeFileMB, config.EnvLargeFiles)
		}
	}
	tmpDir, err := config.TmpDir()
	if err != nil {
		return err
	}

	inputs, err := config.LoadInputsFile(*inputsFile)
	if err != nil {
		return err
	}

	plan := layout.NewPlan(cfg.Layout.FolderDepth, cfg.Layout.FolderBreadth)
	ctx := context.Background()

	log.Printf("provider: %s", prov)
	log.Printf("targets : %s", strings.Join(sortedKeys(targets), ", "))

	// Emptying a target is the opposite of the run below, not a step in it: there
	// is nothing to generate, no pre-flight to pass - it exists to find data - and
	// no manifest to write, since the run manifest records what was put in place.
	if *cleanup {
		return runCleanup(ctx, cfg, inputs, targets, prov, *dryRun)
	}

	// The folder tree is what bucket keys and SFTP paths are built from; a
	// database load places nothing in it, so reporting it there only invites the
	// question of which directories the fixtures went into.
	if targets["bucket"] || targets["filesystem"] {
		log.Printf("layout  : depth=%d breadth=%d (%d leaf dirs)", cfg.Layout.FolderDepth, cfg.Layout.FolderBreadth, plan.LeafCount())
	}

	// How much data this run is about, and where that number came from. Worth a
	// line each: with config.json, .env and flags all able to set a size, "what
	// is actually in effect" is otherwise only answerable by replaying the
	// precedence rules by hand.
	//
	// The dummy line reports the file plan after the file limit has had its say,
	// not the sizes asked for: over the limit files grow, formats can be skipped
	// and png/gif cut, and only the plan says what will actually be written.
	alloc := generate.Allocate(dummyOptions(cfg.Dummy, cfg.Layout.MaxFiles))
	if *largeOnly {
		// Checked before anything is looked up: --target defaults to all, and
		// quietly loading the database fixture alongside would be a surprise.
		if targets["database"] {
			return fmt.Errorf("--large-only adds files to bucket and filesystem only, not database " +
				"(--target defaults to all, so name the targets explicitly)")
		}
		alloc = generate.Allocation{}
	}
	var large []generate.Large
	if targets["bucket"] || targets["filesystem"] {
		large = generate.PlanLarge(cfg.Large.FileMB, cfg.Large.Files)
	}
	if (targets["bucket"] || targets["filesystem"]) && !*largeOnly {
		maxFiles := "no limit"
		if alloc.MaxFiles > 0 {
			maxFiles = fmt.Sprintf("max %d", alloc.MaxFiles)
		}
		log.Printf("dummy   : %d MB in %d files (%s from %s, sizes from %s): %s",
			alloc.TotalBytes()/generate.MiB, alloc.TotalFiles(), maxFiles, origins.MaxFiles, origins.Dummy, alloc)
		if len(alloc.Skipped) > 0 {
			log.Printf("[warn] dummy: max %d files is fewer than the formats on - skipped %s, their size moved to the others",
				alloc.MaxFiles, strings.Join(alloc.Skipped, ", "))
		}
	}
	if len(large) > 0 {
		only := ""
		if *largeOnly {
			only = ", only these - the dummy set is left as it is"
		}
		log.Printf("large   : %d × %d MB under %s/ (from %s%s)", len(large), cfg.Large.FileMB, generate.LargeDir, origins.Large, only)
	} else if cfg.Large.FileMB == 0 && cfg.Large.Files > 0 {
		// The other way round - a size with 0 files - is the default .env and
		// says nothing worth a warning.
		log.Printf("[warn] large: %d files asked for, but the size is 0 - none added (%s or --large-file-mb)",
			cfg.Large.Files, config.EnvLargeFileMB)
	}
	bulkPlan := database.BulkPlan{}
	if targets["database"] {
		if bulkPlan, err = database.PlanBulk(bulkOptions(cfg.Database)); err != nil {
			return err
		}
		if bulkPlan.Empty() {
			log.Printf("database: shop_db fixture only (%s = 0, from %s)", config.EnvDBSizeMB, origins.Database)
		} else {
			log.Printf("database: fixture + %d MB bulk, %s (from %s)", cfg.Database.SizeMB, bulkPlan, origins.Database)
		}
	}

	// ---- pre-flight ----
	switch {
	case *force:
		log.Printf("[warn] --force set: skipping existing-data and capacity checks")
	case *largeOnly:
		if err := largePreflight(ctx, cfg, inputs, targets, prov, large); err != nil {
			return err
		}
	default:
		if err := capacityPreflight(cfg, inputs, targets, alloc, generate.LargeBytes(large)); err != nil {
			return err
		}
		if err := preflight(ctx, cfg, inputs, targets, engines, prov); err != nil {
			return err
		}
	}

	man := newManifest(prov, targets, cfg, origins, *dryRun)
	man.Files = alloc
	man.LargeOnly = *largeOnly
	if len(large) > 0 {
		man.Large = &largeManifest{FileMB: cfg.Large.FileMB, Files: len(large), Dir: generate.LargeDir}
	}

	// ---- generate dummy files once (shared by bucket + filesystem) ----
	var (
		genDir string
		files  []generate.File
	)
	needGen := (targets["bucket"] || targets["filesystem"]) && !*largeOnly
	if needGen {
		if alloc.TotalFiles() == 0 {
			log.Printf("[warn] dummy sizes are all 0; nothing to generate for bucket/filesystem")
		} else {
			log.Printf("generating dummy files...")
			pg := progress.New("generate", alloc.TotalFiles(), alloc.TotalBytes())
			genDir, files, err = generate.Generate(alloc, tmpDir, cfg.Generate.Workers, pg.Add)
			pg.Finish()
			if err != nil {
				return err
			}
			defer os.RemoveAll(genDir)
			log.Printf("generated %d files in %s", len(files), genDir)
		}
	}

	// ---- dispatch ----
	if targets["bucket"] {
		if !*largeOnly {
			if err := doBucket(ctx, cfg, inputs, prov, files, plan, *dryRun, man); err != nil {
				return err
			}
		}
		if err := doLargeBucket(ctx, cfg, inputs, prov, large, *dryRun, man); err != nil {
			return err
		}
	}
	if targets["filesystem"] {
		if !*largeOnly {
			if err := doFilesystem(ctx, cfg, inputs, files, plan, *dryRun, man); err != nil {
				return err
			}
		}
		if err := doLargeFilesystem(ctx, cfg, inputs, large, *dryRun, man); err != nil {
			return err
		}
	}
	if targets["database"] {
		if err := doDatabase(ctx, cfg, inputs, engines, prov, bulkPlan, *dryRun, man); err != nil {
			return err
		}
	}

	// ---- manifest ----
	manPath, err := writeManifest(*manifestTo, man)
	if err != nil {
		log.Printf("[warn] failed to write manifest: %v", err)
	} else {
		log.Printf("manifest: %s", manPath)
	}
	log.Printf("done.")
	return nil
}

// ---------------------------------------------------------------------------
// cleanup
// ---------------------------------------------------------------------------

// runCleanup empties the prepared target instead of filling it.
//
// Only the bucket is handled, because it is the one target whose leftovers block
// a destroy: ncloud_objectstorage_bucket has no force_destroy, so NCP answers
// DeleteBucket with 409 BucketNotEmpty until the bucket is empty. tofu/aws/bucket
// sets force_destroy itself, so AWS never needs this - it stays allowed, since
// the same bucket may be reached through either CSP's credentials.
//
// It empties the WHOLE bucket rather than BasePrefix alone. What blocks the
// destroy is any object at all, including what a migration test wrote outside the
// prefix gendata uploads under.
func runCleanup(ctx context.Context, cfg *config.Config, in *config.Inputs, targets map[string]bool, provider string, dryRun bool) error {
	for _, t := range sortedKeys(targets) {
		if t != "bucket" {
			return fmt.Errorf("--cleanup handles --target bucket only, not %q "+
				"(--target defaults to all, so name the target explicitly)", t)
		}
	}
	if in.BucketName == "" {
		return fmt.Errorf("--cleanup: the connection info carries no bucket name")
	}

	p := bucketParams(cfg, in, provider)
	if dryRun {
		keys, err := bucket.Keys(ctx, p, "")
		if err != nil {
			return err
		}
		log.Printf("[dry-run] bucket: would delete %d objects from s3://%s (the whole bucket)", len(keys), in.BucketName)
		return nil
	}

	log.Printf("bucket: deleting every object in s3://%s ...", in.BucketName)
	n, err := bucket.RemoveAll(ctx, p, "")
	if err != nil {
		return fmt.Errorf("bucket cleanup (%d deleted): %w", n, err)
	}
	log.Printf("bucket: deleted %d objects; s3://%s is empty", n, in.BucketName)
	return nil
}

// ---------------------------------------------------------------------------
// pre-flight
// ---------------------------------------------------------------------------

func preflight(ctx context.Context, cfg *config.Config, in *config.Inputs, targets map[string]bool, engines []database.Engine, provider string) error {
	var problems []string
	if targets["bucket"] {
		if err := bucket.CheckExisting(ctx, bucketParams(cfg, in, provider)); err != nil {
			if errors.Is(err, bucket.ErrDataExists) {
				problems = append(problems, err.Error())
			} else {
				return fmt.Errorf("bucket pre-flight: %w", err)
			}
		}
	}
	if targets["filesystem"] {
		if err := filesystem.CheckExisting(ctx, fsParams(cfg, in)); err != nil {
			if errors.Is(err, filesystem.ErrDataExists) {
				problems = append(problems, err.Error())
			} else {
				return fmt.Errorf("filesystem pre-flight: %w", err)
			}
		}
	}
	if targets["database"] {
		for _, e := range engines {
			dc, ok := dbConfig(in, e)
			if !ok {
				continue
			}
			if err := database.CheckExisting(ctx, dc); err != nil {
				if errors.Is(err, database.ErrDataExists) {
					problems = append(problems, err.Error())
				} else {
					return fmt.Errorf("database(%s) pre-flight: %w", e, err)
				}
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("existing data found (use --force to overwrite):\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	log.Printf("pre-flight: no existing data")
	return nil
}

// largePreflight is the pre-flight of a --large-only run, which adds to whatever
// is there rather than starting from nothing. It refuses only what this run
// would overwrite - the large files themselves - and measures the VM's free
// space instead of comparing the run with the volume size: the dataset already
// on the volume is exactly what that comparison leaves out.
func largePreflight(ctx context.Context, cfg *config.Config, in *config.Inputs, targets map[string]bool, provider string, large []generate.Large) error {
	var problems []string
	if targets["bucket"] {
		if err := bucket.CheckLarge(ctx, bucketParams(cfg, in, provider), large); err != nil {
			if !errors.Is(err, bucket.ErrDataExists) {
				return fmt.Errorf("bucket pre-flight: %w", err)
			}
			problems = append(problems, err.Error())
		}
	}
	if targets["filesystem"] {
		p := fsParams(cfg, in)
		if err := filesystem.CheckLarge(ctx, p, large); err != nil {
			if !errors.Is(err, filesystem.ErrDataExists) {
				return fmt.Errorf("filesystem pre-flight: %w", err)
			}
			problems = append(problems, err.Error())
		}
		total, used, err := filesystem.Usage(ctx, p)
		switch {
		case errors.Is(err, filesystem.ErrNoStatVFS):
			log.Printf("[warn] filesystem: %v - free space not checked", err)
		case err != nil:
			return fmt.Errorf("filesystem pre-flight: %w", err)
		default:
			want := uint64(generate.LargeBytes(large))
			if limit := uint64(float64(total) * capacityHeadroom); used+want > limit {
				problems = append(problems, fmt.Sprintf(
					"filesystem: %d MB of large files on top of %d MB in use, on a %d MB volume (max %d MB in use, %.0f%% of the volume)",
					want/generate.MiB, used/generate.MiB, total/generate.MiB, limit/generate.MiB, capacityHeadroom*100))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the large files cannot be added (use --force to try anyway):\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	log.Printf("pre-flight: none of the large files exist yet")
	return nil
}

// ---------------------------------------------------------------------------
// targets
// ---------------------------------------------------------------------------

func doBucket(ctx context.Context, cfg *config.Config, in *config.Inputs, provider string, files []generate.File, plan layout.Plan, dryRun bool, man *manifest) error {
	prefix := cfg.ObjectStorage.BasePrefix
	man.Bucket = &bucketManifest{Name: in.BucketName, Prefix: prefix}
	if dryRun {
		log.Printf("[dry-run] bucket: would upload %d objects to s3://%s/%s", len(files), in.BucketName, prefix)
		return nil
	}
	if len(files) == 0 {
		log.Printf("bucket: no files to upload (skipped)")
		return nil
	}
	log.Printf("bucket: uploading %d objects to s3://%s/%s, %d at a time ...", len(files), in.BucketName, prefix, cfg.ObjectStorage.Concurrency)
	p := bucketParams(cfg, in, provider)
	pg := progress.New("bucket", len(files), totalSize(files))
	p.OnFile = pg.Add
	n, err := bucket.Upload(ctx, p, files, plan)
	pg.Finish()
	man.Bucket.Objects = n
	if err != nil {
		return fmt.Errorf("bucket upload (%d/%d ok): %w", n, len(files), err)
	}
	return nil
}

func doFilesystem(ctx context.Context, cfg *config.Config, in *config.Inputs, files []generate.File, plan layout.Plan, dryRun bool, man *manifest) error {
	base := fsBasePath(cfg, in)
	man.Filesystem = &fsManifest{Host: in.VMHost, BasePath: base}
	if dryRun {
		log.Printf("[dry-run] filesystem: would transfer %d files to %s@%s:%s", len(files), in.VMUser, in.VMHost, base)
		return nil
	}
	if len(files) == 0 {
		log.Printf("filesystem: no files to transfer (skipped)")
		return nil
	}
	log.Printf("filesystem: transferring %d files to %s@%s:%s, %d at a time over %d SSH connections ...",
		len(files), in.VMUser, in.VMHost, base, cfg.Filesystem.Concurrency, cfg.Filesystem.Connections)
	p := fsParams(cfg, in)
	pg := progress.New("filesystem", len(files), totalSize(files))
	p.OnFile = pg.Add
	n, err := filesystem.Transfer(ctx, p, files, plan)
	pg.Finish()
	man.Filesystem.Files = n
	if err != nil {
		return fmt.Errorf("filesystem transfer (%d/%d ok): %w", n, len(files), err)
	}
	return nil
}

// doLargeBucket uploads the large files, streamed rather than generated first.
// Nothing to do when none are configured.
func doLargeBucket(ctx context.Context, cfg *config.Config, in *config.Inputs, provider string, large []generate.Large, dryRun bool, man *manifest) error {
	if len(large) == 0 {
		return nil
	}
	prefix := cfg.ObjectStorage.BasePrefix
	if man.Bucket == nil {
		man.Bucket = &bucketManifest{Name: in.BucketName, Prefix: prefix}
	}
	if dryRun {
		log.Printf("[dry-run] bucket: would upload %d large files to s3://%s/%s%s/", len(large), in.BucketName, prefix, generate.LargeDir)
		return nil
	}
	log.Printf("bucket: uploading %d large files to s3://%s/%s%s/, one at a time ...", len(large), in.BucketName, prefix, generate.LargeDir)
	p := bucketParams(cfg, in, provider)
	pg := progress.New("bucket(large)", len(large), generate.LargeBytes(large))
	p.OnFile = pg.Add
	n, err := bucket.UploadLarge(ctx, p, large, pg.AddBytes)
	pg.Finish()
	man.Large.BucketObjects = n
	if err != nil {
		return fmt.Errorf("bucket large upload (%d/%d ok): %w", n, len(large), err)
	}
	return nil
}

// doLargeFilesystem transfers the large files, streamed rather than generated
// first. Nothing to do when none are configured.
func doLargeFilesystem(ctx context.Context, cfg *config.Config, in *config.Inputs, large []generate.Large, dryRun bool, man *manifest) error {
	if len(large) == 0 {
		return nil
	}
	base := fsBasePath(cfg, in)
	if man.Filesystem == nil {
		man.Filesystem = &fsManifest{Host: in.VMHost, BasePath: base}
	}
	if dryRun {
		log.Printf("[dry-run] filesystem: would transfer %d large files to %s@%s:%s/%s/", len(large), in.VMUser, in.VMHost, base, generate.LargeDir)
		return nil
	}
	log.Printf("filesystem: transferring %d large files to %s@%s:%s/%s/, one at a time ...", len(large), in.VMUser, in.VMHost, base, generate.LargeDir)
	p := fsParams(cfg, in)
	pg := progress.New("filesystem(large)", len(large), generate.LargeBytes(large))
	p.OnFile = pg.Add
	n, err := filesystem.TransferLarge(ctx, p, large, pg.AddBytes)
	pg.Finish()
	man.Large.FilesystemFiles = n
	if err != nil {
		return fmt.Errorf("filesystem large transfer (%d/%d ok): %w", n, len(large), err)
	}
	return nil
}

// totalSize is the bytes a bucket upload or a filesystem transfer has to move.
func totalSize(files []generate.File) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

func doDatabase(ctx context.Context, cfg *config.Config, in *config.Inputs, engines []database.Engine,
	provider string, bulkPlan database.BulkPlan, dryRun bool, man *manifest) error {

	dbName := in.DBName
	if dbName == "" {
		dbName = database.ShopDBName
	}
	dm := &dbManifest{Database: dbName}
	man.Database = dm
	for _, e := range engines {
		dc, ok := dbConfig(in, e)
		if !ok {
			log.Printf("database: %s host not available in outputs (skipped)%s", e, missingHostHint(provider, e))
			continue
		}
		dm.Engines = append(dm.Engines, string(e))
		if dryRun {
			log.Printf("[dry-run] database(%s): would load shop_db fixtures into db %q on %s:%s", e, dbName, dc.Host, dc.Port)
			if !bulkPlan.Empty() {
				log.Printf("[dry-run] database(%s): would then add %s", e, bulkPlan)
			}
			continue
		}
		log.Printf("database(%s): loading shop_db fixtures into db %q on %s:%s ...", e, dbName, dc.Host, dc.Port)
		if err := database.LoadShopDB(ctx, dc); err != nil {
			return fmt.Errorf("database(%s) load: %w", e, err)
		}
		log.Printf("database(%s): loaded", e)

		// The bulk rows go on top of the fixture, never instead of it: the fixture
		// is what carries the DDL objects and the multibyte rows a migration is
		// being judged on, and these only make it heavy.
		if bulkPlan.Empty() {
			continue
		}
		log.Printf("database(%s): adding %s ...", e, bulkPlan)
		res, err := database.LoadBulk(ctx, dc, bulkOptions(cfg.Database), bulkPlan)
		if err != nil {
			return fmt.Errorf("database(%s) bulk: %w", e, err)
		}
		dm.Bulk = append(dm.Bulk, bulkManifest{Engine: string(e), BulkResult: res})
		log.Printf("database(%s): %d bulk rows in %ds via %s; %s now occupies %d MB",
			e, res.TotalRows, res.Seconds, res.Method, dbName, res.ActualMB)
	}
	return nil
}

func bulkOptions(d config.DatabaseConfig) database.BulkOptions {
	return database.BulkOptions{
		SizeMB:    d.SizeMB,
		BatchSize: d.BatchSize,
		IDOffset:  d.IDOffset,
		Seed:      d.Seed,
		Weights:   d.Weights,
		Workers:   d.Workers,
	}
}

// ---------------------------------------------------------------------------
// capacity pre-flight
// ---------------------------------------------------------------------------

// capacityHeadroom is the share of a prepared disk a run is allowed to ask
// for. The rest is not spare: a database needs room for its WAL/binlog, its
// temporary sort files and the copy an ALTER makes, and a filesystem for the OS
// that is already on the volume.
const capacityHeadroom = 0.8

// capacityPreflight refuses a size that would not fit on the prepared disk.
//
// This is not tidiness. A managed instance that fills its volume does not fail
// the run and carry on - it goes read-only or into STORAGE_FULL, and on both CSPs
// the way back is to tear down and prepare again, half an hour later. The
// limits come from the connection info, because they are facts about the
// infrastructure; when the preparation environment does not know one it sends 0
// and the check is skipped rather than guessed at.
//
// largeBytes is the large files the run adds on top of the dummy set; they land
// on the same volume.
func capacityPreflight(cfg *config.Config, in *config.Inputs, targets map[string]bool, alloc generate.Allocation, largeBytes int64) error {
	var problems []string
	if targets["filesystem"] && in.VMVolumeGB > 0 {
		want := int((alloc.TotalBytes() + largeBytes) / generate.MiB)
		if limit := int(float64(in.VMVolumeGB) * 1024 * capacityHeadroom); want > limit {
			problems = append(problems, fmt.Sprintf(
				"filesystem: %d MB of dummy and large files on a %d GB volume (max %d MB, %.0f%% of the volume)",
				want, in.VMVolumeGB, limit, capacityHeadroom*100))
		}
	}
	if targets["database"] && in.DBStorageGB > 0 {
		want := cfg.Database.SizeMB
		if limit := int(float64(in.DBStorageGB) * 1024 * capacityHeadroom); want > limit {
			problems = append(problems, fmt.Sprintf(
				"database: %d MB requested on %d GB of storage (max %d MB, %.0f%% of the allocation)",
				want, in.DBStorageGB, limit, capacityHeadroom*100))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the requested data would not fit (use --force to try anyway):\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// param builders
// ---------------------------------------------------------------------------

func bucketParams(cfg *config.Config, in *config.Inputs, provider string) bucket.Params {
	endpoint, lookup := cfg.ObjectStorage.EndpointOverride, cfg.ObjectStorage.BucketLookup
	if in.BucketEndpoint != "" {
		endpoint = in.BucketEndpoint
	}
	if in.BucketLookup != "" {
		lookup = in.BucketLookup
	}
	return bucket.Params{
		Provider:         provider,
		Region:           in.Region,
		Bucket:           in.BucketName,
		AccessKey:        in.AccessKey,
		SecretKey:        in.SecretKey,
		EndpointOverride: endpoint,
		BucketLookup:     lookup,
		BasePrefix:       cfg.ObjectStorage.BasePrefix,
		Concurrency:      cfg.ObjectStorage.Concurrency,
	}
}

func fsParams(cfg *config.Config, in *config.Inputs) filesystem.Params {
	return filesystem.Params{
		Host:           in.VMHost,
		Port:           22,
		User:           in.VMUser,
		PrivateKeyPath: in.VMKeyPath,
		BasePath:       fsBasePath(cfg, in),
		Concurrency:    cfg.Filesystem.Concurrency,
		Connections:    cfg.Filesystem.Connections,
	}
}

// fsBasePath resolves the SFTP destination root. The vm module may publish a
// "data_path" output (NCP logs in as root, so it uses /root/testdata instead of
// the /home/ubuntu/testdata default in config.json); it wins when present.
func fsBasePath(cfg *config.Config, in *config.Inputs) string {
	if in.VMDataPath != "" {
		return in.VMDataPath
	}
	return cfg.Filesystem.BasePath
}

// missingHostHint explains an empty host output. NCP managed DBs only expose a
// reachable host once a public domain has been issued in the console.
func missingHostHint(provider string, e database.Engine) string {
	if strings.ToLower(provider) != "ncp" {
		return ""
	}
	switch e {
	case database.MySQL, database.Postgres, database.MongoDB:
		return " — NCP managed DBs need a public domain issued in the console, then run ./scripts/ncp-db-domain.sh"
	}
	return ""
}

func dbConfig(in *config.Inputs, e database.Engine) (database.Config, bool) {
	c := database.Config{Engine: e, User: in.DBUser, Password: in.DBPassword, Database: in.DBName}
	switch e {
	case database.MySQL:
		c.Host, c.Port = in.MySQLHost, in.MySQLPort
	case database.MariaDB:
		c.Host, c.Port = in.MariaDBHost, in.MariaDBPort
	case database.Postgres:
		c.Host, c.Port = in.PostgresHost, in.PostgresPort
	case database.MongoDB:
		c.Host, c.Port = in.MongoHost, in.MongoPort
	}
	if c.Host == "" {
		return c, false
	}
	return c, true
}

func dummyOptions(d config.DummyConfig, maxFiles int) generate.Options {
	return generate.Options{
		MaxFiles: maxFiles,
		SizeSQL:  d.SizeSQL,
		SizeCSV:  d.SizeCSV,
		SizeJSON: d.SizeJSON,
		SizeXML:  d.SizeXML,
		SizeTXT:  d.SizeTXT,
		SizePNG:  d.SizePNG,
		SizeGIF:  d.SizeGIF,
		SizeZIP:  d.SizeZIP,
	}
}

// ---------------------------------------------------------------------------
// flag parsing
// ---------------------------------------------------------------------------

// parseProvider validates the CSP name and normalizes it to lower case.
//
// Where the infrastructure came from is the caller's business - gendata is told
// the connection info rather than looking it up - so the name is only used to
// pick the object-storage endpoint, and every provider internal/bucket knows
// about is allowed.
func parseProvider(s string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(s))
	switch p {
	case "aws", "ncp",
		"azure", "gcp", "alibaba", "tencent", "ibm", "oracle", "openstack", "nhn", "kt", "ktclassic", "mock":
		return p, nil
	default:
		return "", fmt.Errorf("invalid provider %q", s)
	}
}

func parseTargets(s string) (map[string]bool, error) {
	all := map[string]bool{"bucket": true, "filesystem": true, "database": true}
	if s == "all" {
		return all, nil
	}
	out := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if !all[t] {
			return nil, fmt.Errorf("invalid target %q (bucket|filesystem|database|all)", t)
		}
		out[t] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no target selected")
	}
	return out, nil
}

func parseEngines(s string) ([]database.Engine, error) {
	all := []database.Engine{database.MySQL, database.MariaDB, database.Postgres, database.MongoDB}
	if s == "all" {
		return all, nil
	}
	valid := map[string]database.Engine{
		"mysql": database.MySQL, "mariadb": database.MariaDB,
		"postgresql": database.Postgres, "mongodb": database.MongoDB,
	}
	var out []database.Engine
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		v, ok := valid[e]
		if !ok {
			return nil, fmt.Errorf("invalid engine %q (mysql|mariadb|postgresql|mongodb|all)", e)
		}
		out = append(out, v)
	}
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---------------------------------------------------------------------------
// manifest
// ---------------------------------------------------------------------------

type manifest struct {
	Timestamp string             `json:"timestamp"`
	Provider  string             `json:"provider"`
	Targets   []string           `json:"targets"`
	Layout    layoutManifest     `json:"layout"`
	Dummy     config.DummyConfig `json:"dummy"`
	// Files is the plan the sizes above turned into once the file limit applied:
	// files and bytes per format, and any format the limit skipped.
	Files generate.Allocation `json:"files"`
	// Large is the large files the run added, if any. LargeOnly says the dummy
	// set was left alone and these were all the run did.
	Large     *largeManifest `json:"large,omitempty"`
	LargeOnly bool           `json:"largeOnly,omitempty"`
	BulkMB    int            `json:"bulkMB"`
	// Origins says whether the sizes above came from config.json or from .env,
	// which the numbers alone cannot.
	Origins    config.Origins  `json:"origins"`
	DryRun     bool            `json:"dryRun"`
	Bucket     *bucketManifest `json:"bucket,omitempty"`
	Filesystem *fsManifest     `json:"filesystem,omitempty"`
	Database   *dbManifest     `json:"database,omitempty"`
}

type layoutManifest struct {
	FolderDepth   int `json:"folderDepth"`
	FolderBreadth int `json:"folderBreadth"`
	MaxFiles      int `json:"maxFiles"`
}
type bucketManifest struct {
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	Objects int    `json:"objects"`
}
type fsManifest struct {
	Host     string `json:"host"`
	BasePath string `json:"basePath"`
	Files    int    `json:"files"`
}
type largeManifest struct {
	FileMB          int    `json:"fileMB"`
	Files           int    `json:"files"`
	Dir             string `json:"dir"`
	BucketObjects   int    `json:"bucketObjects,omitempty"`
	FilesystemFiles int    `json:"filesystemFiles,omitempty"`
}
type dbManifest struct {
	Engines  []string       `json:"engines"`
	Database string         `json:"database"`
	Bulk     []bulkManifest `json:"bulk,omitempty"`
}

// bulkManifest records what the bulk phase wrote, per engine. ActualMB is the
// interesting half: it is what the next run has to fit alongside, and it is not
// the megabytes that were asked for.
type bulkManifest struct {
	Engine string `json:"engine"`
	database.BulkResult
}

func newManifest(provider string, targets map[string]bool, cfg *config.Config, origins config.Origins, dryRun bool) *manifest {
	return &manifest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Provider:  provider,
		Targets:   sortedKeys(targets),
		Layout:    layoutManifest{cfg.Layout.FolderDepth, cfg.Layout.FolderBreadth, cfg.Layout.MaxFiles},
		Dummy:     cfg.Dummy,
		BulkMB:    cfg.Database.SizeMB,
		Origins:   origins,
		DryRun:    dryRun,
	}
}

func writeManifest(p string, m *manifest) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return p, nil
}
