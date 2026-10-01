package base

import "testing"

// =============================================================================
// SortUnique
// =============================================================================

func TestSortUnique(t *testing.T) {
	got := SortUnique([]string{"utf8mb4", "", "latin1", "utf8mb4", "ascii", ""})
	want := []string{"ascii", "latin1", "utf8mb4"}
	if len(got) != len(want) {
		t.Fatalf("SortUnique = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortUnique[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSortUnique_AllEmptyYieldsNil(t *testing.T) {
	// A profile whose list holds nothing usable must present it as absent, not as
	// a slice of empty strings — missingFrom reads an empty availability list as
	// "this driver does not enumerate them".
	if got := SortUnique([]string{"", ""}); got != nil {
		t.Errorf("SortUnique of empty names = %v, want nil", got)
	}
}

// =============================================================================
// DiffCharset — unsupported names
// =============================================================================

func TestDiffCharset_UnknownCollationBlocks(t *testing.T) {
	// The classic 5.7 → 8.0-only case in reverse: the dump names a collation the
	// target has never heard of, so its first CREATE statement fails.
	src := &CharsetProfile{
		UsedCollations: []string{"utf8mb4_0900_ai_ci"},
	}
	dst := &CharsetProfile{
		AvailableCollations: []string{"utf8mb4_general_ci", "utf8mb4_unicode_ci"},
	}

	issues := DiffCharset(DBMSTypeMySQL, src, dst)
	if len(issues) != 1 {
		t.Fatalf("DiffCharset returned %d issues, want 1: %v", len(issues), issues)
	}
	if issues[0].Severity != SeverityBlocker {
		t.Errorf("severity = %q, want %q", issues[0].Severity, SeverityBlocker)
	}
	if issues[0].Source != "utf8mb4_0900_ai_ci" {
		t.Errorf("source = %q, want the unsupported collation", issues[0].Source)
	}
}

func TestDiffCharset_KnownCollationPasses(t *testing.T) {
	src := &CharsetProfile{
		UsedCharsets:   []string{"utf8mb4"},
		UsedCollations: []string{"utf8mb4_general_ci"},
	}
	dst := &CharsetProfile{
		AvailableCharsets:   []string{"utf8mb4", "latin1"},
		AvailableCollations: []string{"utf8mb4_general_ci", "utf8mb4_0900_ai_ci"},
	}

	if issues := DiffCharset(DBMSTypeMySQL, src, dst); len(issues) != 0 {
		t.Errorf("a target supporting every name must raise nothing, got %v", issues)
	}
}

func TestDiffCharset_NameMatchIgnoresCase(t *testing.T) {
	// MySQL reports collation names lower-cased while PostgreSQL preserves the
	// case a collation was created with. An exact comparison would report names
	// the server does in fact know.
	src := &CharsetProfile{UsedCollations: []string{"en_US.utf8"}}
	dst := &CharsetProfile{AvailableCollations: []string{"EN_US.UTF8"}}

	if issues := DiffCharset(DBMSTypePostgreSQL, src, dst); len(issues) != 0 {
		t.Errorf("case-different names must be treated as the same, got %v", issues)
	}
}

func TestDiffCharset_EmptyAvailabilityIsNotTreatedAsUnsupported(t *testing.T) {
	// A driver that does not enumerate a kind of name leaves the list empty.
	// Reading that as "supports nothing" would block every migration.
	src := &CharsetProfile{UsedCollations: []string{"utf8mb4_general_ci"}}
	dst := &CharsetProfile{}

	if issues := DiffCharset(DBMSTypeMySQL, src, dst); len(issues) != 0 {
		t.Errorf("an unenumerated availability list must not block, got %v", issues)
	}
}

// =============================================================================
// DiffCharset — PostgreSQL encoding
// =============================================================================

func TestDiffCharset_PgNarrowerEncodingBlocks(t *testing.T) {
	// PostgreSQL keeps the encoding on the database, so no dump can carry it —
	// the target's own setting decides, and LATIN1 cannot hold what UTF8 does.
	src := &CharsetProfile{Encoding: "UTF8", Collation: "en_US.utf8"}
	dst := &CharsetProfile{Encoding: "LATIN1", Collation: "en_US.utf8"}

	issues := DiffCharset(DBMSTypePostgreSQL, src, dst)
	if len(issues) != 1 || issues[0].Severity != SeverityBlocker {
		t.Fatalf("narrowing the encoding must block, got %v", issues)
	}
	if issues[0].Scope != "database" {
		t.Errorf("scope = %q, want %q", issues[0].Scope, "database")
	}
}

func TestDiffCharset_PgWideningEncodingWarnsOnly(t *testing.T) {
	// UTF8 represents everything, so the data survives; the operator is told
	// because the text arrives re-encoded.
	src := &CharsetProfile{Encoding: "LATIN1", Collation: "en_US.utf8"}
	dst := &CharsetProfile{Encoding: "UTF8", Collation: "en_US.utf8"}

	issues := DiffCharset(DBMSTypePostgreSQL, src, dst)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning {
		t.Fatalf("widening the encoding must warn, not block, got %v", issues)
	}
}

func TestDiffCharset_EncodingIgnoredOnMySQL(t *testing.T) {
	// MySQL writes the character set into the table DDL, so the target
	// database's default never reaches the tables being restored. Only the
	// collation difference is worth mentioning.
	src := &CharsetProfile{Encoding: "utf8mb4", Collation: "utf8mb4_general_ci"}
	dst := &CharsetProfile{Encoding: "latin1", Collation: "utf8mb4_general_ci"}

	if issues := DiffCharset(DBMSTypeMySQL, src, dst); len(issues) != 0 {
		t.Errorf("the database default charset must not be compared on MySQL, got %v", issues)
	}
}

// =============================================================================
// DiffCharset — database default collation and ordering
// =============================================================================

func TestDiffCharset_DefaultCollationWarns(t *testing.T) {
	// Objects created without a collation of their own take the target's, which
	// changes how they compare strings. It does not stop the migration.
	src := &CharsetProfile{Collation: "utf8mb4_general_ci"}
	dst := &CharsetProfile{Collation: "utf8mb4_0900_ai_ci"}

	issues := DiffCharset(DBMSTypeMySQL, src, dst)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning {
		t.Fatalf("a differing database default must warn, got %v", issues)
	}
}

func TestDiffCharset_BlockersSortFirst(t *testing.T) {
	// One run should report everything, with what stops the migration at the top.
	src := &CharsetProfile{
		Collation:      "utf8mb4_general_ci",
		UsedCollations: []string{"utf8mb4_0900_ai_ci"},
	}
	dst := &CharsetProfile{
		Collation:           "utf8mb4_bin",
		AvailableCollations: []string{"utf8mb4_general_ci", "utf8mb4_bin"},
	}

	issues := DiffCharset(DBMSTypeMySQL, src, dst)
	if len(issues) != 2 {
		t.Fatalf("DiffCharset returned %d issues, want 2: %v", len(issues), issues)
	}
	if issues[0].Severity != SeverityBlocker {
		t.Errorf("issues[0].Severity = %q, want the blocker first", issues[0].Severity)
	}
	if issues[1].Severity != SeverityWarning {
		t.Errorf("issues[1].Severity = %q, want the warning second", issues[1].Severity)
	}
}

func TestDiffCharset_MongoDBYieldsNothing(t *testing.T) {
	// MongoDB stores UTF-8 only, and a collection's collation travels inside the
	// dump's stored options rather than as a name the target must recognise.
	src := &CharsetProfile{UsedCollations: []string{"ko"}}
	dst := &CharsetProfile{}

	if issues := DiffCharset(DBMSTypeMongoDB, src, dst); issues != nil {
		t.Errorf("MongoDB must raise nothing, got %v", issues)
	}
}

func TestDiffCharset_NilProfileYieldsNothing(t *testing.T) {
	if issues := DiffCharset(DBMSTypeMySQL, nil, &CharsetProfile{}); issues != nil {
		t.Errorf("a nil source profile must raise nothing, got %v", issues)
	}
	if issues := DiffCharset(DBMSTypeMySQL, &CharsetProfile{}, nil); issues != nil {
		t.Errorf("a nil target profile must raise nothing, got %v", issues)
	}
}
