package core

import (
	"path/filepath"
	"testing"
)

// An unset staging root follows TMPDIR, so an operator can move staging off a
// small or RAM-backed /tmp without configuring transx-ex itself.
func TestStagingRoot_DefaultFollowsTMPDIR(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if got, want := StagingRoot(""), filepath.Join(tmp, "transxex-staging"); got != want {
		t.Errorf("StagingRoot(\"\") = %q, want %q", got, want)
	}
}

func TestStagingRoot_DefaultFallsBackToTmp(t *testing.T) {
	t.Setenv("TMPDIR", "")

	if got, want := StagingRoot(""), "/tmp/transxex-staging"; got != want {
		t.Errorf("StagingRoot(\"\") = %q, want %q", got, want)
	}
}

// A root the caller names wins over TMPDIR, and is used as given rather than
// gaining a transxex-staging component of its own.
func TestStagingRoot_ExplicitDirWins(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	if got, want := StagingRoot("/data/staging/"), "/data/staging"; got != want {
		t.Errorf("StagingRoot(%q) = %q, want %q", "/data/staging/", got, want)
	}
}

// The two domains stay in subdirectories of their own under whichever root is
// in effect.
func TestStagingPaths_AreDomainSubdirectories(t *testing.T) {
	for _, dir := range []string{"", "/data/staging"} {
		root := StagingRoot(dir)
		if got, want := StorageStagingPath(dir), filepath.Join(root, "storage"); got != want {
			t.Errorf("StorageStagingPath(%q) = %q, want %q", dir, got, want)
		}
		if got, want := DBMSStagingPath(dir), filepath.Join(root, "dbms"); got != want {
			t.Errorf("DBMSStagingPath(%q) = %q, want %q", dir, got, want)
		}
	}
}
