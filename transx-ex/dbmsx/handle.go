package dbmsx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
	execpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/executor"
)

// =============================================================================
// MigrationHandle
// =============================================================================

// MigrationHandle provides async control over a running migration.
// Obtain one via MigrateDataAsync; poll with Status()/Progress(); block with Wait().
//
// The embedded core.Handle supplies the lifecycle — Status, Progress, Wait,
// Cancel — shared with the storage domain.
type MigrationHandle struct {
	*core.Handle[MigrationProgress, *MigrationProgress]
}

// newMigrationHandle constructs a handle in StatusPending state.
// The goroutine that calls run() is responsible for calling Finish().
func newMigrationHandle(ctx context.Context, cancel context.CancelFunc) *MigrationHandle {
	return &MigrationHandle{core.NewHandle[MigrationProgress, *MigrationProgress](ctx, cancel)}
}

// =============================================================================
// Internal Progress Helpers
// =============================================================================

// updateProgress applies fn to the progress struct under the write lock.
func (h *MigrationHandle) updateProgress(fn func(*MigrationProgress)) { h.Update(fn) }

// setFailed transitions to StatusFailed and records the error.
func (h *MigrationHandle) setFailed(err error) { h.Fail(err) }

// =============================================================================
// countingWriter
// =============================================================================

// Compile-time assertion that countingWriter satisfies io.WriteCloser.
var _ io.WriteCloser = (*countingWriter)(nil)

// countingWriter wraps an io.WriteCloser and calls notify(delta) with the number
// of bytes after each successful Write. Used during dump to track BytesTransferred.
type countingWriter struct {
	w      io.WriteCloser
	notify func(delta int64)
}

// Write implements io.Writer.
func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		cw.notify(int64(n))
	}
	return n, err
}

// Close closes the underlying writer.
func (cw *countingWriter) Close() error {
	return cw.w.Close()
}

// newCountingWriter creates (or truncates) the file at path and returns a
// countingWriter whose notify increments BytesTransferred in the handle's progress.
func (h *MigrationHandle) newCountingWriter(path string) (*countingWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create staging file %q: %w", path, err)
	}
	return &countingWriter{
		w: f,
		notify: func(delta int64) {
			h.updateProgress(func(p *MigrationProgress) {
				p.BytesTransferred += delta
			})
		},
	}, nil
}

// newTableNotify returns a callback that increments TablesDone and updates
// CurrentTable in the handle's progress. Pass it as the notify argument to
// DBDriver.Restore.
func (h *MigrationHandle) newTableNotify() func(string) {
	return func(table string) {
		h.updateProgress(func(p *MigrationProgress) {
			p.TablesDone++
			p.CurrentTable = table
		})
	}
}

// =============================================================================
// run — Async Migration Main Loop
// =============================================================================

// run executes the full migration described by dmm.
// It is launched as a goroutine by MigrateDataAsync; the caller closes h.done.
//
// Happy-path status transitions:
//
//	StatusPending → StatusChecking → StatusDumping → StatusRestoring → StatusDone
//
// On failure with RollbackOnFailure:
//
//	… → StatusRollingBack → StatusRolledBack  (h.err is set)
//
// On failure without rollback:
//
//	… → StatusFailed
func (h *MigrationHandle) run(dmm DBMSMigrationModel) error {
	// Validate before anything else. The staging path never goes through Plan,
	// which is where the synchronous MigrateData picks up model validation, so
	// without this an infeasible model — a column-exclusion filter over an
	// ssh-tunnel, mismatched MongoDB access types — would only surface as a
	// confusing failure mid-dump, after the target had already been touched.
	if err := Validate(dmm); err != nil {
		h.setFailed(err)
		return err
	}
	if dmm.Scope == "" {
		dmm.Scope = ScopeFull
	}

	// Verify the destination is empty before touching any data.
	h.SetStatus(StatusChecking)
	logger.Info("migration status changed", slog.String("status", string(StatusChecking)))

	dstDriver, err := drvpkg.NewDBDriver(dmm.Destination.DBMSType)
	if err != nil {
		h.setFailed(err)
		return err
	}
	if dmm.TargetGrant != nil {
		if err := dstDriver.PrepareTarget(h.Context(), dmm.Destination, dmm.TargetGrant); err != nil {
			h.setFailed(err)
			return err
		}
	}
	if err := dstDriver.CheckTargetEmpty(h.Context(), dmm.Destination); err != nil {
		h.setFailed(err)
		return err
	}
	// Server version, then character-set compatibility — both before any data is
	// read. A target that cannot parse what the dump will carry fails on its
	// first CREATE statement, which on a full migration is hours after the dump
	// began. Version comes first: it is the cheaper question and the more
	// fundamental one.
	if err := enforceVersion(h.Context(), dmm); err != nil {
		h.setFailed(err)
		return err
	}
	if err := enforceCharset(h.Context(), dmm); err != nil {
		h.setFailed(err)
		return err
	}

	// Migrate via direct-pipe or two-step staging.
	if err := h.migrate(dmm, dstDriver); err != nil {
		if dmm.RollbackOnFailure {
			h.SetStatus(StatusRollingBack)
			logger.Info("migration status changed", slog.String("status", string(StatusRollingBack)))
			_ = rollbackDrop(h.Context(), dmm.Destination, dstDriver)
			h.SetStatus(StatusRolledBack)
			h.updateProgress(func(p *MigrationProgress) { p.ErrMessage = err.Error() })
			logger.Info("migration status changed", slog.String("status", string(StatusRolledBack)))
			h.SetErr(err)
		} else {
			h.setFailed(err)
			logger.Error("migration failed", slog.String("err", err.Error()))
		}
		return err
	}

	h.SetStatus(StatusDone)
	logger.Info("migration status changed", slog.String("status", string(StatusDone)))
	return nil
}

// migrate dispatches to the direct-pipe or staging-file strategy.
func (h *MigrationHandle) migrate(dmm DBMSMigrationModel, dstDriver drvpkg.DBDriver) error {
	if dmm.Source.IsSSHTunnel() && dmm.Destination.IsSSHTunnel() {
		return h.migrateViaPipe(dmm)
	}
	return h.migrateViaStaging(dmm, dstDriver)
}

// migrateViaPipe handles ssh-tunnel → ssh-tunnel using PipeExecutor.
// Dump and restore run concurrently inside pipeline.Execute().
func (h *MigrationHandle) migrateViaPipe(dmm DBMSMigrationModel) error {
	if err := h.requireTargetDatabase(dmm.Destination); err != nil {
		return err
	}
	pipeline, err := planDirectPipe(dmm)
	if err != nil {
		return err
	}
	h.SetStatus(StatusDumping)
	logger.Info("migration status changed", slog.String("status", string(StatusDumping)), slog.String("mode", "pipe"))

	// The staging path counts through newCountingWriter; this one has no staging
	// file to wrap, so PipeExecutor reports what crosses its pipe instead. Without
	// it BytesTransferred stayed at zero for the whole migration.
	//
	// The total replaces rather than adds: the pipe reports its own running total,
	// and this is a single-step pipeline, so there is no earlier step to carry.
	ctx := core.WithReporter(h.Context(), core.ReporterFunc(func(u core.Update) {
		if u.Bytes < 0 {
			return
		}
		h.updateProgress(func(p *MigrationProgress) { p.BytesTransferred = u.Bytes })
	}))
	return pipeline.Execute(ctx)
}

// requireTargetDatabase returns *TargetDatabaseNotFoundError when the target
// database does not exist on the destination server.
//
// dbmsx never creates the target database: naming, character set and privileges
// stay under the operator's control, so a missing target is an error rather than
// something to provision silently. PrepareTarget enforces this for every other
// path; the direct-pipe path has no PrepareTarget step when TargetGrant is nil,
// which is why the check is repeated here. Without it, the restore CLI would
// fail with an opaque exit status instead of naming the missing database.
//
// MongoDB is exempt: it creates databases lazily, so "absent" and "empty" are
// indistinguishable and only emptiness can be enforced (CheckTargetEmpty does).
func (h *MigrationHandle) requireTargetDatabase(dst DBMSLocation) error {
	if dst.Database == "" || !dst.IsSSHTunnel() {
		return nil
	}
	tunnel := dst.SSHTunnel
	dbHost := tunnel.DBHost
	if dbHost == "" {
		dbHost = "127.0.0.1"
	}
	dbPort := tunnel.DBPort
	if dbPort == 0 {
		dbPort = 3306
	}
	switch dst.DBMSType {
	case DBMSTypeMariaDB, DBMSTypeMySQL:
		bin := "mariadb"
		if dst.DBMSType == DBMSTypeMySQL {
			bin = "mysql"
		}
		cmd := fmt.Sprintf("%s --batch --silent -h %s -P %d", bin, dbHost, dbPort)
		if tunnel.Username != "" {
			cmd += " -u " + tunnel.Username
		}
		if tunnel.Password != "" {
			cmd += " -p" + tunnel.Password
		}
		dbLiteral := strings.ReplaceAll(dst.Database, `\`, `\\`)
		dbLiteral = strings.ReplaceAll(dbLiteral, "'", `\'`)
		cmd += " -e " + shellQuoteSingle(fmt.Sprintf("SHOW DATABASES LIKE '%s'", dbLiteral))

		var out strings.Builder
		if err := drvpkg.ExecuteViaSSH(tunnel.SSH, cmd, nil, &out); err != nil {
			return fmt.Errorf("check target database exists: %w", err)
		}
		if strings.TrimSpace(out.String()) == "" {
			return &drvpkg.TargetDatabaseNotFoundError{DBMSType: dst.DBMSType, Database: dst.Database}
		}
		return nil

	case DBMSTypePostgreSQL:
		// Queried through the "postgres" maintenance database, which always
		// exists — dst.Database is exactly what may not.
		pgPort := dbPort
		if tunnel.DBPort == 0 {
			pgPort = 5432
		}
		psqlCmd := fmt.Sprintf("psql -h %s -p %d", dbHost, pgPort)
		if tunnel.Username != "" {
			psqlCmd += " -U " + tunnel.Username
		}
		dbLiteral := strings.ReplaceAll(dst.Database, "'", "''")
		psqlCmd += fmt.Sprintf(" -d postgres --no-password -tAc %s",
			shellQuoteSingle(fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname='%s'", dbLiteral)))
		if tunnel.Password != "" {
			psqlCmd = "PGPASSWORD=" + shellQuoteSingle(tunnel.Password) + " " + psqlCmd
		}

		var out strings.Builder
		if err := drvpkg.ExecuteViaSSH(tunnel.SSH, psqlCmd, nil, &out); err != nil {
			return fmt.Errorf("check target database exists: %w", err)
		}
		if strings.TrimSpace(out.String()) != "1" {
			return &drvpkg.TargetDatabaseNotFoundError{DBMSType: dst.DBMSType, Database: dst.Database}
		}
		return nil

	default:
		return nil
	}
}

// shellQuoteSingle wraps s in single quotes for safe use in a remote shell command,
// escaping any embedded single quotes.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// migrateViaStaging handles all non-pipe paths:
//  1. dump the source to a local staging file (byte-progress via countingWriter)
//  2. restore from the staging file to the destination (table-progress via tableNotify)
func (h *MigrationHandle) migrateViaStaging(dmm DBMSMigrationModel, dstDriver drvpkg.DBDriver) error {
	scope := dmm.Scope

	// Dump step.
	h.SetStatus(StatusDumping)
	logger.Info("migration status changed", slog.String("status", string(StatusDumping)), slog.String("mode", "staging"))

	srcDriver, err := drvpkg.NewDBDriver(dmm.Source.DBMSType)
	if err != nil {
		return err
	}
	// A file of this migration's own, not a path derived from the database name —
	// see executor.NewStagingFile. Two migrations of the same database at the same
	// scope used to share one file and overwrite each other's dump.
	outputPath, cleanup, err := execpkg.NewStagingFile(dmm.Source.DBMSType, dmm.Source.Database, scope)
	if err != nil {
		return err
	}

	// The dump is an intermediate, not an artefact. Once the restore has read it
	// there is nothing left that wants it, and what it holds is the source
	// database in full — every row, in plain text, on local disk.
	//
	// Removed on the way out of every path, a failed dump and an aborted restore
	// included: a partial dump is the same data with less of it.
	defer cleanup()

	cw, err := h.newCountingWriter(outputPath)
	if err != nil {
		return err
	}
	if err := srcDriver.Dump(h.Context(), dmm.Source, scope, outputPath, cw, dumpOption(dmm)); err != nil {
		cw.Close() //nolint:errcheck
		return &MigrationError{Stage: StageDump, Err: err}
	}
	if err := cw.Close(); err != nil {
		return fmt.Errorf("close staging file: %w", err)
	}

	// Restore step.
	h.SetStatus(StatusRestoring)
	logger.Info("migration status changed", slog.String("status", string(StatusRestoring)))

	if err := dstDriver.Restore(h.Context(), dmm.Destination, outputPath, h.newTableNotify()); err != nil {
		return &MigrationError{Stage: StageRestore, Err: err}
	}
	return nil
}

// =============================================================================
// rollbackDrop
// =============================================================================

// rollbackDrop reverses a partial restore after migration failure by dropping
// every object the migration created in loc — not just tables but also views,
// routines, triggers, events and (PostgreSQL) materialized views, sequences,
// types, extensions and rules. Because CheckTargetEmpty guaranteed the target
// was empty before the migration, everything present now was created by it.
//
// It is best-effort: it drops what it can and the caller ignores the returned
// error. MongoDB drops all collections directly; SQL engines enumerate objects
// via Inspect and run a generated DROP script.
func rollbackDrop(ctx context.Context, loc DBMSLocation, driver drvpkg.DBDriver) error {
	if loc.DBMSType == DBMSTypeMongoDB {
		// mongorestore's archive format has no per-collection SQL DROP
		// equivalent to generate below, so drop every collection directly
		// instead of building a DROP-statements file. The database itself is
		// left intact.
		return driver.DropAllObjects(ctx, loc)
	}

	// Enumerate every schema object so the teardown script covers non-table
	// objects too (views, routines, triggers, events, etc.).
	inspectLoc := loc
	inspectLoc.Metric = fullSchemaMetric(loc.DBMSType)
	info, err := driver.Inspect(ctx, inspectLoc)
	if err != nil {
		return fmt.Errorf("rollbackDrop: inspect destination: %w", err)
	}

	script := buildTeardownScript(loc.DBMSType, info)
	if script == "" {
		return nil
	}

	tmp, err := os.CreateTemp("", "dbmsx-rollback-*.sql")
	if err != nil {
		return fmt.Errorf("rollbackDrop: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(script); err != nil {
		tmp.Close()
		return fmt.Errorf("rollbackDrop: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return driver.Restore(ctx, loc, tmpPath, nil)
}

// fullSchemaMetric returns a MetricOption that enumerates every droppable schema
// object for dbmsType (no per-table columns/indexes/exact counts — not needed
// for teardown). Returns nil for MongoDB (which drops collections directly).
func fullSchemaMetric(dbmsType string) *MetricOption {
	t := func() *bool { b := true; return &b }
	switch dbmsType {
	case DBMSTypeMySQL:
		return &MetricOption{MySQL: &MySQLMetric{
			Views: t(), Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case DBMSTypeMariaDB:
		return &MetricOption{MariaDB: &MariaDBMetric{
			Views: t(), Functions: t(), Procedures: t(), Triggers: t(), Events: t(),
		}}
	case DBMSTypePostgreSQL:
		return &MetricOption{PostgreSQL: &PostgreSQLMetric{
			Views: t(), MaterializedViews: t(), Functions: t(), Procedures: t(),
			Sequences: t(), Types: t(), Extensions: t(),
		}}
	default:
		return nil
	}
}

// buildTeardownScript builds the DROP script that removes every object in info.
// Returns "" when there is nothing to drop.
//
// MySQL/MariaDB: FOREIGN_KEY_CHECKS is disabled so table order is irrelevant;
// triggers are dropped with their tables but listed explicitly for safety.
// PostgreSQL: DROP ... CASCADE removes dependent triggers/rules/FKs/owned
// sequences with their tables. Object types are ordered by drop risk (tables
// first, name-only routine drops last) so a failure late in the script still
// leaves the earlier, more important drops applied.
func buildTeardownScript(dbmsType string, info *drvpkg.DBMSInfo) string {
	var sb strings.Builder

	quote := func(name string) string {
		switch dbmsType {
		case DBMSTypePostgreSQL:
			return `"` + name + `"`
		default:
			return "`" + name + "`"
		}
	}
	drop := func(kind string, names []string, suffix string) {
		for _, n := range names {
			sb.WriteString("DROP ")
			sb.WriteString(kind)
			sb.WriteString(" IF EXISTS ")
			sb.WriteString(quote(n))
			sb.WriteString(suffix)
			sb.WriteString(";\n")
		}
	}
	// dropRaw emits a drop without identifier-quoting the target, for names that
	// already carry a fully-formed, pre-quoted form (e.g. PostgreSQL routine
	// signatures like `"user_count"(integer, text)`).
	dropRaw := func(kind string, names []string, suffix string) {
		for _, n := range names {
			sb.WriteString("DROP ")
			sb.WriteString(kind)
			sb.WriteString(" IF EXISTS ")
			sb.WriteString(n)
			sb.WriteString(suffix)
			sb.WriteString(";\n")
		}
	}

	switch dbmsType {
	case DBMSTypeMySQL, DBMSTypeMariaDB:
		sb.WriteString("SET FOREIGN_KEY_CHECKS=0;\n")
		drop("TRIGGER", info.Triggers, "")
		drop("VIEW", info.Views, "")
		drop("TABLE", tableNames(info), "")
		drop("PROCEDURE", info.Procedures, "")
		drop("FUNCTION", info.Functions, "")
		drop("EVENT", info.Events, "")
		sb.WriteString("SET FOREIGN_KEY_CHECKS=1;\n")

	case DBMSTypePostgreSQL:
		// Every PostgreSQL inventory entry already arrives schema-qualified and
		// quoted by the server (see the pgObjectQuery* contract), so all of these
		// are emitted raw: quoting them again would turn sales.v_monthly into the
		// single identifier "sales.v_monthly", which names nothing. Functions and
		// procedures additionally carry their identity signature (e.g.
		// sales.user_count(integer, text)) so overloaded routines drop
		// unambiguously, and come last as name-driven drops are the riskiest.
		dropRaw("TABLE", qualifiedTableNames(info), " CASCADE")
		dropRaw("MATERIALIZED VIEW", info.MaterializedViews, " CASCADE")
		dropRaw("VIEW", info.Views, " CASCADE")
		dropRaw("SEQUENCE", info.Sequences, " CASCADE")
		dropRaw("TYPE", info.Types, " CASCADE")
		dropRaw("EXTENSION", info.Extensions, " CASCADE")
		dropRaw("FUNCTION", info.Functions, " CASCADE")
		dropRaw("PROCEDURE", info.Procedures, " CASCADE")
		// A migration into more than one schema creates the schemas as well, and
		// CheckTargetEmpty guaranteed they held nothing beforehand, so they go
		// too. "public" is the exception: every database ships with it, dropping
		// it would leave the target unusable, and the statements above have
		// already emptied it.
		for _, s := range info.PgSchema {
			if s.Name == "public" {
				continue
			}
			sb.WriteString("DROP SCHEMA IF EXISTS ")
			sb.WriteString(pgQuoteIdent(s.Name))
			sb.WriteString(" CASCADE;\n")
		}

	default:
		drop("TABLE", tableNames(info), "")
	}

	// A script containing only the FK-check toggles has nothing to drop.
	if s := strings.TrimSpace(sb.String()); s == "" ||
		s == "SET FOREIGN_KEY_CHECKS=0;\nSET FOREIGN_KEY_CHECKS=1;" {
		return ""
	}
	return sb.String()
}

// tableNames extracts the table names from info.Tables.
func tableNames(info *drvpkg.DBMSInfo) []string {
	names := make([]string, 0, len(info.Tables))
	for _, t := range info.Tables {
		names = append(names, t.Name)
	}
	return names
}

// qualifiedTableNames renders info.Tables the way the PostgreSQL inventories
// arrive: quoted, and prefixed with the owning schema where there is one. It is
// the table-side counterpart of the pgObjectQuery* contract, so buildTeardownScript
// can emit every PostgreSQL drop target raw.
//
// Both quoting rules matter. A table in a database with several schemas must say
// which one it belongs to, or the DROP resolves through search_path and hits the
// wrong table; a table from a single-schema inspection carries no schema and is
// dropped by bare name, which is what the restore created it as.
func qualifiedTableNames(info *drvpkg.DBMSInfo) []string {
	names := make([]string, 0, len(info.Tables))
	for _, t := range info.Tables {
		if t.PgSchema == "" {
			names = append(names, pgQuoteIdent(t.Name))
			continue
		}
		names = append(names, pgQuoteIdent(t.PgSchema)+"."+pgQuoteIdent(t.Name))
	}
	return names
}

// pgQuoteIdent wraps an identifier in double quotes, doubling any it contains.
// The driver has its own copy for the SQL it builds; this one serves the
// teardown script, which is assembled here rather than in the driver.
func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
