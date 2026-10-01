package storagex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ============================================================================
// Fixtures
// ============================================================================

func sshLoc(path string) DataLocation {
	return DataLocation{
		StorageType: StorageTypeFilesystem,
		Path:        path,
		Filesystem: &FilesystemAccess{
			AccessType: AccessTypeSSH,
			SSH:        &SSHConfig{Host: "198.51.100.7", Port: 22, Username: "tester"},
		},
	}
}

func localLoc(path string) DataLocation {
	return DataLocation{
		StorageType: StorageTypeFilesystem,
		Path:        path,
		Filesystem:  &FilesystemAccess{AccessType: AccessTypeLocal},
	}
}

// s3Loc builds an object storage location. NewMinioProvider only constructs a
// client, so planning an S3 pipeline needs no server and no credentials that
// resolve.
func s3Loc(path string) DataLocation {
	return DataLocation{
		StorageType: StorageTypeObjectStorage,
		Path:        path,
		ObjectStorage: &ObjectStorageAccess{
			AccessType: AccessTypeMinio,
			Minio: &S3MinioConfig{
				Endpoint:        "198.51.100.9:9000",
				AccessKeyId:     "key",
				SecretAccessKey: "secret",
			},
		},
	}
}

// stagingDirs lists what is currently under the staging root, so a test can tell
// what it created from what was already there.
func stagingDirs(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	entries, err := os.ReadDir(DefaultStagingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read staging root: %v", err)
	}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// ============================================================================
// newStagingLocation
// ============================================================================

// Every relay stages through a directory of its own. It has to be its own,
// because the second step of a relay sends the WHOLE staging directory onward —
// so anything else left in there would travel as this transfer's data.
func TestNewStagingLocationIsUniquePerCall(t *testing.T) {
	before := stagingDirs(t)

	first, cleanFirst, err := newStagingLocation()
	if err != nil {
		t.Fatalf("newStagingLocation: %v", err)
	}
	defer cleanFirst()

	second, cleanSecond, err := newStagingLocation()
	if err != nil {
		t.Fatalf("newStagingLocation: %v", err)
	}
	defer cleanSecond()

	if first.Path == second.Path {
		t.Fatalf("two calls returned the same directory %q; one relay's leftovers would become another's payload", first.Path)
	}

	for _, loc := range []DataLocation{first, second} {
		if got := filepath.Dir(loc.Path); got != DefaultStagingPath {
			t.Errorf("staging directory %q sits in %q, want it under %q", loc.Path, got, DefaultStagingPath)
		}
		if loc.Path == DefaultStagingPath {
			t.Errorf("staging directory is the shared root %q itself", DefaultStagingPath)
		}
		if !loc.IsLocal() {
			t.Errorf("staging location %q is not a local filesystem location", loc.Path)
		}

		info, err := os.Stat(loc.Path)
		if err != nil {
			t.Errorf("staging directory %q was not created: %v", loc.Path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("staging path %q is not a directory", loc.Path)
		}
		// A relay copies everything it finds here, so it must start empty.
		entries, err := os.ReadDir(loc.Path)
		if err != nil {
			t.Errorf("read staging directory %q: %v", loc.Path, err)
			continue
		}
		if len(entries) != 0 {
			t.Errorf("staging directory %q starts with %d entries, want none", loc.Path, len(entries))
		}
		if before[filepath.Base(loc.Path)] {
			t.Errorf("staging directory %q reuses a name that already existed", loc.Path)
		}
	}
}

func TestNewStagingLocationCleanupRemovesDirectory(t *testing.T) {
	loc, cleanup, err := newStagingLocation()
	if err != nil {
		t.Fatalf("newStagingLocation: %v", err)
	}

	// Cleanup has to remove a populated directory, not just an empty one: it runs
	// after a transfer, and after a failed one it runs over a half-written copy.
	if err := os.WriteFile(filepath.Join(loc.Path, "staged.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write into staging: %v", err)
	}

	cleanup()

	if _, err := os.Stat(loc.Path); !os.IsNotExist(err) {
		t.Errorf("staging directory %q still exists after cleanup (stat error: %v)", loc.Path, err)
	}
	// The shared root is not the cleanup's to remove — a concurrent relay may be
	// using it.
	if _, err := os.Stat(DefaultStagingPath); err != nil {
		t.Errorf("cleanup removed the staging root %q: %v", DefaultStagingPath, err)
	}
}

// ============================================================================
// Which pipelines stage, and which do not
// ============================================================================

func TestRelayPipelinesStageInTheirOwnDirectory(t *testing.T) {
	cases := []struct {
		name  string
		model DataMigrationModel
		// step whose Destination is the staging directory
		stagingStep int
	}{
		{
			// The regression this file exists for: this pipeline used to stage
			// through DefaultStagingPath itself.
			name: "filesystem relay (ssh to ssh)",
			model: DataMigrationModel{
				Source:      sshLoc("/testdata"),
				Destination: sshLoc("/restored"),
				Strategy:    StrategyRelay,
			},
			stagingStep: 0,
		},
		{
			name: "object storage relay (s3 to s3)",
			model: DataMigrationModel{
				Source:      s3Loc("src-bucket/"),
				Destination: s3Loc("dst-bucket/"),
			},
			stagingStep: 0,
		},
		{
			name: "cross storage (ssh to s3)",
			model: DataMigrationModel{
				Source:      sshLoc("/testdata"),
				Destination: s3Loc("dst-bucket/"),
			},
			stagingStep: 0,
		},
		{
			name: "cross storage (s3 to ssh)",
			model: DataMigrationModel{
				Source:      s3Loc("src-bucket/"),
				Destination: sshLoc("/restored"),
			},
			stagingStep: 0,
		},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipe, err := Plan(tc.model)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if pipe.Cleanup == nil {
				t.Fatal("a relay pipeline carries no Cleanup, so its staging directory would be left behind")
			}
			defer pipe.Cleanup()

			staging := pipe.Steps[tc.stagingStep].Destination.Path
			if staging == DefaultStagingPath {
				t.Fatalf("staged through the shared root %q; leftovers from another transfer would be copied onward", DefaultStagingPath)
			}
			if filepath.Dir(staging) != DefaultStagingPath {
				t.Errorf("staging %q is not under %q", staging, DefaultStagingPath)
			}
			// The second step reads back what the first one wrote.
			if got := pipe.Steps[tc.stagingStep+1].Source.Path; got != staging {
				t.Errorf("step %d reads %q, want the staging directory %q", tc.stagingStep+2, got, staging)
			}
			if owner, dup := seen[staging]; dup {
				t.Errorf("staging %q was already handed to %q", staging, owner)
			}
			seen[staging] = tc.name
		})
	}
}

// A transfer that needs no staging must not create any, and must carry no
// Cleanup to run.
func TestDirectPipelinesDoNotStage(t *testing.T) {
	cases := []struct {
		name  string
		model DataMigrationModel
	}{
		{
			name: "filesystem pull (ssh to local)",
			model: DataMigrationModel{
				Source:      sshLoc("/testdata"),
				Destination: localLoc("/var/tmp/restored"),
			},
		},
		{
			name: "upload (local to s3)",
			model: DataMigrationModel{
				Source:      localLoc("/testdata"),
				Destination: s3Loc("dst-bucket/"),
			},
		},
		{
			name: "download (s3 to local)",
			model: DataMigrationModel{
				Source:      s3Loc("src-bucket/"),
				Destination: localLoc("/var/tmp/restored"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := stagingDirs(t)

			pipe, err := Plan(tc.model)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if pipe.Cleanup != nil {
				t.Error("a direct pipeline carries a Cleanup, which suggests it allocated staging it does not need")
				pipe.Cleanup()
			}
			if len(pipe.Steps) != 1 {
				t.Errorf("got %d steps, want a single direct one", len(pipe.Steps))
			}

			for name := range stagingDirs(t) {
				if !before[name] {
					t.Errorf("planning created staging directory %q", name)
				}
			}
		})
	}
}

// ============================================================================
// Cleanup at the end of a run
// ============================================================================

// The contract that matters: once Execute returns, the staging directory is
// gone. Execute runs Cleanup with defer, so this holds whether the pipeline
// succeeded, failed on a step, or never started — and "never started" is the
// case a test can produce without a server on the other end.
//
// Leaving it behind is what made a later transfer copy this one's files onward,
// so this is the regression that most wants pinning down.
func TestExecuteRemovesStagingWhenPipelineEnds(t *testing.T) {
	pipe, err := Plan(DataMigrationModel{
		Source:      sshLoc("/testdata"),
		Destination: sshLoc("/restored"),
		Strategy:    StrategyRelay,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	staging := pipe.Steps[0].Destination.Path
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging %q missing before Execute: %v", staging, err)
	}
	// Stand in for a transfer that got part way: a half-written staging directory
	// is exactly what must not survive.
	if err := os.WriteFile(filepath.Join(staging, "partial.txt"), []byte("half"), 0o600); err != nil {
		t.Fatalf("write into staging: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pipe.Execute(ctx); err == nil {
		t.Fatal("Execute on a cancelled context returned no error")
	}

	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging %q survived Execute (stat error: %v)", staging, err)
	}
}
