package base

import (
	"errors"
	"strings"
	"testing"
)

// =============================================================================
// ValidateDatabaseName
// =============================================================================

func TestValidateDatabaseName_Accepted(t *testing.T) {
	cases := []struct {
		dbmsType string
		name     string
	}{
		{DBMSTypeMySQL, "app"},
		{DBMSTypeMySQL, "app_db-2024.v1"},
		{DBMSTypeMariaDB, "shop"},
		{DBMSTypePostgreSQL, "analytics"},
		// A quoted PostgreSQL identifier may hold spaces and mixed case.
		{DBMSTypePostgreSQL, "Reporting Warehouse"},
		{DBMSTypeMongoDB, "events"},
		// Multi-byte names are within the byte limit and carry no forbidden rune.
		{DBMSTypeMySQL, "café_données"},
	}
	for _, c := range cases {
		if err := ValidateDatabaseName(c.dbmsType, c.name); err != nil {
			t.Errorf("ValidateDatabaseName(%s, %q) = %v, want nil", c.dbmsType, c.name, err)
		}
	}
}

func TestValidateDatabaseName_Rejected(t *testing.T) {
	cases := []struct {
		label    string
		dbmsType string
		name     string
	}{
		{"empty", DBMSTypeMySQL, ""},
		{"nul", DBMSTypeMySQL, "app\x00db"},
		{"newline", DBMSTypePostgreSQL, "app\ndb"},
		{"carriage return", DBMSTypeMongoDB, "app\rdb"},
		// Each engine's own quoting character must not appear in the name: the
		// DDL cannot carry it as a bound parameter.
		{"back quote on mysql", DBMSTypeMySQL, "app`db"},
		{"back quote on mariadb", DBMSTypeMariaDB, "app`db"},
		{"double quote on postgresql", DBMSTypePostgreSQL, `app"db`},
		{"slash on mongodb", DBMSTypeMongoDB, "app/db"},
		{"dot on mongodb", DBMSTypeMongoDB, "app.db"},
		{"dollar on mongodb", DBMSTypeMongoDB, "app$db"},
		{"over 64 bytes on mysql", DBMSTypeMySQL, strings.Repeat("a", 65)},
		{"over 63 bytes on postgresql", DBMSTypePostgreSQL, strings.Repeat("a", 64)},
		{"over 63 bytes on mongodb", DBMSTypeMongoDB, strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			err := ValidateDatabaseName(c.dbmsType, c.name)
			if err == nil {
				t.Fatalf("ValidateDatabaseName(%s, %q) = nil, want an error", c.dbmsType, c.name)
			}
			var nameErr *InvalidDatabaseNameError
			if !errors.As(err, &nameErr) {
				t.Fatalf("error is %T, want *InvalidDatabaseNameError", err)
			}
			if nameErr.Reason == "" {
				t.Error("InvalidDatabaseNameError.Reason is empty; it should say what is wrong")
			}
		})
	}
}

func TestValidateDatabaseName_QuotingCharIsPerEngine(t *testing.T) {
	// A back quote is fatal to MySQL's quoting but ordinary to PostgreSQL, and
	// the reverse holds for a double quote. Each engine must judge only its own.
	if err := ValidateDatabaseName(DBMSTypePostgreSQL, "app`db"); err != nil {
		t.Errorf("back quote on postgresql = %v, want nil", err)
	}
	if err := ValidateDatabaseName(DBMSTypeMySQL, `app"db`); err != nil {
		t.Errorf("double quote on mysql = %v, want nil", err)
	}
}

// =============================================================================
// ValidateCreateOption
// =============================================================================

func TestValidateCreateOption_NilIsValidForEveryEngine(t *testing.T) {
	for _, dbmsType := range []string{
		DBMSTypeMySQL, DBMSTypeMariaDB, DBMSTypePostgreSQL, DBMSTypeMongoDB,
	} {
		if err := ValidateCreateOption(dbmsType, nil); err != nil {
			t.Errorf("ValidateCreateOption(%s, nil) = %v, want nil", dbmsType, err)
		}
	}
}

func TestValidateCreateOption_MySQLTokens(t *testing.T) {
	ok := &CreateDatabaseOption{
		MySQL: &MySQLCreateOption{CharacterSet: "utf8mb4", Collate: "utf8mb4_0900_ai_ci"},
	}
	if err := ValidateCreateOption(DBMSTypeMySQL, ok); err != nil {
		t.Errorf("valid charset/collate = %v, want nil", err)
	}

	// A value spliced into DDL unquoted must not be able to carry a statement.
	bad := &CreateDatabaseOption{
		MySQL: &MySQLCreateOption{CharacterSet: "utf8mb4; DROP DATABASE other"},
	}
	if err := ValidateCreateOption(DBMSTypeMySQL, bad); err == nil {
		t.Error("charset carrying a statement = nil, want an error")
	}
}

func TestValidateCreateOption_PostgreSQLEncodingRequiresTemplate(t *testing.T) {
	// template1 may already hold data in another encoding, so the server refuses
	// the combination; the option check reports it before connecting.
	missing := &CreateDatabaseOption{
		PostgreSQL: &PostgreSQLCreateOption{Encoding: "UTF8"},
	}
	err := ValidateCreateOption(DBMSTypePostgreSQL, missing)
	if err == nil {
		t.Fatal("encoding without template = nil, want an error")
	}
	if !strings.Contains(err.Error(), "template") {
		t.Errorf("error %q should name the missing template", err)
	}

	withTemplate := &CreateDatabaseOption{
		PostgreSQL: &PostgreSQLCreateOption{
			Encoding: "UTF8", LcCollate: "en_US.utf8", LcCtype: "en_US.utf8", Template: "template0",
		},
	}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, withTemplate); err != nil {
		t.Errorf("encoding with template0 = %v, want nil", err)
	}

	// Owner and template are quoted identifiers, so they must survive quoting.
	badOwner := &CreateDatabaseOption{
		PostgreSQL: &PostgreSQLCreateOption{Owner: `ops"; DROP DATABASE other; --`},
	}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, badOwner); err == nil {
		t.Error("owner carrying a double quote = nil, want an error")
	}
}

func TestValidateCreateOption_MongoDBCollection(t *testing.T) {
	ok := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{Collection: "orders"}}
	if err := ValidateCreateOption(DBMSTypeMongoDB, ok); err != nil {
		t.Errorf("valid collection = %v, want nil", err)
	}
	for _, name := range []string{"sys$tem", "system.users"} {
		bad := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{Collection: name}}
		if err := ValidateCreateOption(DBMSTypeMongoDB, bad); err == nil {
			t.Errorf("collection %q = nil, want an error", name)
		}
	}
}

func TestValidateCreateOption_MongoDBCollation(t *testing.T) {
	valid := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{
		Collection: "orders",
		Collation:  &MongoDBCollation{Locale: "ko", Strength: 2},
	}}
	if err := ValidateCreateOption(DBMSTypeMongoDB, valid); err != nil {
		t.Errorf("valid collation = %v, want nil", err)
	}

	// A collation document without a locale is rejected by the server, so it is
	// refused before a connection is made.
	noLocale := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{
		Collection: "orders",
		Collation:  &MongoDBCollation{Strength: 2},
	}}
	if err := ValidateCreateOption(DBMSTypeMongoDB, noLocale); err == nil {
		t.Error("collation without a locale = nil, want an error")
	}

	// ICU defines levels 1 to 5; 0 means "take the server default".
	for _, strength := range []int{-1, 6} {
		bad := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{
			Collection: "orders",
			Collation:  &MongoDBCollation{Locale: "ko", Strength: strength},
		}}
		if err := ValidateCreateOption(DBMSTypeMongoDB, bad); err == nil {
			t.Errorf("strength %d = nil, want an error", strength)
		}
	}

	// The locale is spliced into a mongosh expression on the SSH path, so it is
	// held to the same token shape as the other engines' charset names.
	inject := &CreateDatabaseOption{MongoDB: &MongoDBCreateOption{
		Collection: "orders",
		Collation:  &MongoDBCollation{Locale: `ko" }); db.dropDatabase(); //`},
	}}
	if err := ValidateCreateOption(DBMSTypeMongoDB, inject); err == nil {
		t.Error("a locale that is not a plain token = nil, want an error")
	}
}

func TestValidateCreateOption_PostgreSQLLocaleRules(t *testing.T) {
	// LOCALE sets LC_COLLATE and LC_CTYPE, so naming it beside either is
	// ambiguous rather than redundant.
	conflict := &CreateDatabaseOption{PostgreSQL: &PostgreSQLCreateOption{
		Template: "template0", Locale: "en_US.utf8", LcCollate: "C",
	}}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, conflict); err == nil {
		t.Error("locale together with lcCollate = nil, want an error")
	}

	// icuLocale only means anything under the icu provider.
	orphan := &CreateDatabaseOption{PostgreSQL: &PostgreSQLCreateOption{
		Template: "template0", IcuLocale: "en-US",
	}}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, orphan); err == nil {
		t.Error("icuLocale without localeProvider=icu = nil, want an error")
	}

	unknown := &CreateDatabaseOption{PostgreSQL: &PostgreSQLCreateOption{
		Template: "template0", LocaleProvider: "glibc",
	}}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, unknown); err == nil {
		t.Error("an unknown localeProvider = nil, want an error")
	}

	// Every locale setting needs a template whose own encoding matches, the
	// same rule the encoding clauses already carry.
	noTemplate := &CreateDatabaseOption{PostgreSQL: &PostgreSQLCreateOption{
		LocaleProvider: "icu", IcuLocale: "en-US",
	}}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, noTemplate); err == nil {
		t.Error("locale settings without a template = nil, want an error")
	}

	valid := &CreateDatabaseOption{PostgreSQL: &PostgreSQLCreateOption{
		Template: "template0", Encoding: "UTF8", LocaleProvider: "icu", IcuLocale: "en-US",
	}}
	if err := ValidateCreateOption(DBMSTypePostgreSQL, valid); err != nil {
		t.Errorf("a complete icu option = %v, want nil", err)
	}
}

func TestValidateCreateOption_IgnoresOtherEnginesSubStruct(t *testing.T) {
	// Only the sub-struct matching the engine is consulted, so a bogus PostgreSQL
	// block must not fail a MySQL create.
	opt := &CreateDatabaseOption{
		MySQL:      &MySQLCreateOption{CharacterSet: "utf8mb4"},
		PostgreSQL: &PostgreSQLCreateOption{Encoding: "not a token!"},
	}
	if err := ValidateCreateOption(DBMSTypeMySQL, opt); err != nil {
		t.Errorf("ValidateCreateOption(mysql, …) = %v, want nil", err)
	}
}

// =============================================================================
// Option Accessors and Outcome Decisions
// =============================================================================

func TestCreateOptAccessors_TolerateNil(t *testing.T) {
	if got := MySQLCreateOpt(nil); got != (MySQLCreateOption{}) {
		t.Errorf("MySQLCreateOpt(nil) = %+v, want zero value", got)
	}
	if got := MariaDBCreateOpt(&CreateDatabaseOption{}); got != (MariaDBCreateOption{}) {
		t.Errorf("MariaDBCreateOpt(empty) = %+v, want zero value", got)
	}
	if got := PostgreSQLCreateOpt(nil); got != (PostgreSQLCreateOption{}) {
		t.Errorf("PostgreSQLCreateOpt(nil) = %+v, want zero value", got)
	}
	if got := MongoDBCreateOpt(&CreateDatabaseOption{}); got != (MongoDBCreateOption{}) {
		t.Errorf("MongoDBCreateOpt(empty) = %+v, want zero value", got)
	}
	if got := PostgreSQLDropOpt(nil); got.Force {
		t.Error("PostgreSQLDropOpt(nil).Force = true, want false")
	}
}

func TestCreateExistsResult(t *testing.T) {
	err := CreateExistsResult(nil, DBMSTypeMySQL, "app")
	var exists *TargetDatabaseExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("nil option = %v (%T), want *TargetDatabaseExistsError", err, err)
	}
	if exists.Database != "app" || exists.DBMSType != DBMSTypeMySQL {
		t.Errorf("error carries %s/%q, want mysql/app", exists.DBMSType, exists.Database)
	}

	if err := CreateExistsResult(&CreateDatabaseOption{IfNotExists: true}, DBMSTypeMySQL, "app"); err != nil {
		t.Errorf("IfNotExists = %v, want nil", err)
	}
}

func TestDropMissingResult(t *testing.T) {
	err := DropMissingResult(nil, DBMSTypePostgreSQL, "app")
	var missing *TargetDatabaseNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("nil option = %v (%T), want *TargetDatabaseNotFoundError", err, err)
	}

	if err := DropMissingResult(&DropDatabaseOption{IfExists: true}, DBMSTypePostgreSQL, "app"); err != nil {
		t.Errorf("IfExists = %v, want nil", err)
	}
}

func TestDropRequiresEmpty_DefaultsToUnconditional(t *testing.T) {
	// DropDatabase removes the database and its contents unless the caller opts
	// into the guard, so both a nil option and an empty one mean "no guard".
	if DropRequiresEmpty(nil) {
		t.Error("DropRequiresEmpty(nil) = true, want false")
	}
	if DropRequiresEmpty(&DropDatabaseOption{}) {
		t.Error("DropRequiresEmpty(empty) = true, want false")
	}
	if !DropRequiresEmpty(&DropDatabaseOption{RequireEmpty: true}) {
		t.Error("DropRequiresEmpty(RequireEmpty) = false, want true")
	}
}

// =============================================================================
// System Databases
// =============================================================================

func TestIsSystemDatabase_CoversEveryEngine(t *testing.T) {
	// CreateDatabase and DropDatabase refuse these names, and they are among the
	// names ListDatabases hides — the managed-service ones are covered by
	// TestIsSystemDatabase_ManagedServiceDatabases.
	blocked := map[string][]string{
		DBMSTypeMySQL:      {"information_schema", "mysql", "performance_schema", "sys"},
		DBMSTypeMariaDB:    {"information_schema", "mysql", "performance_schema", "sys"},
		DBMSTypePostgreSQL: {"postgres", "template0", "template1"},
		DBMSTypeMongoDB:    {"admin", "config", "local"},
	}
	for dbmsType, names := range blocked {
		for _, name := range names {
			if !IsSystemDatabase(dbmsType, name) {
				t.Errorf("IsSystemDatabase(%s, %q) = false, want true", dbmsType, name)
			}
		}
		if IsSystemDatabase(dbmsType, "app") {
			t.Errorf("IsSystemDatabase(%s, \"app\") = true, want false", dbmsType)
		}
	}
}
