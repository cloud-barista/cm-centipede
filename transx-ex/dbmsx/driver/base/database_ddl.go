package base

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// =============================================================================
// Create Options
// =============================================================================

// CreateDatabaseOption selects the engine-specific attributes of a new database.
// It splits by engine because the attributes do not overlap — character sets are
// MySQL/MariaDB wording, encoding and templates are PostgreSQL wording, and
// MongoDB has no CREATE DATABASE at all — the same split MetricOption uses. Only
// the field matching DBMSLocation.DBMSType is consulted, and a nil option means
// engine defaults throughout.
type CreateDatabaseOption struct {
	// IfNotExists turns an existing database into a no-op success instead of
	// TargetDatabaseExistsError. It is enforced by the drivers' existence
	// pre-check rather than the engine's own clause, so every engine behaves
	// alike: PostgreSQL has no IF NOT EXISTS for CREATE DATABASE.
	IfNotExists bool `json:"ifNotExists,omitempty"`

	MySQL      *MySQLCreateOption      `json:"mysql,omitempty"`
	MariaDB    *MariaDBCreateOption    `json:"mariadb,omitempty"`
	PostgreSQL *PostgreSQLCreateOption `json:"postgresql,omitempty"`
	MongoDB    *MongoDBCreateOption    `json:"mongodb,omitempty"`
}

// MySQLCreateOption carries the optional clauses of CREATE DATABASE. An empty
// field is omitted from the statement, leaving the server default in place.
type MySQLCreateOption struct {
	CharacterSet string `json:"characterSet,omitempty"` // e.g. "utf8mb4"
	Collate      string `json:"collate,omitempty"`      // e.g. "utf8mb4_0900_ai_ci"
}

// MariaDBCreateOption mirrors MySQLCreateOption: MariaDB is MySQL-compatible, so
// the clauses match. It is a distinct type to allow the two engines to diverge
// later, as MariaDBMetric is to MySQLMetric.
type MariaDBCreateOption struct {
	CharacterSet string `json:"characterSet,omitempty"` // e.g. "utf8mb4"
	Collate      string `json:"collate,omitempty"`      // e.g. "utf8mb4_general_ci"
}

// PostgreSQLCreateOption carries the optional clauses of CREATE DATABASE.
//
// Encoding, LcCollate and LcCtype require Template to be a template database
// whose own encoding matches — in practice "template0", since the default
// "template1" may already hold data in another encoding and the server then
// rejects the request. ValidateCreateOption reports that combination up front
// rather than letting the engine fail mid-call.
type PostgreSQLCreateOption struct {
	Owner     string `json:"owner,omitempty"`
	Encoding  string `json:"encoding,omitempty"`  // e.g. "UTF8"
	LcCollate string `json:"lcCollate,omitempty"` // e.g. "en_US.utf8"
	LcCtype   string `json:"lcCtype,omitempty"`   // e.g. "en_US.utf8"
	Template  string `json:"template,omitempty"`  // e.g. "template0"

	// Locale sets LC_COLLATE and LC_CTYPE together, which is what most callers
	// mean. It cannot be combined with either of them.
	Locale string `json:"locale,omitempty"` // e.g. "en_US.utf8"

	// LocaleProvider selects which library supplies the collation rules: "libc",
	// the host's own locales, or "icu", whose rules travel with the server
	// instead of the operating system. PostgreSQL 15 and later only.
	//
	// This is worth reaching for precisely because a libc collation exists only
	// where the host has that locale installed — the reason a migration between
	// two same-version servers can still fail on a slim container.
	LocaleProvider string `json:"localeProvider,omitempty"`

	// IcuLocale names the ICU locale, e.g. "en-US". It requires LocaleProvider
	// to be "icu".
	IcuLocale string `json:"icuLocale,omitempty"`
}

// MongoDBCreateOption materialises the database. MongoDB has no CREATE DATABASE:
// a database begins to exist when its first collection appears. With Collection
// set the database is materialised by creating that (empty) collection; with it
// unset the call is a documented no-op and the database stays invisible to
// ListDatabases until the migration writes to it.
type MongoDBCreateOption struct {
	Collection string `json:"collection,omitempty"`

	// Collation is the default collation of that collection — the comparison
	// rules its queries and indexes use unless they name their own. It has no
	// effect without Collection, since there is no collection to carry it and
	// MongoDB has nothing at the database level to attach it to; the driver logs
	// and ignores it in that case rather than failing, matching how a Collection
	// on its own is a documented no-op.
	Collation *MongoDBCollation `json:"collation,omitempty"`
}

// MongoDBCollation selects the comparison rules a collection uses. It mirrors
// the subset of MongoDB's collation document that changes sort order in ways a
// migration cares about; the fields it leaves out (alternate, maxVariable,
// backwards, normalization) take the server's defaults.
type MongoDBCollation struct {
	// Locale is the ICU locale, e.g. "ko", "en_US", or "simple" for plain
	// byte-wise comparison. It is required: a collation document without one is
	// rejected by the server.
	Locale string `json:"locale"`
	// Strength is the ICU comparison level, 1 to 5. Empty (0) takes the server
	// default of 3, which compares case and accents.
	Strength int `json:"strength,omitempty"`
	// CaseLevel adds case comparison to strength 1 or 2, which otherwise ignore
	// it.
	CaseLevel bool `json:"caseLevel,omitempty"`
	// NumericOrdering compares embedded digits as numbers, so "item10" sorts
	// after "item2" rather than before it.
	NumericOrdering bool `json:"numericOrdering,omitempty"`
}

// =============================================================================
// Drop Options
// =============================================================================

// DropDatabaseOption governs how far a drop is allowed to go. A nil option drops
// the database and everything in it, which is what the name says; the guards
// below are opt-in for callers that want a safety net.
type DropDatabaseOption struct {
	// IfExists turns a missing database into a no-op success instead of
	// TargetDatabaseNotFoundError.
	IfExists bool `json:"ifExists,omitempty"`

	// RequireEmpty refuses to drop a database that still holds tables or
	// collections, returning TargetNotEmptyError. It reuses the driver's
	// CheckTargetEmpty, so the emptiness rule matches the migration pre-check.
	RequireEmpty bool `json:"requireEmpty,omitempty"`

	PostgreSQL *PostgreSQLDropOption `json:"postgresql,omitempty"`
}

// PostgreSQLDropOption handles the one obstacle unique to PostgreSQL: a database
// with live sessions cannot be dropped.
type PostgreSQLDropOption struct {
	// Force terminates the other backends connected to the database before
	// dropping it. It is implemented with pg_terminate_backend rather than the
	// WITH (FORCE) clause, which the server only accepts from PostgreSQL 13 on.
	Force bool `json:"force,omitempty"`
}

// =============================================================================
// Option Accessors
// =============================================================================
//
// Each accessor tolerates a nil option and a nil sub-struct, so driver code can
// read a field without a preceding nil dance. The zero value of every sub-struct
// means "engine default", which is exactly what a nil option should produce.

// MySQLCreateOpt returns opt's MySQL sub-struct, or a zero value.
func MySQLCreateOpt(opt *CreateDatabaseOption) MySQLCreateOption {
	if opt == nil || opt.MySQL == nil {
		return MySQLCreateOption{}
	}
	return *opt.MySQL
}

// MariaDBCreateOpt returns opt's MariaDB sub-struct, or a zero value.
func MariaDBCreateOpt(opt *CreateDatabaseOption) MariaDBCreateOption {
	if opt == nil || opt.MariaDB == nil {
		return MariaDBCreateOption{}
	}
	return *opt.MariaDB
}

// PostgreSQLCreateOpt returns opt's PostgreSQL sub-struct, or a zero value.
func PostgreSQLCreateOpt(opt *CreateDatabaseOption) PostgreSQLCreateOption {
	if opt == nil || opt.PostgreSQL == nil {
		return PostgreSQLCreateOption{}
	}
	return *opt.PostgreSQL
}

// MongoDBCreateOpt returns opt's MongoDB sub-struct, or a zero value.
func MongoDBCreateOpt(opt *CreateDatabaseOption) MongoDBCreateOption {
	if opt == nil || opt.MongoDB == nil {
		return MongoDBCreateOption{}
	}
	return *opt.MongoDB
}

// PostgreSQLDropOpt returns opt's PostgreSQL sub-struct, or a zero value.
func PostgreSQLDropOpt(opt *DropDatabaseOption) PostgreSQLDropOption {
	if opt == nil || opt.PostgreSQL == nil {
		return PostgreSQLDropOption{}
	}
	return *opt.PostgreSQL
}

// =============================================================================
// Shared Outcome Decisions
// =============================================================================
//
// Create and drop each have one branch every driver must resolve identically —
// what to do when the database is already there, or already gone. Deciding it
// here keeps the four drivers from drifting apart on the option semantics.

// CreateExistsResult is what a driver returns once it has found the database it
// was asked to create: nil when the caller passed IfNotExists, and
// TargetDatabaseExistsError otherwise.
func CreateExistsResult(opt *CreateDatabaseOption, dbmsType, database string) error {
	if opt != nil && opt.IfNotExists {
		return nil
	}
	return &TargetDatabaseExistsError{DBMSType: dbmsType, Database: database}
}

// DropMissingResult is what a driver returns once it has found the database it
// was asked to drop absent: nil when the caller passed IfExists, and
// TargetDatabaseNotFoundError otherwise.
func DropMissingResult(opt *DropDatabaseOption, dbmsType, database string) error {
	if opt != nil && opt.IfExists {
		return nil
	}
	return &TargetDatabaseNotFoundError{DBMSType: dbmsType, Database: database}
}

// DropRequiresEmpty reports whether the caller asked for the emptiness guard.
// The default is an unconditional drop: DropDatabase removes the database and
// its contents, as the name says.
func DropRequiresEmpty(opt *DropDatabaseOption) bool {
	return opt != nil && opt.RequireEmpty
}

// =============================================================================
// Name Validation
// =============================================================================

// maxDatabaseNameLen is the longest database name each engine accepts, in bytes:
// 64 for MySQL/MariaDB identifiers, 63 for a PostgreSQL identifier (NAMEDATALEN
// minus the terminator) and 63 for a MongoDB database name (the limit the server
// enforces on non-Windows hosts).
var maxDatabaseNameLen = map[string]int{
	DBMSTypeMySQL:      64,
	DBMSTypeMariaDB:    64,
	DBMSTypePostgreSQL: 63,
	DBMSTypeMongoDB:    63,
}

// mongoForbiddenNameChars lists the characters MongoDB rejects in a database
// name, because they collide with its on-disk file naming and its
// namespace separator.
const mongoForbiddenNameChars = `/\. "$*<>:|?`

// ValidateDatabaseName reports whether name is usable as a database name on
// dbmsType. Create and drop build their DDL by quoting this name — a statement
// cannot carry it as a bound parameter — so a name that breaks out of its quoting
// must be refused before it reaches the engine, not merely escaped.
//
// Rejected: an empty name, one longer than the engine allows, one holding a NUL
// or a line break, one holding the engine's own quoting character, and for
// MongoDB one holding any of mongoForbiddenNameChars.
func ValidateDatabaseName(dbmsType, name string) error {
	reject := func(reason string) error {
		return &InvalidDatabaseNameError{DBMSType: dbmsType, Database: name, Reason: reason}
	}

	if name == "" {
		return reject("name is empty")
	}
	if !utf8.ValidString(name) {
		return reject("name is not valid UTF-8")
	}
	if max, ok := maxDatabaseNameLen[dbmsType]; ok && len(name) > max {
		return reject(fmt.Sprintf("name is longer than the %d bytes %s allows", max, dbmsType))
	}
	if strings.ContainsAny(name, "\x00\n\r") {
		return reject("name contains a NUL or a line break")
	}

	switch dbmsType {
	case DBMSTypeMySQL, DBMSTypeMariaDB:
		// Back quotes delimit the identifier in the generated DDL.
		if strings.Contains(name, "`") {
			return reject("name contains a back quote")
		}
	case DBMSTypePostgreSQL:
		// Double quotes delimit the identifier in the generated DDL.
		if strings.Contains(name, `"`) {
			return reject("name contains a double quote")
		}
	case DBMSTypeMongoDB:
		if strings.ContainsAny(name, mongoForbiddenNameChars) {
			return reject("name contains a character MongoDB forbids (" + mongoForbiddenNameChars + ")")
		}
	}
	return nil
}

// identTokenAllowed reports whether s is safe to splice into DDL unquoted: the
// character sets, collations, encodings and locale names the create options
// carry. These are not identifiers the engine lets us quote uniformly, so they
// are constrained to a conservative token shape instead.
func identTokenAllowed(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return s != ""
}

// ValidateCreateOption checks the create option for dbmsType before any
// connection is made: the option values that get spliced into DDL must have a
// safe token shape, and PostgreSQL's encoding/locale clauses must come with the
// template that makes them acceptable to the server.
func ValidateCreateOption(dbmsType string, opt *CreateDatabaseOption) error {
	token := func(field, value string) error {
		if value == "" || identTokenAllowed(value) {
			return nil
		}
		return fmt.Errorf("createDatabase option %s %q is not a valid token: "+
			"only letters, digits, '_', '-' and '.' are allowed", field, value)
	}

	switch dbmsType {
	case DBMSTypeMySQL:
		o := MySQLCreateOpt(opt)
		if err := token("characterSet", o.CharacterSet); err != nil {
			return err
		}
		return token("collate", o.Collate)

	case DBMSTypeMariaDB:
		o := MariaDBCreateOpt(opt)
		if err := token("characterSet", o.CharacterSet); err != nil {
			return err
		}
		return token("collate", o.Collate)

	case DBMSTypePostgreSQL:
		o := PostgreSQLCreateOpt(opt)
		for _, f := range []struct{ name, value string }{
			{"encoding", o.Encoding},
			{"lcCollate", o.LcCollate},
			{"lcCtype", o.LcCtype},
			{"locale", o.Locale},
			{"icuLocale", o.IcuLocale},
		} {
			if err := token(f.name, f.value); err != nil {
				return err
			}
		}
		// Owner and Template are identifiers, so they are double-quoted rather
		// than token-checked; they must still survive that quoting.
		if strings.ContainsAny(o.Owner+o.Template, "\"\x00\n\r") {
			return fmt.Errorf("createDatabase option owner/template must not contain " +
				"a double quote, a NUL or a line break")
		}
		if o.Locale != "" && (o.LcCollate != "" || o.LcCtype != "") {
			return fmt.Errorf("createDatabase postgresql locale cannot be combined with " +
				"lcCollate or lcCtype: locale sets both, so naming them together is ambiguous")
		}
		switch o.LocaleProvider {
		case "", "libc", "icu":
			// valid
		default:
			return fmt.Errorf("createDatabase postgresql localeProvider %q is not supported: "+
				`it must be "libc" or "icu"`, o.LocaleProvider)
		}
		if o.IcuLocale != "" && o.LocaleProvider != "icu" {
			return fmt.Errorf("createDatabase postgresql icuLocale requires localeProvider " +
				`to be "icu"`)
		}
		if (o.Encoding != "" || o.LcCollate != "" || o.LcCtype != "" ||
			o.Locale != "" || o.LocaleProvider != "" || o.IcuLocale != "") && o.Template == "" {
			return fmt.Errorf("createDatabase postgresql encoding/locale settings require " +
				`template to be set (usually "template0"): the default template1 may hold ` +
				"data in another encoding or locale, which the server then refuses")
		}

	case DBMSTypeMongoDB:
		o := MongoDBCreateOpt(opt)
		if o.Collection != "" {
			if strings.ContainsAny(o.Collection, "\x00$") || strings.HasPrefix(o.Collection, "system.") {
				return fmt.Errorf("createDatabase mongodb collection %q is not usable: "+
					"a collection name must not contain a NUL or '$', nor start with \"system.\"",
					o.Collection)
			}
		}
		if c := o.Collation; c != nil {
			// The locale is checked for shape rather than against the ICU list:
			// that list grows with the server's ICU version, so rejecting an
			// unknown name here would refuse locales the server accepts. A
			// malformed one is refused by the server with a clear message.
			if c.Locale == "" {
				return fmt.Errorf("createDatabase mongodb collation requires a locale " +
					`(an ICU locale such as "ko", or "simple" for byte-wise comparison)`)
			}
			if !identTokenAllowed(c.Locale) {
				return fmt.Errorf("createDatabase mongodb collation locale %q is not a valid token: "+
					"only letters, digits, '_', '-' and '.' are allowed", c.Locale)
			}
			if c.Strength < 0 || c.Strength > 5 {
				return fmt.Errorf("createDatabase mongodb collation strength %d is out of range: "+
					"it must be 1 to 5, or 0 to take the server default", c.Strength)
			}
		}
	}
	return nil
}
