package base

import (
	"context"
	"io"
)

// DBDriver is the unified interface for all supported database engines.
type DBDriver interface {
	// Dump writes the database at loc to outputPath according to scope, copying
	// the bytes to w as it goes when w is non-nil. opt carries the choices that
	// depend on where the dump is headed — currently only a PostgreSQL schema
	// rename — and may be nil, which reproduces the source as it stands.
	Dump(ctx context.Context, loc DBMSLocation, scope string, outputPath string, w io.Writer, opt *DumpOption) error
	Restore(ctx context.Context, loc DBMSLocation, inputPath string, notify func(table string)) error
	TestConnection(ctx context.Context, loc DBMSLocation) error
	BuildDumpCmd(loc DBMSLocation, scope string) string
	BuildRestoreCmd(loc DBMSLocation) string
	CheckTargetEmpty(ctx context.Context, loc DBMSLocation) error

	// ListDatabases returns the names of the user databases on loc's server,
	// sorted, with the engine's system databases omitted. It is a server-level
	// call: loc.Database is ignored and need not exist, since the driver enters
	// through whichever database the engine always provides. The result covers
	// only what loc's credentials are permitted to see.
	ListDatabases(ctx context.Context, loc DBMSLocation) ([]string, error)

	// ListSchemas returns the names of the user schemas inside loc.Database,
	// sorted, with the engine's system schemas omitted. loc.PgSchema is ignored:
	// this is what a caller consults to decide what to put there.
	//
	// Only PostgreSQL separates schemas from databases. MySQL and MariaDB treat
	// the two as synonyms and MongoDB has no equivalent, so those drivers return
	// (nil, nil) — an engine without the concept reports no schemas rather than
	// an error, which lets a caller probe any location uniformly.
	ListSchemas(ctx context.Context, loc DBMSLocation) ([]string, error)

	// CreateDatabase creates loc.Database on loc's server. Like ListDatabases it
	// is a server-level call — the connection enters through a database the engine
	// always provides, since loc.Database does not exist yet — but loc.Database is
	// required rather than ignored: it names what gets created. Returns
	// TargetDatabaseExistsError when the database is already there, unless
	// opt.IfNotExists is set; opt may be nil for engine defaults.
	//
	// MongoDB has no CREATE DATABASE: the call succeeds without server-side DDL
	// and materialises the database only when opt.MongoDB.Collection is set.
	CreateDatabase(ctx context.Context, loc DBMSLocation, opt *CreateDatabaseOption) error

	// DropDatabase drops loc.Database and everything in it, entering through the
	// same server-level connection CreateDatabase uses — a database cannot be
	// dropped by a session connected to it. Returns TargetDatabaseNotFoundError
	// when it does not exist, unless opt.IfExists is set, and TargetNotEmptyError
	// when opt.RequireEmpty is set and the database still holds objects.
	//
	// This is not DropAllObjects: that empties a database and keeps it, this
	// removes the database itself.
	DropDatabase(ctx context.Context, loc DBMSLocation, opt *DropDatabaseOption) error

	Inspect(ctx context.Context, loc DBMSLocation) (*DBMSInfo, error)
	DescribeTable(ctx context.Context, loc DBMSLocation, table string) (*TableInfo, error)

	// CharsetProfile reports loc's text handling: the database's own defaults,
	// the character set and collation names its objects carry, and the names its
	// server recognises. Unlike Inspect it touches no table statistics — a few
	// catalog queries and nothing per table — because a migration runs it on both
	// endpoints before it starts.
	//
	// loc.Filter narrows the "used" lists to the objects a dump would actually
	// carry, so a collation only a filtered-out table depends on cannot block a
	// migration that never touches it.
	//
	// MongoDB has neither character sets nor server-level collation names and
	// returns an empty profile rather than an error, so a caller can profile any
	// location uniformly.
	CharsetProfile(ctx context.Context, loc DBMSLocation) (*CharsetProfile, error)

	// ServerVersion returns the version string loc's server identifies itself
	// by, as reported — vendor suffixes and all. Like CharsetProfile it is a
	// pre-flight call a migration makes on both endpoints before it starts: one
	// query, no table statistics.
	//
	// It is a server-level call. loc.Database is not read and need not exist,
	// since the connection enters through whichever database the engine always
	// provides — which is what lets a target be checked before its first
	// migration has created anything.
	ServerVersion(ctx context.Context, loc DBMSLocation) (string, error)

	// PrepareTarget grants access to a pre-existing, empty target database.
	// The database must already exist — it is NOT created — otherwise
	// TargetDatabaseNotFoundError is returned. If it exists but is not empty,
	// TargetNotEmptyError is returned. On success the driver creates the grant
	// user and privileges. (MongoDB creates databases lazily, so it enforces
	// only emptiness; TargetDatabaseNotFoundError does not apply there.)
	PrepareTarget(ctx context.Context, loc DBMSLocation, grant *TargetGrant) error

	// DropAllObjects removes every object inside loc.Database (collections for
	// MongoDB) but never drops the database itself — the target database is
	// assumed to be pre-provisioned and must survive rollback. Used by the
	// rollback-before-retry flow for engines whose dump format has no
	// per-table/collection SQL DROP equivalent (currently only MongoDB; SQL
	// engines roll back via a generated DROP script instead and implement this
	// as a no-op).
	DropAllObjects(ctx context.Context, loc DBMSLocation) error
}
