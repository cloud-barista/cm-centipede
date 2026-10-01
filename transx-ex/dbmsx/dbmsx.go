package dbmsx

import (
	"context"
	"fmt"
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Public API — Migration
// =============================================================================

// MigrateData runs a full migration synchronously.
// It validates the destination is empty, plans the pipeline, executes it, and
// — when RollbackOnFailure is true — attempts a best-effort rollback that drops
// every object created in the destination database (the database itself is left
// intact) if any step fails.
func MigrateData(dmm DBMSMigrationModel) error {
	logger.Info("MigrateData started",
		slog.String("dbms", dmm.Destination.DBMSType),
		slog.String("database", dmm.Destination.Database),
		slog.String("scope", dmm.Scope),
	)
	ctx := context.Background()

	// Validate before touching the destination. Plan validates too, but it runs
	// after PrepareTarget and CheckTargetEmpty — an infeasible model would
	// otherwise get a grant user created for a migration that cannot start.
	if err := Validate(dmm); err != nil {
		return err
	}

	dstDriver, err := drvpkg.NewDBDriver(dmm.Destination.DBMSType)
	if err != nil {
		return err
	}
	if dmm.TargetGrant != nil {
		if err := dstDriver.PrepareTarget(ctx, dmm.Destination, dmm.TargetGrant); err != nil {
			return err
		}
	}
	if err := dstDriver.CheckTargetEmpty(ctx, dmm.Destination); err != nil {
		return err
	}
	// Server version, then character-set compatibility — both before any data is
	// read. A target that cannot parse what the dump will carry fails on its
	// first CREATE statement, which on a full migration is hours after the dump
	// began. Version comes first: it is the cheaper question and the more
	// fundamental one.
	if err := enforceVersion(ctx, dmm); err != nil {
		return err
	}
	if err := enforceCharset(ctx, dmm); err != nil {
		return err
	}

	pipeline, err := Plan(dmm)
	if err != nil {
		return err
	}

	if err := pipeline.Execute(ctx); err != nil {
		logger.Error("MigrateData failed", slog.String("err", err.Error()))
		if dmm.RollbackOnFailure {
			_ = rollbackDrop(ctx, dmm.Destination, dstDriver)
		}
		return err
	}
	logger.Info("MigrateData completed",
		slog.String("dbms", dmm.Destination.DBMSType),
		slog.String("database", dmm.Destination.Database),
	)
	return nil
}

// MigrateDataAsync launches a migration in a background goroutine and returns
// a MigrationHandle immediately. Callers use the handle to poll progress, wait
// for completion, or cancel the operation.
func MigrateDataAsync(dmm DBMSMigrationModel) *MigrationHandle {
	ctx, cancel := context.WithCancel(context.Background())
	h := newMigrationHandle(ctx, cancel)
	go func() {
		h.run(dmm) //nolint:errcheck — the error is recorded in the handle, returned by Wait()
		h.Finish()
	}()
	return h
}

// =============================================================================
// Public API — Single-Operation Helpers
// =============================================================================

// Dump exports the database described by loc to outputPath using the given scope.
// scope must be one of ScopeSchemaOnly, ScopeDataOnly, or ScopeFull.
func Dump(loc DBMSLocation, scope, outputPath string) error {
	logger.Info("Dump started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("scope", scope),
	)
	if err := validateLocation("source", loc); err != nil {
		return err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return err
	}
	return drv.Dump(context.Background(), loc, scope, outputPath, nil, nil)
}

// Restore imports the file at inputPath into the database described by loc.
func Restore(loc DBMSLocation, inputPath string) error {
	logger.Info("Restore started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("inputPath", inputPath),
	)
	if err := validateLocation("destination", loc); err != nil {
		return err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return err
	}
	return drv.Restore(context.Background(), loc, inputPath, nil)
}

// ListDatabases returns the names of the user databases on the server described
// by loc, sorted, with the engine's system databases omitted.
//
// It is a server-level call: loc.Database is not required and is ignored, since
// each driver enters through a database the engine always provides (PostgreSQL
// through "postgres", MongoDB through "admin"). The result covers only what
// loc's credentials are permitted to see.
func ListDatabases(loc DBMSLocation) ([]string, error) {
	logger.Info("ListDatabases started",
		slog.String("dbms", loc.DBMSType),
		slog.String("access", loc.AccessType),
	)
	if err := validateServerLocation("source", loc); err != nil {
		return nil, err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	return drv.ListDatabases(context.Background(), loc)
}

// ListSchemas returns the names of the user schemas inside loc.Database,
// sorted, with the engine's system schemas omitted.
//
// loc.PgSchema is ignored: this is the call that tells a caller what may go
// there. Only PostgreSQL separates schemas from databases — MySQL and MariaDB
// treat the two as synonyms and MongoDB has no equivalent — so those engines
// return an empty list rather than an error, and a caller can probe any
// location uniformly.
func ListSchemas(loc DBMSLocation) ([]string, error) {
	logger.Info("ListSchemas started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("access", loc.AccessType),
	)
	if err := validateServerLocation("source", loc); err != nil {
		return nil, err
	}
	// Unlike ListDatabases this reads inside one database, so it has to exist.
	if loc.Database == "" {
		return nil, fmt.Errorf("source.database is required")
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	return drv.ListSchemas(context.Background(), loc)
}

// CreateDatabase creates the database named by loc.Database on loc's server.
//
// It is a server-level call in the same sense as ListDatabases: the connection
// enters through a database the engine always provides (PostgreSQL "postgres",
// MongoDB "admin", MySQL/MariaDB with none selected), because loc.Database is
// what is about to be created. Unlike ListDatabases, loc.Database is required —
// it names the new database — and it must not be one of the engine's system
// databases.
//
// An existing database yields *TargetDatabaseExistsError unless
// opt.IfNotExists is set, which turns the call into a no-op success. opt may be
// nil, in which case the engine's defaults apply.
//
// MongoDB has no CREATE DATABASE: the call succeeds without server-side DDL and
// materialises the database only when opt.MongoDB.Collection is set.
func CreateDatabase(loc DBMSLocation, opt *CreateDatabaseOption) error {
	logger.Info("CreateDatabase started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("access", loc.AccessType),
	)
	if err := validateDatabaseLocation("target", loc); err != nil {
		return err
	}
	// A system database can neither be created nor meaningfully replaced, and the
	// name list is the one ListDatabases already hides.
	if drvpkg.IsSystemDatabase(loc.DBMSType, loc.Database) {
		return &SystemDatabaseError{
			DBMSType: loc.DBMSType, Database: loc.Database, Operation: "create",
		}
	}
	if err := drvpkg.ValidateCreateOption(loc.DBMSType, opt); err != nil {
		return err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return err
	}
	return drv.CreateDatabase(context.Background(), loc, opt)
}

// DropDatabase drops the database named by loc.Database, and everything in it,
// from loc's server. It enters through the same server-level connection
// CreateDatabase uses: a database cannot be dropped by a session connected to it.
//
// The default is unconditional, as the name says. opt.RequireEmpty refuses a
// database that still holds objects (*TargetNotEmptyError), and a missing
// database yields *TargetDatabaseNotFoundError unless opt.IfExists is set.
// System databases are refused outright.
//
// This is not RollbackDrop: that empties a database and leaves it in place,
// this removes the database itself.
func DropDatabase(loc DBMSLocation, opt *DropDatabaseOption) error {
	logger.Info("DropDatabase started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("access", loc.AccessType),
	)
	if err := validateDatabaseLocation("target", loc); err != nil {
		return err
	}
	// Dropping a catalog, a template or an internal bookkeeping database breaks
	// the server; refuse before connecting.
	if drvpkg.IsSystemDatabase(loc.DBMSType, loc.Database) {
		return &SystemDatabaseError{
			DBMSType: loc.DBMSType, Database: loc.Database, Operation: "drop",
		}
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return err
	}
	return drv.DropDatabase(context.Background(), loc, opt)
}

// Inspect returns server-level and schema-level metadata for the database at loc.
func Inspect(loc DBMSLocation) (*DBMSInfo, error) {
	logger.Info("Inspect started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
	)
	if err := validateLocation("source", loc); err != nil {
		return nil, err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	return drv.Inspect(context.Background(), loc)
}

// InspectAll inspects every user database on the server described by loc and
// returns their metadata, sorted by database name.
//
// It is the server-level counterpart of Inspect: loc.Database is ignored, since
// the databases are the ones ListDatabases enumerates — system databases already
// excluded, names already sorted. loc.Metric applies to each of them alike.
//
// A database that fails on its own — dropped mid-scan, or not readable by loc's
// credentials — is reported in the second return value and does not abort the
// call. The error is returned only when nothing could be inspected at all:
// loc is invalid, the engine has no driver, or the enumeration itself failed.
func InspectAll(loc DBMSLocation) ([]DBMSInfo, []InspectError, error) {
	logger.Info("InspectAll started",
		slog.String("dbms", loc.DBMSType),
		slog.String("access", loc.AccessType),
	)
	if err := validateServerLocation("source", loc); err != nil {
		return nil, nil, err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	names, err := drv.ListDatabases(ctx, loc)
	if err != nil {
		return nil, nil, err
	}

	infos := make([]DBMSInfo, 0, len(names))
	var skipped []InspectError
	for _, name := range names {
		dbLoc := loc
		dbLoc.Database = name
		info, err := drv.Inspect(ctx, dbLoc)
		if err != nil {
			logger.Warn("InspectAll skipped a database",
				slog.String("dbms", loc.DBMSType),
				slog.String("database", name),
				slog.String("error", err.Error()),
			)
			skipped = append(skipped, InspectError{
				Database: name,
				Message:  err.Error(),
				Err:      err,
			})
			continue
		}
		infos = append(infos, *info)
	}
	logger.Info("InspectAll finished",
		slog.String("dbms", loc.DBMSType),
		slog.Int("inspected", len(infos)),
		slog.Int("skipped", len(skipped)),
	)
	return infos, skipped, nil
}

// RollbackDrop drops every object inside the destination database described by
// loc — tables, views, routines, triggers, events and engine-specific objects
// (PostgreSQL sequences/types/extensions, MongoDB collections) — but leaves the
// database itself intact. It is best-effort: individual drop failures are ignored.
// Intended for use before a retry migration to ensure the target is clean.
func RollbackDrop(loc DBMSLocation) error {
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return err
	}
	return rollbackDrop(context.Background(), loc, drv)
}

// DescribeTable returns detailed column and index metadata for the named table
// in the database at loc.
func DescribeTable(loc DBMSLocation, table string) (*TableInfo, error) {
	logger.Info("DescribeTable started",
		slog.String("dbms", loc.DBMSType),
		slog.String("database", loc.Database),
		slog.String("table", table),
	)
	if err := validateLocation("source", loc); err != nil {
		return nil, err
	}
	drv, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return nil, err
	}
	return drv.DescribeTable(context.Background(), loc, table)
}
