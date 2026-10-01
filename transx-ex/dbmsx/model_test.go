package dbmsx

import (
	"strings"
	"testing"

	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
)

// --- helpers ---

func directMySQL() DBMSLocation {
	return DBMSLocation{
		DBMSType:   DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "127.0.0.1", Port: 3306},
	}
}

func sshPostgreSQL() DBMSLocation {
	return DBMSLocation{
		DBMSType:   DBMSTypePostgreSQL,
		Database:   "testdb",
		AccessType: AccessTypeSSHTunnel,
		SSHTunnel: &SSHTunnelConfig{
			SSH: &SSHConfig{Host: "ssh.example.com", Port: 22, Username: "admin"},
		},
	}
}

// --- Validate: valid cases ---

func TestValidate_DirectMySQL_Full(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      directMySQL(),
		Destination: directMySQL(),
		Scope:       ScopeFull,
	}
	if err := Validate(model); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_SSHTunnel_PostgreSQL_SchemaOnly(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      sshPostgreSQL(),
		Destination: sshPostgreSQL(),
		Scope:       ScopeSchemaOnly,
	}
	if err := Validate(model); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_EmptyScope_Allowed(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      directMySQL(),
		Destination: directMySQL(),
		// Scope empty → defaults to full at runtime
	}
	if err := Validate(model); err != nil {
		t.Fatalf("empty scope should be valid, got: %v", err)
	}
}

// --- Validate: error cases ---

func TestValidate_MissingSourceDBMSType(t *testing.T) {
	loc := directMySQL()
	loc.DBMSType = ""
	model := DBMSMigrationModel{Source: loc, Destination: directMySQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error for missing dbmsType")
	}
}

func TestValidate_MissingDatabase(t *testing.T) {
	loc := directMySQL()
	loc.Database = ""
	model := DBMSMigrationModel{Source: loc, Destination: directMySQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error for missing database")
	}
}

func TestValidate_MissingDirectConfig(t *testing.T) {
	loc := directMySQL()
	loc.Direct = nil
	model := DBMSMigrationModel{Source: loc, Destination: directMySQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error when direct config is nil")
	}
}

func TestValidate_DirectConfig_MissingHost(t *testing.T) {
	loc := directMySQL()
	loc.Direct.Host = ""
	model := DBMSMigrationModel{Source: loc, Destination: directMySQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error when direct.host is empty")
	}
}

func TestValidate_SSHTunnel_MissingSSHConfig(t *testing.T) {
	loc := sshPostgreSQL()
	loc.SSHTunnel.SSH = nil
	model := DBMSMigrationModel{Source: loc, Destination: sshPostgreSQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error when sshTunnel.ssh is nil")
	}
}

func TestValidate_InvalidScope(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      directMySQL(),
		Destination: directMySQL(),
		Scope:       "everything",
	}
	if err := Validate(model); err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestValidate_CrossEngine_Blocked(t *testing.T) {
	// Nothing downstream translates between engines: a dump is the source
	// engine's own dialect and the restore hands it over verbatim, so the pair
	// has to be refused before anything is read.
	model := DBMSMigrationModel{
		Source:      directMySQL(),
		Destination: sshPostgreSQL(),
	}
	err := Validate(model)
	if err == nil {
		t.Fatal("expected error for a migration between different engines")
	}
	if !strings.Contains(err.Error(), "different database engines") {
		t.Errorf("error should name the engine mismatch, got: %v", err)
	}
}

func TestValidate_SameEngine_Allowed(t *testing.T) {
	// The two ends may differ in every other way — access type, host, database —
	// as long as the engine matches.
	dst := directMySQL()
	dst.Database = "otherdb"
	model := DBMSMigrationModel{Source: directMySQL(), Destination: dst}
	if err := Validate(model); err != nil {
		t.Fatalf("same-engine migration should be valid, got: %v", err)
	}
}

func TestValidate_MongoDB_CrossType_Blocked(t *testing.T) {
	directMongo := DBMSLocation{
		DBMSType:   DBMSTypeMongoDB,
		Database:   "testdb",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "127.0.0.1"},
	}
	sshMongo := DBMSLocation{
		DBMSType:   DBMSTypeMongoDB,
		Database:   "testdb",
		AccessType: AccessTypeSSHTunnel,
		SSHTunnel: &SSHTunnelConfig{
			SSH: &SSHConfig{Host: "ssh.example.com", Port: 22, Username: "admin"},
		},
	}
	model := DBMSMigrationModel{Source: directMongo, Destination: sshMongo}
	if err := Validate(model); err == nil {
		t.Fatal("expected error for MongoDB cross-type access")
	}
}

func TestValidate_ColumnExclusion_SSHTunnel_Blocked(t *testing.T) {
	loc := sshPostgreSQL()
	loc.Filter = &DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users", "password"), // column exclusion needs direct access
	}}
	model := DBMSMigrationModel{Source: loc, Destination: sshPostgreSQL()}
	if err := Validate(model); err == nil {
		t.Fatal("expected error when column exclusion used with ssh-tunnel")
	}
}

func TestValidate_WholeTableExclusion_SSHTunnel_OK(t *testing.T) {
	loc := sshPostgreSQL()
	loc.Filter = &DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("logs"),
	}}
	model := DBMSMigrationModel{Source: loc, Destination: sshPostgreSQL()}
	if err := Validate(model); err != nil {
		t.Fatalf("whole-table exclusion over ssh-tunnel should be allowed: %v", err)
	}
}

// --- Helper methods ---

func TestIsDirect(t *testing.T) {
	loc := directMySQL()
	if !loc.IsDirect() {
		t.Error("IsDirect() should return true for direct access")
	}
	if loc.IsSSHTunnel() {
		t.Error("IsSSHTunnel() should return false for direct access")
	}
}

func TestIsSSHTunnel(t *testing.T) {
	loc := sshPostgreSQL()
	if !loc.IsSSHTunnel() {
		t.Error("IsSSHTunnel() should return true for ssh-tunnel access")
	}
	if loc.IsDirect() {
		t.Error("IsDirect() should return false for ssh-tunnel access")
	}
}

func TestIsMongoDB(t *testing.T) {
	mongoLoc := DBMSLocation{DBMSType: DBMSTypeMongoDB}
	if !mongoLoc.IsMongoDB() {
		t.Error("IsMongoDB() should return true for mongodb")
	}
	if directMySQL().IsMongoDB() {
		t.Error("IsMongoDB() should return false for mysql")
	}
}

// =============================================================================
// PgSchema — selection, per-name rules, and the source/destination mapping
// =============================================================================

// directPostgreSQL is the PostgreSQL counterpart of directMySQL in model_test.go.
// The schema mapping rules turn on the access types, so both a direct and an
// ssh-tunnel PostgreSQL location are needed here.
func directPostgreSQL() DBMSLocation {
	return DBMSLocation{
		DBMSType:   DBMSTypePostgreSQL,
		Database:   "shop",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "127.0.0.1", Port: 5432},
	}
}

func withPgSchema(loc DBMSLocation, schemas ...string) DBMSLocation {
	loc.PgSchema = schemas
	return loc
}

// --- pgSchema on the wrong engine ---

func TestValidate_PgSchema_RejectedOnMySQL(t *testing.T) {
	src := directMySQL()
	src.PgSchema = []string{"public"}
	err := Validate(DBMSMigrationModel{Source: src, Destination: directMySQL()})
	if err == nil {
		t.Fatal("expected pgSchema on mysql to be rejected")
	}
	if !strings.Contains(err.Error(), "PostgreSQL-only") {
		t.Errorf("error should explain the field is PostgreSQL-only, got: %v", err)
	}
}

func TestValidate_PgSchema_RejectedOnMongoDB(t *testing.T) {
	loc := DBMSLocation{
		DBMSType:   DBMSTypeMongoDB,
		Database:   "shop",
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "127.0.0.1"},
		PgSchema:   []string{"public"},
	}
	dst := loc
	dst.PgSchema = nil
	if err := Validate(DBMSMigrationModel{Source: loc, Destination: dst}); err == nil {
		t.Fatal("expected pgSchema on mongodb to be rejected")
	}
}

// --- per-name validation ---

func TestValidate_PgSchema_NameRules(t *testing.T) {
	cases := []struct {
		name    string
		schemas []string
		wantErr string
	}{
		{"empty name", []string{""}, "is empty"},
		{"double quote", []string{`a"b`}, "double quote"},
		{"line break", []string{"a\nb"}, "NUL or a line break"},
		{"too long", []string{strings.Repeat("x", 64)}, "63 bytes"},
		{"system schema", []string{"pg_catalog"}, "system schema"},
		{"duplicate", []string{"sales", "sales"}, "more than once"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := withPgSchema(directPostgreSQL(), c.schemas...)
			err := Validate(DBMSMigrationModel{Source: src, Destination: directPostgreSQL()})
			if err == nil {
				t.Fatalf("expected %v to be rejected", c.schemas)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error should mention %q, got: %v", c.wantErr, err)
			}
		})
	}
}

func TestValidate_PgSchema_AcceptsUnusualButLegalNames(t *testing.T) {
	// A dot is legal inside a quoted identifier, and 63 bytes is the limit
	// rather than one past it.
	src := withPgSchema(directPostgreSQL(), "my.schema", strings.Repeat("x", 63))
	dst := directPostgreSQL()
	if err := Validate(DBMSMigrationModel{Source: src, Destination: dst}); err != nil {
		t.Fatalf("expected the names to be accepted, got: %v", err)
	}
}

// --- destination mapping rules ---

func TestValidate_PgSchemaMapping(t *testing.T) {
	cases := []struct {
		name    string
		src     DBMSLocation
		dst     DBMSLocation
		wantErr string // "" means the model must validate
	}{
		{
			name: "rule 1: empty destination reproduces any number of source schemas",
			src:  withPgSchema(directPostgreSQL(), "sales", "hr"),
			dst:  directPostgreSQL(),
		},
		{
			name: "rule 1: empty on both sides",
			src:  directPostgreSQL(),
			dst:  directPostgreSQL(),
		},
		{
			name: "rule 2: equal lengths with an explicit source rename by position",
			src:  withPgSchema(directPostgreSQL(), "sales", "hr"),
			dst:  withPgSchema(directPostgreSQL(), "sales_prod", "hr_prod"),
		},
		{
			name: "rule 2: a partial rename still lists every source schema",
			src:  withPgSchema(directPostgreSQL(), "sales", "hr"),
			dst:  withPgSchema(directPostgreSQL(), "sales_prod", "hr"),
		},
		{
			name: "rule 3: a single target needs no explicit source",
			src:  directPostgreSQL(),
			dst:  withPgSchema(directPostgreSQL(), "app"),
		},
		{
			name:    "rule 4: several targets against an unlisted source are ambiguous",
			src:     directPostgreSQL(),
			dst:     withPgSchema(directPostgreSQL(), "a", "b"),
			wantErr: "must be listed explicitly",
		},
		{
			name:    "rule 5: fewer targets than sources would merge schemas",
			src:     withPgSchema(directPostgreSQL(), "sales", "hr"),
			dst:     withPgSchema(directPostgreSQL(), "all"),
			wantErr: "merging schemas is not supported",
		},
		{
			name:    "rule 6: more targets than sources have nothing to rename",
			src:     withPgSchema(directPostgreSQL(), "sales"),
			dst:     withPgSchema(directPostgreSQL(), "a", "b"),
			wantErr: "same length",
		},
		{
			name:    "rule 7: a repeated target is a merge by another name",
			src:     withPgSchema(directPostgreSQL(), "sales", "hr"),
			dst:     withPgSchema(directPostgreSQL(), "all", "all"),
			wantErr: "more than once",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(DBMSMigrationModel{Source: c.src, Destination: c.dst})
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("expected the model to validate, got: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("expected an error mentioning %q", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Errorf("error should mention %q, got: %v", c.wantErr, err)
			}
		})
	}
}

// --- the pipe path cannot rename ---

func TestValidate_PgSchemaRename_RejectedOnSSHToSSH(t *testing.T) {
	src := sshPostgreSQL()
	dst := withPgSchema(sshPostgreSQL(), "app")
	err := Validate(DBMSMigrationModel{Source: src, Destination: dst})
	if err == nil {
		t.Fatal("expected a rename over an ssh-tunnel-to-ssh-tunnel pipe to be rejected")
	}
	if !strings.Contains(err.Error(), "pg_dump") {
		t.Errorf("error should explain the pipe streams pg_dump output, got: %v", err)
	}
}

func TestValidate_PgSchemaSelection_AllowedOnSSHToSSH(t *testing.T) {
	// Selecting schemas is not renaming them: pg_dump takes -n per schema, so
	// only a non-empty destination list is refused on this path.
	src := withPgSchema(sshPostgreSQL(), "sales")
	dst := sshPostgreSQL()
	if err := Validate(DBMSMigrationModel{Source: src, Destination: dst}); err != nil {
		t.Fatalf("selecting source schemas over ssh-tunnel should be allowed: %v", err)
	}
}

func TestValidate_PgSchemaRename_AllowedWhenOneSideIsDirect(t *testing.T) {
	// One direct side routes the plan through a staging file, where the dump is
	// generated locally and can carry the destination names.
	src := sshPostgreSQL()
	dst := withPgSchema(directPostgreSQL(), "app")
	if err := Validate(DBMSMigrationModel{Source: src, Destination: dst}); err != nil {
		t.Fatalf("expected the rename to be allowed on the staging path, got: %v", err)
	}
}

// =============================================================================
// ProviderName — inert and engine-agnostic
// =============================================================================

// ProviderName records where a database is hosted and nothing more. Unlike
// PgSchema it is accepted on every engine and never checked against a list, so a
// provider this library has never heard of cannot block a migration.
func TestValidate_ProviderNameIsNeverRejected(t *testing.T) {
	engines := []DBMSLocation{directMySQL(), sshPostgreSQL()}
	providers := []string{
		ProviderAWS, ProviderOnPrem, ProviderOpenStack,
		"a-provider-that-does-not-exist-yet",
		"", // unspecified
	}
	for _, base := range engines {
		for _, provider := range providers {
			src, dst := base, base
			src.ProviderName = provider
			dst.ProviderName = provider
			if err := Validate(DBMSMigrationModel{Source: src, Destination: dst}); err != nil {
				t.Errorf("providerName %q on %s should be accepted, got: %v", provider, base.DBMSType, err)
			}
		}
	}
}

// The two ends are independent: migrating from a cloud provider to on-premises
// is the ordinary case, not a mismatch to report.
func TestValidate_ProviderNameMayDifferAcrossEnds(t *testing.T) {
	src := directMySQL()
	src.ProviderName = ProviderAWS
	dst := directMySQL()
	dst.ProviderName = ProviderOnPrem

	if err := Validate(DBMSMigrationModel{Source: src, Destination: dst}); err != nil {
		t.Fatalf("a cloud-to-on-premises migration should validate, got: %v", err)
	}
}
