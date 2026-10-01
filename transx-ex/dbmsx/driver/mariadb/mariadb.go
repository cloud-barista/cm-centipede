package mariadb

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
)

// =============================================================================
// MariaDBDriver
// =============================================================================

// MariaDBDriver implements DBDriver for MariaDB databases.
// Direct mode uses go-sql-driver/mysql (MariaDB is MySQL-protocol compatible).
// SSH-tunnel mode uses the mariadb-dump and mariadb CLI tools.
type MariaDBDriver struct{}

// NewMariaDBDriver returns a new MariaDBDriver.
func NewMariaDBDriver() *MariaDBDriver { return &MariaDBDriver{} }

var _ base.DBDriver = (*MariaDBDriver)(nil)

// =============================================================================
// Dump
// =============================================================================

// Dump exports the MariaDB database at loc to outputPath according to scope.
// If scope is empty it defaults to ScopeFull.
// w, when non-nil, receives the bytes written for progress tracking.
// The dump option is ignored: its only field renames PostgreSQL schemas, and
// MariaDB spells a schema a database — renaming one is choosing a target database.
func (d *MariaDBDriver) Dump(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer, _ *base.DumpOption) error {
	if scope == "" {
		scope = base.ScopeFull
	}
	logger.Info("mariadb dump started", "database", loc.Database, "scope", scope, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.dumpViaSQL(ctx, loc, scope, outputPath, w)
	}
	return d.dumpViaSSH(ctx, loc, scope, outputPath, w)
}

// mariadbDumpCharset is the character set the SQL path reads and writes under.
// It is utf8mb4 because that is the widest MariaDB offers, so no source value
// has to be narrowed on the way out; the direct connection states it in the DSN
// and the SSH commands are told explicitly.
const mariadbDumpCharset = "utf8mb4"

// mariadbDumpHeader opens every dump the SQL path writes. splitMariaDBStatements
// splits on ";" at end of line, so the statement stands alone as its own entry
// and the restore path replays it first.
const mariadbDumpHeader = "SET NAMES " + mariadbDumpCharset + ";\n" +
	// The dump writes tables in catalog order, so a child row can be inserted
	// before the parent it references. The SQL restore path disables the checks
	// on its own connection, but a dump is also replayed through the CLI (an
	// ssh-tunnel destination reading a staged dump), and that consumer sets
	// nothing of its own. Carrying the guard in the file keeps the dump
	// portable between the two, which is what mysqldump does with its own
	// /*!40014 …*/ guard.
	"SET FOREIGN_KEY_CHECKS=0;\n\n"

func (d *MariaDBDriver) dumpViaSQL(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer) error {
	db, err := d.openDB(loc)
	if err != nil {
		return err
	}
	defer db.Close()

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}

	// Every dump states the encoding its literals are written in, as
	// mariadb-dump does. The restore path opens its own utf8mb4 connection and
	// does not need telling, but a dump file is also something an operator feeds
	// to the mariadb CLI by hand, and there the client's default decides how the
	// bytes are read.
	if _, err := io.WriteString(dst, mariadbDumpHeader); err != nil {
		return fmt.Errorf("write dump header: %w", err)
	}

	if scope == base.ScopeSchemaOnly {
		if err := d.dumpSchemaViaSQL(ctx, db, loc.Database, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump schema: %w", err)
		}
	} else if scope == base.ScopeDataOnly {
		if err := d.dumpDataViaSQL(ctx, db, loc.Database, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump data: %w", err)
		}
	} else {
		// ScopeFull: core schema → data → triggers+events
		// Triggers are written after data to prevent re-firing during restore.
		if err := d.dumpCoreSchemaViaSQL(ctx, db, loc.Database, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump core schema: %w", err)
		}
		if err := d.dumpDataViaSQL(ctx, db, loc.Database, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump data: %w", err)
		}
		if err := d.dumpTriggersAndEventsViaSQL(ctx, db, loc.Database, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump triggers and events: %w", err)
		}
	}
	return nil
}

// dumpSchemaViaSQL writes full schema DDL: tables, views, functions, procedures, triggers, events.
// Extraction order: Tables(FK stripped) → FK Constraints → Views → Functions →
// Procedures → Triggers → Events.
func (d *MariaDBDriver) dumpSchemaViaSQL(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	if err := d.dumpCoreSchemaViaSQL(ctx, db, database, filter, dst); err != nil {
		return err
	}
	return d.dumpTriggersAndEventsViaSQL(ctx, db, database, filter, dst)
}

// dumpCoreSchemaViaSQL writes DDL for tables, views, functions, and procedures.
// Triggers and events are intentionally excluded so that they can be written after
// data in a full dump, preventing trigger re-firing during restore.
func (d *MariaDBDriver) dumpCoreSchemaViaSQL(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	// --- Tables ---
	tables, err := d.listTables(ctx, db, database, filter)
	if err != nil {
		return base.Obj{Kind: base.ObjectKindTable, Owner: database}.Fail(base.ActionList, err)
	}

	// SHOW CREATE TABLE omits COLLATE= when the table's collation is the default
	// for its character set, and that default is a property of the server, not
	// of the dump. A table restored onto a server with a different default would
	// therefore land with a different collation and no error, so the catalog
	// value is read up front and spliced back in below.
	collations, err := d.tableCollations(ctx, db, database)
	if err != nil {
		return err
	}

	var fkAlters []string
	for i, table := range tables {
		obj := base.Obj{
			Kind: base.ObjectKindTable, Owner: database, Name: table,
			Index: i + 1, Total: len(tables),
		}
		createSQL, err := d.showCreateObject(ctx, db,
			fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`", database, table), 1)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		createSQL = mariadbEnsureTableCollation(createSQL, collations[table])
		createSQL = mariadbDropColumns(createSQL, filter.ExcludedColumns(table))
		createSQL = mariadbDropIndexes(createSQL, table, filter)
		clean, alters := mariadbExtractFKs(table, createSQL)
		for _, alter := range alters {
			if n := mariadbConstraintName(alter); n != "" && filter.IsObjectExcluded(filterpkg.ObjectKindForeignKey, n) {
				continue
			}
			// A FK whose parent side was removed by the filter can never be
			// created on the target; emitting it would abort the restore.
			if refTable, reason := mariadbFKRefDropped(alter, filter); reason != "" {
				logger.Warn("mariadb skip foreign key with filtered parent",
					"table", table, "constraint", mariadbConstraintName(alter),
					"referencedTable", refTable, "reason", reason)
				continue
			}
			fkAlters = append(fkAlters, alter)
		}
		fmt.Fprintf(dst, "%s;\n\n", clean)
	}

	// --- Foreign Key Constraints ---
	for _, alter := range fkAlters {
		fmt.Fprintf(dst, "%s;\n", alter)
	}
	if len(fkAlters) > 0 {
		fmt.Fprintln(dst)
	}

	// --- Views ---
	views, err := d.listViews(ctx, db, database, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindView, Owner: database}.Fail(base.ActionList, err)
	}
	views = filter.FilterObjects(filterpkg.ObjectKindView, views)
	for i, view := range views {
		obj := base.Obj{
			Kind: filterpkg.ObjectKindView, Owner: database, Name: view,
			Index: i + 1, Total: len(views),
		}
		createSQL, collation, err := d.showCreateObjectWithCollation(ctx, db,
			fmt.Sprintf("SHOW CREATE VIEW `%s`.`%s`", database, view), 1)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		fmt.Fprint(dst, mariadbWithCollation(fmt.Sprintf("%s;\n", createSQL), collation), "\n")
	}

	// --- Functions ---
	funcs, err := d.listRoutines(ctx, db, database, "FUNCTION")
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindFunction, Owner: database}.Fail(base.ActionList, err)
	}
	funcs = filter.FilterObjects(filterpkg.ObjectKindFunction, funcs)
	for i, fn := range funcs {
		obj := base.Obj{
			Kind: filterpkg.ObjectKindFunction, Owner: database, Name: fn,
			Index: i + 1, Total: len(funcs),
		}
		createSQL, collation, err := d.showCreateObjectWithCollation(ctx, db,
			fmt.Sprintf("SHOW CREATE FUNCTION `%s`.`%s`", database, fn), 2)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		// The SET statements stay outside the DELIMITER block: inside it they
		// would need the block's terminator instead of ";".
		fmt.Fprint(dst, mariadbWithCollation(
			fmt.Sprintf("DELIMITER $$\n%s$$\nDELIMITER ;\n", createSQL), collation), "\n")
	}

	// --- Procedures ---
	procs, err := d.listRoutines(ctx, db, database, "PROCEDURE")
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindProcedure, Owner: database}.Fail(base.ActionList, err)
	}
	procs = filter.FilterObjects(filterpkg.ObjectKindProcedure, procs)
	for i, proc := range procs {
		obj := base.Obj{
			Kind: filterpkg.ObjectKindProcedure, Owner: database, Name: proc,
			Index: i + 1, Total: len(procs),
		}
		createSQL, collation, err := d.showCreateObjectWithCollation(ctx, db,
			fmt.Sprintf("SHOW CREATE PROCEDURE `%s`.`%s`", database, proc), 2)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		fmt.Fprint(dst, mariadbWithCollation(
			fmt.Sprintf("DELIMITER $$\n%s$$\nDELIMITER ;\n", createSQL), collation), "\n")
	}

	return nil
}

// dumpTriggersAndEventsViaSQL writes CREATE TRIGGER and CREATE EVENT DDL.
func (d *MariaDBDriver) dumpTriggersAndEventsViaSQL(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	// --- Triggers ---
	triggers, err := d.listTriggers(ctx, db, database, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindTrigger, Owner: database}.Fail(base.ActionList, err)
	}
	triggers = filter.FilterObjects(filterpkg.ObjectKindTrigger, triggers)
	for i, trigger := range triggers {
		obj := base.Obj{
			Kind: filterpkg.ObjectKindTrigger, Owner: database, Name: trigger,
			Index: i + 1, Total: len(triggers),
		}
		createSQL, collation, err := d.showCreateObjectWithCollation(ctx, db,
			fmt.Sprintf("SHOW CREATE TRIGGER `%s`.`%s`", database, trigger), 2)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		fmt.Fprint(dst, mariadbWithCollation(
			fmt.Sprintf("DELIMITER $$\n%s$$\nDELIMITER ;\n", createSQL), collation), "\n")
	}

	// --- Events ---
	events, err := d.listEvents(ctx, db, database)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindEvent, Owner: database}.Fail(base.ActionList, err)
	}
	events = filter.FilterObjects(filterpkg.ObjectKindEvent, events)
	for i, event := range events {
		obj := base.Obj{
			Kind: filterpkg.ObjectKindEvent, Owner: database, Name: event,
			Index: i + 1, Total: len(events),
		}
		createSQL, collation, err := d.showCreateObjectWithCollation(ctx, db,
			fmt.Sprintf("SHOW CREATE EVENT `%s`.`%s`", database, event), 3)
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		// Same DELIMITER block the triggers above need: an event whose body is a
		// BEGIN…END compound statement carries its own semicolons, and without
		// the block the restore splits it at the first one and executes a
		// truncated CREATE EVENT.
		fmt.Fprint(dst, mariadbWithCollation(
			fmt.Sprintf("DELIMITER $$\n%s$$\nDELIMITER ;\n", createSQL), collation), "\n")
	}

	return nil
}

// dumpDataViaSQL writes INSERT statements for all rows in each table.
func (d *MariaDBDriver) dumpDataViaSQL(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	tables, err := d.listTables(ctx, db, database, filter)
	if err != nil {
		return base.Obj{Kind: base.ObjectKindTable, Owner: database}.Fail(base.ActionList, err)
	}

	for i, table := range tables {
		obj := base.Obj{
			Kind: base.ObjectKindTable, Owner: database, Name: table,
			Index: i + 1, Total: len(tables),
		}
		query, err := d.buildSelectQuery(ctx, db, database, table, filter)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return obj.Fail(base.ActionReadRows, err)
		}

		// When columns are excluded the INSERT must name the kept columns so the
		// value tuples align on restore; otherwise the compact form is used.
		var colClause string
		if filter.HasColumnExclusion(table) {
			quoted := make([]string, len(cols))
			for i, c := range cols {
				quoted[i] = "`" + c + "`"
			}
			colClause = " (" + strings.Join(quoted, ", ") + ")"
		}

		// A row that cannot be read is reported by its position in the table
		// rather than the table's position in the dump: with thousands of rows
		// alike, which one stopped the scan is the part that is hard to find.
		rowNum := 0
		rowObj := base.Obj{Kind: base.ObjectKindTable, Owner: database, Name: table, Unit: "row"}
		for rows.Next() {
			rowNum++
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				rowObj.Index = rowNum
				return rowObj.Fail(base.ActionReadRows, err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = mariadbFormatValue(v)
			}
			fmt.Fprintf(dst, "INSERT INTO `%s`%s VALUES (%s);\n", table, colClause, strings.Join(parts, ", "))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			rowObj.Index = rowNum
			return rowObj.Fail(base.ActionReadRows, err)
		}
		fmt.Fprintln(dst)
	}
	return nil
}

func (d *MariaDBDriver) dumpViaSSH(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer) error {
	cfg := loc.SSHTunnel
	args := d.buildSSHDumpArgs(cfg, loc.Database, scope, loc.Filter)
	cmd := "mariadb-dump " + strings.Join(args, " ")

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}
	// mysqldump has no option to omit DEFINER, so the stream is filtered on the
	// way to disk rather than the command being asked to leave it out.
	filtered := base.NewDefinerFilterWriter(dst)
	defer filtered.Close()

	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, filtered); err != nil {
		return &base.OperationError{
			Operation:   base.OperationDump,
			DBMSType:    base.DBMSTypeMariaDB,
			Source:      loc.Database,
			Destination: outputPath,
			Command:     cmd,
			Output:      base.SSHStderr(err),
			Err:         err,
		}
	}
	return nil
}

// =============================================================================
// Restore
// =============================================================================

// Restore imports the SQL file at inputPath into the database at loc.
func (d *MariaDBDriver) Restore(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	logger.Info("mariadb restore started", "database", loc.Database, "inputPath", inputPath, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.restoreViaSQL(ctx, loc, inputPath, notify)
	}
	return d.restoreViaSSH(ctx, loc, inputPath, notify)
}

func (d *MariaDBDriver) restoreViaSQL(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	db, err := d.openDB(loc)
	if err != nil {
		return err
	}
	defer db.Close()

	content, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read dump file: %w", err)
	}

	// FOREIGN_KEY_CHECKS is a session variable, so every statement of the restore
	// must run on the same connection. Pin one out of the pool instead of using
	// *sql.DB, which is free to hand out a different (FK-checking) connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("disable FK checks: %w", err)
	}

	stmts := splitMariaDBStatements(string(content))
	var lastNotifyTable string
	for i, stmt := range stmts {
		// The splitter cuts on the delimiter, so a dump's comment block arrives
		// attached to the statement below it. Drop the comments, keep the
		// statement — skipping the whole entry loses real SQL (see
		// base.StripLeadingComments).
		stmt = base.StripLeadingComments(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return base.RestoreStatementError(
				base.DBMSTypeMariaDB, loc.Database, stmt, i+1, len(stmts), err)
		}
		if notify != nil {
			if tbl := extractMariaDBInsertTable(stmt); tbl != "" && tbl != lastNotifyTable {
				lastNotifyTable = tbl
				notify(tbl)
			} else if tbl := extractMariaDBCreateTable(stmt); tbl != "" {
				notify(tbl)
			}
		}
	}

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
		return fmt.Errorf("re-enable FK checks: %w", err)
	}
	return nil
}

func (d *MariaDBDriver) restoreViaSSH(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input file: %w", err)
	}
	defer f.Close()

	cmd := d.BuildRestoreCmd(loc)
	if err := base.ExecuteViaSSH(loc.SSHTunnel.SSH, cmd, f, nil); err != nil {
		return &base.OperationError{
			Operation:   base.OperationRestore,
			DBMSType:    base.DBMSTypeMariaDB,
			Destination: loc.Database,
			Command:     cmd,
			Output:      base.SSHStderr(err),
			Err:         err,
		}
	}
	return nil
}

// =============================================================================
// TestConnection
// =============================================================================

// TestConnection verifies the database is reachable and accepts authentication.
func (d *MariaDBDriver) TestConnection(ctx context.Context, loc base.DBMSLocation) error {
	if loc.IsDirect() {
		db, err := d.openDB(loc)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.PingContext(ctx)
	}

	cfg := loc.SSHTunnel
	cmd := d.buildMariaDBCmd(cfg, loc.Database) + ` -e "SELECT 1"`
	var out bytes.Buffer
	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, &out); err != nil {
		return &base.OperationError{
			Operation: base.OperationConnect,
			DBMSType:  base.DBMSTypeMariaDB,
			Source:    loc.Database,
			Command:   cmd,
			Output:    base.SSHStderr(err),
			Err:       err,
		}
	}
	return nil
}

// =============================================================================
// BuildDumpCmd / BuildRestoreCmd
// =============================================================================

// BuildDumpCmd returns the mariadb-dump CLI command used by PipeExecutor (T13)
// to dump on the remote SSH host.
func (d *MariaDBDriver) BuildDumpCmd(loc base.DBMSLocation, scope string) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	if scope == "" {
		scope = base.ScopeFull
	}
	args := d.buildSSHDumpArgs(loc.SSHTunnel, loc.Database, scope, loc.Filter)
	return "mariadb-dump " + strings.Join(args, " ")
}

// BuildRestoreCmd returns the mariadb CLI command used by PipeExecutor (T13)
// to restore on the remote SSH host.
func (d *MariaDBDriver) BuildRestoreCmd(loc base.DBMSLocation) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	return d.buildMariaDBCmd(loc.SSHTunnel, loc.Database)
}

// =============================================================================
// CheckTargetEmpty
// =============================================================================

// CheckTargetEmpty returns *TargetNotEmptyError when the destination already has tables.
func (d *MariaDBDriver) CheckTargetEmpty(ctx context.Context, loc base.DBMSLocation) error {
	logger.Info("mariadb check target empty", "database", loc.Database, "access", loc.AccessType)
	var count int64

	if loc.IsDirect() {
		db, err := d.openDB(loc)
		if err != nil {
			return err
		}
		defer db.Close()
		row := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_type='BASE TABLE'",
			loc.Database)
		if err := row.Scan(&count); err != nil {
			return err
		}
	} else {
		query := fmt.Sprintf(
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='%s' AND table_type='BASE TABLE'",
			mariadbEscape(loc.Database))
		// A single query, so a per-command connection is all this needs.
		res, err := d.mariadbQueryViaSSH(base.NewSSHExec(loc.SSHTunnel.SSH), loc.SSHTunnel, "", query)
		if err != nil {
			return err
		}
		if len(res) > 0 && len(res[0]) > 0 {
			count, _ = strconv.ParseInt(strings.TrimSpace(res[0][0]), 10, 64)
		}
	}

	if count > 0 {
		return &base.TargetNotEmptyError{
			DBMSType:   base.DBMSTypeMariaDB,
			Database:   loc.Database,
			TableCount: int(count),
		}
	}
	return nil
}

// =============================================================================
// ListDatabases
// =============================================================================

// ListDatabases returns the user databases on loc's server. loc.Database is
// ignored: SHOW DATABASES is a server-level query, and it already narrows the
// result to the schemas loc's credentials are granted.
func (d *MariaDBDriver) ListDatabases(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	logger.Info("mariadb list databases", "access", loc.AccessType)

	var names []string

	if loc.IsDirect() {
		// Connect at server level, with no database selected.
		serverLoc := loc
		serverLoc.Database = ""
		db, err := d.openDB(serverLoc)
		if err != nil {
			return nil, err
		}
		defer db.Close()

		rows, err := db.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return nil, fmt.Errorf("list databases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		// A single query, so a per-command connection is all this needs.
		res, err := d.mariadbQueryViaSSH(base.NewSSHExec(loc.SSHTunnel.SSH), loc.SSHTunnel, "", "SHOW DATABASES")
		if err != nil {
			return nil, fmt.Errorf("list databases via SSH: %w", err)
		}
		for _, row := range res {
			if len(row) > 0 {
				names = append(names, row[0])
			}
		}
	}

	return base.UserDatabases(base.DBMSTypeMariaDB, names), nil
}

// =============================================================================
// CreateDatabase / DropDatabase
// =============================================================================

// CreateDatabase creates loc.Database on loc's server. The connection is opened
// at server level with no database selected — the same entry ListDatabases uses
// — because the database being created does not exist yet.
func (d *MariaDBDriver) CreateDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.CreateDatabaseOption) error {
	logger.Info("mariadb create database", "database", loc.Database, "access", loc.AccessType)

	stmt := mariadbBuildCreateDatabase(loc.Database, base.MariaDBCreateOpt(opt))

	if loc.IsDirect() {
		serverLoc := loc
		serverLoc.Database = ""
		db, err := d.openDB(serverLoc)
		if err != nil {
			return err
		}
		defer db.Close()

		exists, err := d.databaseExistsSQL(ctx, db, loc.Database)
		if err != nil {
			return err
		}
		if exists {
			return base.CreateExistsResult(opt, base.DBMSTypeMariaDB, loc.Database)
		}
		if _, err = db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("createDatabase CREATE DATABASE: %w", err)
		}
		return nil
	}

	// The existence check and the DDL are two commands, so they share one
	// connection rather than paying a handshake each.
	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	exists, err := d.databaseExistsSSH(exec, loc.SSHTunnel, loc.Database)
	if err != nil {
		return err
	}
	if exists {
		return base.CreateExistsResult(opt, base.DBMSTypeMariaDB, loc.Database)
	}
	if _, err = d.mariadbQueryViaSSH(exec, loc.SSHTunnel, "", stmt); err != nil {
		return fmt.Errorf("createDatabase CREATE DATABASE via SSH: %w", err)
	}
	return nil
}

// DropDatabase drops loc.Database and everything in it, entering at server level
// as CreateDatabase does.
func (d *MariaDBDriver) DropDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.DropDatabaseOption) error {
	logger.Info("mariadb drop database", "database", loc.Database, "access", loc.AccessType)

	stmt := "DROP DATABASE " + mariadbQuoteIdent(loc.Database)

	if loc.IsDirect() {
		serverLoc := loc
		serverLoc.Database = ""
		db, err := d.openDB(serverLoc)
		if err != nil {
			return err
		}
		defer db.Close()

		exists, err := d.databaseExistsSQL(ctx, db, loc.Database)
		if err != nil {
			return err
		}
		if !exists {
			return base.DropMissingResult(opt, base.DBMSTypeMariaDB, loc.Database)
		}
		// CheckTargetEmpty opens its own connection to loc.Database, as it does
		// for PrepareTarget; the guard is opt-in and runs at most once.
		if base.DropRequiresEmpty(opt) {
			if err = d.CheckTargetEmpty(ctx, loc); err != nil {
				return err
			}
		}
		if _, err = db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("dropDatabase DROP DATABASE: %w", err)
		}
		return nil
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	exists, err := d.databaseExistsSSH(exec, loc.SSHTunnel, loc.Database)
	if err != nil {
		return err
	}
	if !exists {
		return base.DropMissingResult(opt, base.DBMSTypeMariaDB, loc.Database)
	}
	if base.DropRequiresEmpty(opt) {
		if err = d.CheckTargetEmpty(ctx, loc); err != nil {
			return err
		}
	}
	if _, err = d.mariadbQueryViaSSH(exec, loc.SSHTunnel, "", stmt); err != nil {
		return fmt.Errorf("dropDatabase DROP DATABASE via SSH: %w", err)
	}
	return nil
}

// mariadbBuildCreateDatabase renders CREATE DATABASE with the optional character
// set and collation. The name is back-quoted and the two option values were
// already constrained to a safe token shape by base.ValidateCreateOption: DDL
// cannot carry any of them as a bound parameter.
func mariadbBuildCreateDatabase(name string, o base.MariaDBCreateOption) string {
	var b strings.Builder
	b.WriteString("CREATE DATABASE ")
	b.WriteString(mariadbQuoteIdent(name))
	if o.CharacterSet != "" {
		b.WriteString(" CHARACTER SET ")
		b.WriteString(o.CharacterSet)
	}
	if o.Collate != "" {
		b.WriteString(" COLLATE ")
		b.WriteString(o.Collate)
	}
	return b.String()
}

// tableCollations maps every base table in database to its collation. It backs
// mariadbEnsureTableCollation, which needs the value SHOW CREATE TABLE may have
// left out; one query covers the whole dump rather than one per table.
func (d *MariaDBDriver) tableCollations(ctx context.Context, db *sql.DB, database string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT table_name, COALESCE(table_collation,'')
		 FROM information_schema.tables
		 WHERE table_schema=? AND table_type='BASE TABLE'`, database)
	if err != nil {
		return nil, fmt.Errorf("read table collations: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var name, collation string
		if err := rows.Scan(&name, &collation); err != nil {
			return nil, fmt.Errorf("read table collations: %w", err)
		}
		out[name] = collation
	}
	return out, rows.Err()
}

// mariadbDefaultCharsetRe matches the DEFAULT CHARSET table option. Only the
// table option is written that way — a column states its own as
// "CHARACTER SET x" — so the match is unambiguous, and there is exactly one per
// statement.
var mariadbDefaultCharsetRe = regexp.MustCompile(`(?i)DEFAULT CHARSET=[A-Za-z0-9_]+`)

// mariadbEnsureTableCollation splices COLLATE= into a CREATE TABLE statement
// that left it out, so the table lands on the target with the collation it had
// on the source rather than whatever the target's character set defaults to.
//
// The clause goes directly after DEFAULT CHARSET= rather than at the end of the
// options: a partitioned table carries a /*!50100 PARTITION BY ... */ clause
// after them, and appending would land inside it.
//
// A statement that already names a collation is returned untouched. The test is
// for "COLLATE=" with the equals sign, which is the table-option spelling; a
// column-level clause reads "COLLATE x" and does not match.
func mariadbEnsureTableCollation(createSQL, collation string) string {
	if collation == "" || strings.Contains(createSQL, "COLLATE=") {
		return createSQL
	}
	at := mariadbDefaultCharsetRe.FindStringIndex(createSQL)
	if at == nil {
		return createSQL
	}
	return createSQL[:at[1]] + " COLLATE=" + collation + createSQL[at[1]:]
}

// mariadbQuoteIdent wraps name in back quotes, doubling any it contains.
// Database names reach it only after base.ValidateDatabaseName has refused back
// quotes; the doubling keeps the quoting sound for every other identifier.
func mariadbQuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// mariadbDatabaseExistsQuery matches a schema name exactly. SHOW DATABASES LIKE
// would treat "_" and "%" in the name as wildcards, so a database called
// "app_db" would report a sibling "appXdb" as itself.
const mariadbDatabaseExistsQuery = "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = "

// databaseExistsSQL reports whether database exists on the server db is
// connected to. db must be a server-level connection (no database selected).
func (d *MariaDBDriver) databaseExistsSQL(ctx context.Context, db *sql.DB, database string) (bool, error) {
	var name string
	err := db.QueryRowContext(ctx, mariadbDatabaseExistsQuery+"?", database).Scan(&name)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check database exists: %w", err)
	}
	return true, nil
}

// databaseExistsSSH is the SSH-tunnel counterpart of databaseExistsSQL. It needs
// no database selected: information_schema is readable without one.
func (d *MariaDBDriver) databaseExistsSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string) (bool, error) {
	rows, err := d.mariadbQueryViaSSH(exec, cfg, "",
		mariadbDatabaseExistsQuery+"'"+mariadbEscape(database)+"'")
	if err != nil {
		return false, fmt.Errorf("check database exists via SSH: %w", err)
	}
	return len(rows) > 0, nil
}

// =============================================================================
// Inspect
// =============================================================================

// The three queries below are shared by the direct and SSH paths so both report
// identical metadata: the direct path appends a "?" placeholder, the SSH path a
// quoted literal. Keeping the column list in one place is what stops the two
// from drifting — the SSH path reads its columns by position.

// mariadbDatabaseCharsetQuery reads the database's default character set and
// collation. Both are recorded on the schema itself, so no join is needed.
const mariadbDatabaseCharsetQuery = `SELECT COALESCE(DEFAULT_CHARACTER_SET_NAME,''),
	        COALESCE(DEFAULT_COLLATION_NAME,'')
	 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = `

// mariadbTableListQuery reads every base table with its size statistics and text
// defaults. information_schema.TABLES records only the collation, so the
// character set is joined in from COLLATIONS — a small in-memory table — rather
// than split off the collation name, whose charset prefix is a naming
// convention the engine does not guarantee.
const mariadbTableListQuery = `SELECT t.table_name,
	        COALESCE(t.table_rows,0),
	        COALESCE(t.data_length+t.index_length,0),
	        COALESCE(t.data_length,0),
	        COALESCE(t.index_length,0),
	        COALESCE(t.engine,''),
	        COALESCE(t.table_collation,''),
	        COALESCE(c.character_set_name,'')
	 FROM information_schema.tables t
	 LEFT JOIN information_schema.collations c ON c.collation_name = t.table_collation
	 WHERE t.table_schema=`

const mariadbTableListOrder = ` AND t.table_type='BASE TABLE' ORDER BY t.table_name`

// mariadbColumnQuery reads one table's columns. character_set_name and
// collation_name are NULL on every non-string column, so both are coalesced:
// the SSH path would otherwise read back the literal string "NULL".
const mariadbColumnQuery = `SELECT column_name, column_type, is_nullable,
	        COALESCE(column_default,''), column_key, COALESCE(column_comment,''),
	        COALESCE(character_set_name,''), COALESCE(collation_name,'')
	 FROM information_schema.columns
	 WHERE table_schema=`

// Inspect returns server-level and schema-level metadata for the database at loc.
func (d *MariaDBDriver) Inspect(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	if loc.IsDirect() {
		return d.inspectViaSQL(ctx, loc)
	}
	return d.inspectViaSSH(ctx, loc)
}

func (d *MariaDBDriver) inspectViaSQL(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := mariadbMetric(loc)
	db, err := d.openDB(loc)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	info := &base.DBMSInfo{Database: loc.Database}

	if info.ServerVersion, err = d.serverVersionViaSQL(ctx, db); err != nil {
		return nil, err
	}

	// Database defaults. A database that vanished between the version query and
	// this one leaves the two fields empty rather than failing the inspection.
	row := db.QueryRowContext(ctx, mariadbDatabaseCharsetQuery+"?", loc.Database)
	if err := row.Scan(&info.CharacterSet, &info.Collation); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("read database character set: %w", err)
	}

	row = db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(data_length+index_length),0),
		        COALESCE(SUM(data_length),0),
		        COALESCE(SUM(index_length),0),
		        COUNT(*)
		 FROM information_schema.tables
		 WHERE table_schema=? AND table_type='BASE TABLE'`, loc.Database)
	if err := row.Scan(&info.TotalSize, &info.DataSize, &info.IndexSize, &info.TableCount); err != nil {
		return nil, err
	}

	// Table list with statistics-based row estimates (always collected).
	rows, err := db.QueryContext(ctx, mariadbTableListQuery+"?"+mariadbTableListOrder, loc.Database)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t base.TableInfo
		if err := rows.Scan(&t.Name, &t.RowCount, &t.TotalSize, &t.DataSize, &t.IndexSize,
			&t.Engine, &t.Collation, &t.CharacterSet); err != nil {
			rows.Close()
			return nil, err
		}
		info.Tables = append(info.Tables, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Per-table detail (opt-in).
	if m.needPerTableScan() {
		for i := range info.Tables {
			t := &info.Tables[i]
			obj := base.Obj{
				Kind: base.ObjectKindTable, Owner: loc.Database, Name: t.Name,
				Index: i + 1, Total: len(info.Tables),
			}
			if m.rowCountExact {
				// information_schema.tables.table_rows is an InnoDB statistics
				// estimate that can read 0 or stale right after a bulk restore;
				// use an exact COUNT(*) when accuracy is requested.
				if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`", t.Name)).Scan(&t.RowCount); err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
			}
			if m.columns || m.indexes {
				ti, err := d.describeTableViaSQL(ctx, db, loc.Database, t.Name)
				if err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
				if m.columns {
					t.Columns = ti.Columns
				}
				if m.indexes {
					t.Indexes = ti.Indexes
				}
			}
		}
	}

	// Database-wide schema objects (opt-in).
	if m.needSchemaScan() {
		if m.foreignKeys {
			if info.ForeignKeys, err = d.listForeignKeys(ctx, db, loc.Database); err != nil {
				return nil, err
			}
		}
		if m.views {
			if info.Views, err = d.listViews(ctx, db, loc.Database, nil); err != nil {
				return nil, err
			}
		}
		if m.functions {
			if info.Functions, err = d.listRoutines(ctx, db, loc.Database, "FUNCTION"); err != nil {
				return nil, err
			}
		}
		if m.procedures {
			if info.Procedures, err = d.listRoutines(ctx, db, loc.Database, "PROCEDURE"); err != nil {
				return nil, err
			}
		}
		if m.triggers {
			if info.Triggers, err = d.listTriggers(ctx, db, loc.Database, nil); err != nil {
				return nil, err
			}
		}
		if m.events {
			if info.Events, err = d.listEvents(ctx, db, loc.Database); err != nil {
				return nil, err
			}
		}
	}
	return info, nil
}

func (d *MariaDBDriver) inspectViaSSH(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := mariadbMetric(loc)
	cfg := loc.SSHTunnel
	info := &base.DBMSInfo{Database: loc.Database}

	// One connection for the whole inspection: with per-table metrics enabled
	// this issues two queries per table, and dialing each time would make the
	// SSH handshake the dominant cost.
	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()

	if info.ServerVersion, err = d.serverVersionViaSSH(exec, cfg); err != nil {
		return nil, err
	}

	// Database defaults. An empty result leaves the fields empty rather than
	// failing the inspection, matching the direct path.
	res, err := d.mariadbQueryViaSSH(exec, cfg, "",
		mariadbDatabaseCharsetQuery+"'"+mariadbEscape(loc.Database)+"'")
	if err != nil {
		return nil, fmt.Errorf("read database character set via SSH: %w", err)
	}
	if len(res) > 0 && len(res[0]) >= 2 {
		info.CharacterSet = strings.TrimSpace(res[0][0])
		info.Collation = strings.TrimSpace(res[0][1])
	}

	sizeQ := fmt.Sprintf(
		`SELECT COALESCE(SUM(data_length+index_length),0),COALESCE(SUM(data_length),0),COALESCE(SUM(index_length),0),COUNT(*)
		 FROM information_schema.tables
		 WHERE table_schema='%s' AND table_type='BASE TABLE'`, mariadbEscape(loc.Database))
	res, err = d.mariadbQueryViaSSH(exec, cfg, "", sizeQ)
	if err != nil {
		return nil, err
	}
	if len(res) > 0 && len(res[0]) >= 4 {
		info.TotalSize, _ = strconv.ParseInt(strings.TrimSpace(res[0][0]), 10, 64)
		info.DataSize, _ = strconv.ParseInt(strings.TrimSpace(res[0][1]), 10, 64)
		info.IndexSize, _ = strconv.ParseInt(strings.TrimSpace(res[0][2]), 10, 64)
		n, _ := strconv.Atoi(strings.TrimSpace(res[0][3]))
		info.TableCount = n
	}

	tableQ := mariadbTableListQuery + "'" + mariadbEscape(loc.Database) + "'" + mariadbTableListOrder
	res, err = d.mariadbQueryViaSSH(exec, cfg, "", tableQ)
	if err != nil {
		return nil, err
	}
	for _, row := range res {
		if t, ok := mariadbParseTableRow(row); ok {
			info.Tables = append(info.Tables, t)
		}
	}

	// Per-table detail (opt-in).
	if m.needPerTableScan() {
		for i := range info.Tables {
			t := &info.Tables[i]
			obj := base.Obj{
				Kind: base.ObjectKindTable, Owner: loc.Database, Name: t.Name,
				Index: i + 1, Total: len(info.Tables),
			}
			if m.rowCountExact {
				// table_rows is an InnoDB statistics estimate that can read 0 or
				// stale right after a bulk restore; use an exact COUNT(*).
				countQ := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.`%s`", mariadbEscape(loc.Database), t.Name)
				countRes, err := d.mariadbQueryViaSSH(exec, cfg, "", countQ)
				if err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
				if len(countRes) > 0 && len(countRes[0]) > 0 {
					if n, perr := strconv.ParseInt(strings.TrimSpace(countRes[0][0]), 10, 64); perr == nil {
						t.RowCount = n
					}
				}
			}
			if m.columns || m.indexes {
				ti, err := d.describeTableViaSSH(ctx, exec, loc, t.Name)
				if err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
				if m.columns {
					t.Columns = ti.Columns
				}
				if m.indexes {
					t.Indexes = ti.Indexes
				}
			}
		}
	}

	// Database-wide schema objects (opt-in).
	if m.needSchemaScan() {
		db := mariadbEscape(loc.Database)
		if m.foreignKeys {
			info.ForeignKeys, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA='%s' AND CONSTRAINT_TYPE='FOREIGN KEY' ORDER BY CONSTRAINT_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
		if m.views {
			info.Views, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA='%s' ORDER BY TABLE_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
		if m.functions {
			info.Functions, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_TYPE='FUNCTION' AND ROUTINE_SCHEMA='%s' ORDER BY ROUTINE_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
		if m.procedures {
			info.Procedures, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_TYPE='PROCEDURE' AND ROUTINE_SCHEMA='%s' ORDER BY ROUTINE_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
		if m.triggers {
			info.Triggers, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s' ORDER BY TRIGGER_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
		if m.events {
			info.Events, err = d.listNamesViaSSH(exec, cfg, fmt.Sprintf(
				`SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA='%s' ORDER BY EVENT_NAME`, db))
			if err != nil {
				return nil, err
			}
		}
	}
	return info, nil
}

// listForeignKeys returns foreign-key constraint names in the database.
func (d *MariaDBDriver) listForeignKeys(ctx context.Context, db *sql.DB, database string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS
		 WHERE CONSTRAINT_SCHEMA=? AND CONSTRAINT_TYPE='FOREIGN KEY'
		 ORDER BY CONSTRAINT_NAME`, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// mariadbParseTableRow and mariadbParseColumnRow turn one tab-separated row of
// the SSH path's output into the struct the direct path builds by column name.
//
// They exist as free functions so the mapping can be tested without a server.
// This path addresses its columns by position, so it is the one place where
// adding a column to mariadbTableListQuery or mariadbColumnQuery and forgetting
// to widen the guard silently mis-assigns every field after it — a class of bug
// the integration tests, which need a live MariaDB, do not catch.
//
// A row shorter than the query's column list is skipped rather than
// half-populated: a truncated row means the query did not run as written.
func mariadbParseTableRow(row []string) (base.TableInfo, bool) {
	if len(row) < 8 {
		return base.TableInfo{}, false
	}
	t := base.TableInfo{Name: strings.TrimSpace(row[0])}
	t.RowCount, _ = strconv.ParseInt(strings.TrimSpace(row[1]), 10, 64)
	t.TotalSize, _ = strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
	t.DataSize, _ = strconv.ParseInt(strings.TrimSpace(row[3]), 10, 64)
	t.IndexSize, _ = strconv.ParseInt(strings.TrimSpace(row[4]), 10, 64)
	t.Engine = strings.TrimSpace(row[5])
	t.Collation = strings.TrimSpace(row[6])
	t.CharacterSet = strings.TrimSpace(row[7])
	return t, true
}

func mariadbParseColumnRow(row []string) (base.ColumnInfo, bool) {
	if len(row) < 8 {
		return base.ColumnInfo{}, false
	}
	return base.ColumnInfo{
		Name:       strings.TrimSpace(row[0]),
		DataType:   strings.TrimSpace(row[1]),
		IsNullable: strings.EqualFold(strings.TrimSpace(row[2]), "YES"),
		// Default and Comment are kept verbatim: the direct path does not trim
		// them, and both may legitimately begin or end with whitespace.
		Default:      row[3],
		IsPrimary:    strings.TrimSpace(row[4]) == "PRI",
		Comment:      row[5],
		CharacterSet: strings.TrimSpace(row[6]),
		Collation:    strings.TrimSpace(row[7]),
	}, true
}

// listNamesViaSSH runs a single-column query over SSH and returns the trimmed
// values of the first column.
func (d *MariaDBDriver) listNamesViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, query string) ([]string, error) {
	res, err := d.mariadbQueryViaSSH(exec, cfg, "", query)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, row := range res {
		if len(row) > 0 {
			names = append(names, strings.TrimSpace(row[0]))
		}
	}
	return names, nil
}

// =============================================================================
// CharsetProfile
// =============================================================================

// mariadbUsedCharsetQuery lists the character set and collation of every base
// table and of every column that states its own, tagged with the table it
// belongs to so loc.Filter can be applied before the names are collected.
//
// The table rows come first and carry an empty column name; that is what tells
// the two apart. TABLES has no character-set column, so it is joined in from
// COLLATIONS as the inspect query does.
//
// The join misses on MariaDB 11.4+, where a table collated with a contextually
// typed collation reports "utf8mb4_uca1400_ai_ci" while COLLATIONS lists the
// bare "uca1400_ai_ci" (see mariadbExpandCollations). The character set is then
// taken from the collation name's own prefix rather than left empty, which would
// drop the name from the compatibility check without saying so.
const mariadbUsedCharsetQuery = `SELECT t.table_name, '',
	        COALESCE(c.character_set_name, SUBSTRING_INDEX(t.table_collation, '_', 1), ''),
	        COALESCE(t.table_collation,'')
	 FROM information_schema.tables t
	 LEFT JOIN information_schema.collations c ON c.collation_name = t.table_collation
	 WHERE t.table_type='BASE TABLE' AND t.table_schema=`

const mariadbUsedCharsetColumnPart = `
	 UNION ALL
	 SELECT table_name, column_name, COALESCE(character_set_name,''), COALESCE(collation_name,'')
	 FROM information_schema.columns
	 WHERE collation_name IS NOT NULL AND table_schema=`

// mariadbAvailableCollationQuery and mariadbAvailableCharsetQuery enumerate what the
// server recognises. This is what decides whether a restore parses: the dump
// carries these as literal names, and the server either knows one or rejects the
// statement naming it. Both are small in-memory tables.
// COLLATIONS is read with its character set column because MariaDB 11.4+ lists
// the UCA-14.0.0 collations as *contextually typed*: one row named
// "uca1400_ai_ci" with a NULL character set, usable with any Unicode character
// set. mariadbExpandCollations turns those into the qualified names a dump
// actually carries.
const mariadbAvailableCollationQuery = `SELECT COLLATION_NAME, COALESCE(CHARACTER_SET_NAME,'') FROM information_schema.COLLATIONS`
const mariadbAvailableCharsetQuery = `SELECT CHARACTER_SET_NAME FROM information_schema.CHARACTER_SETS`

// =============================================================================
// ServerVersion
// =============================================================================

// ServerVersion returns what SELECT VERSION() reports — on MariaDB a string
// carrying the vendor name, "10.6.14-MariaDB". See base.DBDriver.
func (d *MariaDBDriver) ServerVersion(ctx context.Context, loc base.DBMSLocation) (string, error) {
	if loc.IsDirect() {
		// The database is cleared so the connection lands at server level: the
		// version has to be readable on a target whose database does not exist
		// yet, which is the state a target is in before its first migration.
		verLoc := loc
		verLoc.Database = ""
		db, err := d.openDB(verLoc)
		if err != nil {
			return "", err
		}
		defer db.Close()
		return d.serverVersionViaSQL(ctx, db)
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return "", err
	}
	defer exec.Close()
	return d.serverVersionViaSSH(exec, loc.SSHTunnel)
}

// serverVersionViaSQL and serverVersionViaSSH take a connection the caller
// already holds, so an inspection reuses its own rather than dialling twice.

func (d *MariaDBDriver) serverVersionViaSQL(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return "", fmt.Errorf("SELECT VERSION(): %w", err)
	}
	return strings.TrimSpace(version), nil
}

func (d *MariaDBDriver) serverVersionViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig) (string, error) {
	res, err := d.mariadbQueryViaSSH(exec, cfg, "", "SELECT VERSION()")
	if err != nil {
		return "", err
	}
	// An empty result leaves the version empty rather than failing: the caller
	// reads "" as "cannot compare", which is what an unreadable version means.
	if len(res) == 0 || len(res[0]) == 0 {
		return "", nil
	}
	return strings.TrimSpace(res[0][0]), nil
}

// CharsetProfile reports the database's text defaults, the names its objects
// carry, and the names its server recognises.
func (d *MariaDBDriver) CharsetProfile(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	if loc.IsDirect() {
		return d.charsetProfileViaSQL(ctx, loc)
	}
	return d.charsetProfileViaSSH(ctx, loc)
}

func (d *MariaDBDriver) charsetProfileViaSQL(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	db, err := d.openDB(loc)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	p := &base.CharsetProfile{}
	if err := db.QueryRowContext(ctx, mariadbDatabaseCharsetQuery+"?", loc.Database).
		Scan(&p.Encoding, &p.Collation); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("charsetProfile database defaults: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		mariadbUsedCharsetQuery+"?"+mariadbUsedCharsetColumnPart+"?", loc.Database, loc.Database)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile used names: %w", err)
	}
	var used [][]string
	for rows.Next() {
		r := make([]string, 4)
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			rows.Close()
			return nil, fmt.Errorf("charsetProfile used names: %w", err)
		}
		used = append(used, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("charsetProfile used names: %w", err)
	}
	mariadbCollectUsed(p, used, loc.Filter)

	// Character sets first: the collation list is expanded against them.
	if p.AvailableCharsets, err = d.queryNames(ctx, db, mariadbAvailableCharsetQuery); err != nil {
		return nil, fmt.Errorf("charsetProfile available character sets: %w", err)
	}
	collRows, err := d.queryRows(ctx, db, mariadbAvailableCollationQuery)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile available collations: %w", err)
	}
	p.AvailableCollations = base.SortUnique(mariadbExpandCollations(collRows, p.AvailableCharsets))
	return p, nil
}

func (d *MariaDBDriver) charsetProfileViaSSH(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	cfg := loc.SSHTunnel
	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()

	p := &base.CharsetProfile{}
	dbLit := "'" + mariadbEscape(loc.Database) + "'"

	res, err := d.mariadbQueryViaSSH(exec, cfg, "", mariadbDatabaseCharsetQuery+dbLit)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile database defaults via SSH: %w", err)
	}
	if len(res) > 0 && len(res[0]) >= 2 {
		p.Encoding = strings.TrimSpace(res[0][0])
		p.Collation = strings.TrimSpace(res[0][1])
	}

	usedRes, err := d.mariadbQueryViaSSH(exec, cfg, "",
		mariadbUsedCharsetQuery+dbLit+mariadbUsedCharsetColumnPart+dbLit)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile used names via SSH: %w", err)
	}
	mariadbCollectUsed(p, usedRes, loc.Filter)

	if p.AvailableCharsets, err = d.listNamesViaSSH(exec, cfg, mariadbAvailableCharsetQuery); err != nil {
		return nil, fmt.Errorf("charsetProfile available character sets via SSH: %w", err)
	}
	p.AvailableCharsets = base.SortUnique(p.AvailableCharsets)

	collRes, err := d.mariadbQueryViaSSH(exec, cfg, "", mariadbAvailableCollationQuery)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile available collations via SSH: %w", err)
	}
	p.AvailableCollations = base.SortUnique(mariadbExpandCollations(collRes, p.AvailableCharsets))
	return p, nil
}

// mariadbExpandCollations turns the (collation, character set) rows of
// mariadbAvailableCollationQuery into the collation names a dump can name.
//
// MariaDB 11.4 made the UCA-14.0.0 collations *contextually typed*: COLLATIONS
// carries one row per collation with a NULL character set ("uca1400_ai_ci"),
// usable with every Unicode character set. What a table reports, and what the
// dump therefore writes into its DDL, is the qualified form
// ("utf8mb4_uca1400_ai_ci"). Comparing the two lists as they come makes a
// server refuse the collation it uses by default — 11.4 to 11.4 is blocked.
//
// So each contextually typed name is also emitted qualified, once per Unicode
// character set the server lists. Non-Unicode character sets are left out: the
// UCA collations do not apply to them, and inventing "latin1_uca1400_ai_ci"
// would let a genuinely unknown name through the check.
func mariadbExpandCollations(rows [][]string, charsets []string) []string {
	var out []string
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		name := strings.TrimSpace(r[0])
		if name == "" {
			continue
		}
		out = append(out, name)
		if len(r) > 1 && strings.TrimSpace(r[1]) != "" {
			continue // already tied to one character set
		}
		for _, cs := range charsets {
			if mariadbIsUnicodeCharset(cs) {
				out = append(out, cs+"_"+name)
			}
		}
	}
	return out
}

// mariadbIsUnicodeCharset reports whether a character set can carry the
// contextually typed UCA collations. MariaDB's Unicode character sets are
// utf8mb3, utf8mb4, utf16, utf16le, utf32 and ucs2.
func mariadbIsUnicodeCharset(charset string) bool {
	cs := strings.ToLower(strings.TrimSpace(charset))
	return strings.HasPrefix(cs, "utf") || cs == "ucs2"
}

// queryRows runs a query and returns its rows as trimmed strings, keeping the
// column order. It is queryNames' multi-column counterpart.
func (d *MariaDBDriver) queryRows(ctx context.Context, db *sql.DB, query string) ([][]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]string
	for rows.Next() {
		vals := make([]string, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}

// mariadbCollectUsed fills p's used lists from the (table, column, charset,
// collation) rows of mariadbUsedCharsetQuery, dropping what the filter removes
// from the dump.
//
// A name kept only by an object the migration will not carry must not block it,
// which is the whole reason the query tags each row with its table: a filtered
// table takes its collation out of the question, and so does a filtered column.
func mariadbCollectUsed(p *base.CharsetProfile, rows [][]string, filter *filterpkg.DBMSFilterOption) {
	var charsets, collations []string
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		table := strings.TrimSpace(r[0])
		column := strings.TrimSpace(r[1])
		if filter.IsTableExcluded(table) {
			continue
		}
		if column != "" && filter.ExcludedColumns(table)[column] {
			continue
		}
		charsets = append(charsets, strings.TrimSpace(r[2]))
		collations = append(collations, strings.TrimSpace(r[3]))
	}
	p.UsedCharsets = base.SortUnique(charsets)
	p.UsedCollations = base.SortUnique(collations)
}

// queryNames runs a single-column query and returns its values, sorted and
// deduplicated.
func (d *MariaDBDriver) queryNames(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return base.SortUnique(names), rows.Err()
}

// =============================================================================
// DescribeTable
// =============================================================================

// DescribeTable returns column and index metadata for a single table.
func (d *MariaDBDriver) DescribeTable(ctx context.Context, loc base.DBMSLocation, table string) (*base.TableInfo, error) {
	if loc.IsDirect() {
		db, err := d.openDB(loc)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return d.describeTableViaSQL(ctx, db, loc.Database, table)
	}
	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()
	return d.describeTableViaSSH(ctx, exec, loc, table)
}

func (d *MariaDBDriver) describeTableViaSQL(ctx context.Context, db *sql.DB, database, table string) (*base.TableInfo, error) {
	info := &base.TableInfo{Name: table}

	colRows, err := db.QueryContext(ctx,
		mariadbColumnQuery+"? AND table_name=? ORDER BY ordinal_position", database, table)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()

	for colRows.Next() {
		var col base.ColumnInfo
		var nullable, key string
		if err := colRows.Scan(&col.Name, &col.DataType, &nullable, &col.Default, &key, &col.Comment,
			&col.CharacterSet, &col.Collation); err != nil {
			return nil, err
		}
		col.IsNullable = strings.EqualFold(nullable, "YES")
		col.IsPrimary = key == "PRI"
		info.Columns = append(info.Columns, col)
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	// index_type is read from the catalog rather than assumed: FULLTEXT, HASH and
	// SPATIAL indexes all exist in MariaDB and reporting them as BTREE would
	// misdescribe the schema.
	idxRows, err := db.QueryContext(ctx,
		`SELECT index_name, column_name, non_unique, COALESCE(index_type,'')
		 FROM information_schema.statistics
		 WHERE table_schema=? AND table_name=?
		 ORDER BY index_name, seq_in_index`, database, table)
	if err != nil {
		return nil, err
	}
	defer idxRows.Close()

	idxMap := make(map[string]*base.IndexInfo)
	var idxOrder []string
	for idxRows.Next() {
		var idxName, colName, idxType string
		var nonUnique int
		if err := idxRows.Scan(&idxName, &colName, &nonUnique, &idxType); err != nil {
			return nil, err
		}
		if _, ok := idxMap[idxName]; !ok {
			idxMap[idxName] = &base.IndexInfo{Name: idxName, IsUnique: nonUnique == 0, Type: idxType}
			idxOrder = append(idxOrder, idxName)
		}
		idxMap[idxName].Columns = append(idxMap[idxName].Columns, colName)
	}
	if err := idxRows.Err(); err != nil {
		return nil, err
	}
	for _, name := range idxOrder {
		info.Indexes = append(info.Indexes, *idxMap[name])
	}
	return info, nil
}

func (d *MariaDBDriver) describeTableViaSSH(ctx context.Context, exec *base.SSHExec, loc base.DBMSLocation, table string) (*base.TableInfo, error) {
	cfg := loc.SSHTunnel
	info := &base.TableInfo{Name: table}

	colQ := mariadbColumnQuery + "'" + mariadbEscape(loc.Database) +
		"' AND table_name='" + mariadbEscape(table) + "' ORDER BY ordinal_position"

	res, err := d.mariadbQueryViaSSH(exec, cfg, "", colQ)
	if err != nil {
		return nil, err
	}
	for _, row := range res {
		if col, ok := mariadbParseColumnRow(row); ok {
			info.Columns = append(info.Columns, col)
		}
	}

	idxQ := fmt.Sprintf(
		`SELECT index_name,column_name,non_unique,COALESCE(index_type,'')
		 FROM information_schema.statistics
		 WHERE table_schema='%s' AND table_name='%s'
		 ORDER BY index_name,seq_in_index`,
		mariadbEscape(loc.Database), mariadbEscape(table))

	idxRes, err := d.mariadbQueryViaSSH(exec, cfg, "", idxQ)
	if err != nil {
		return nil, err
	}
	idxMap := make(map[string]*base.IndexInfo)
	var idxOrder []string
	for _, row := range idxRes {
		if len(row) < 4 {
			continue
		}
		idxName := strings.TrimSpace(row[0])
		colName := strings.TrimSpace(row[1])
		isUnique := strings.TrimSpace(row[2]) == "0"
		idxType := strings.TrimSpace(row[3])
		if _, ok := idxMap[idxName]; !ok {
			idxMap[idxName] = &base.IndexInfo{Name: idxName, IsUnique: isUnique, Type: idxType}
			idxOrder = append(idxOrder, idxName)
		}
		idxMap[idxName].Columns = append(idxMap[idxName].Columns, colName)
	}
	for _, name := range idxOrder {
		info.Indexes = append(info.Indexes, *idxMap[name])
	}
	return info, nil
}

// =============================================================================
// Internal Helpers — Connection
// =============================================================================

// buildDSN constructs a MariaDB DSN for the given direct-mode location.
func (d *MariaDBDriver) buildDSN(loc base.DBMSLocation) (string, error) {
	cfg := loc.Direct
	port := cfg.Port
	if port == 0 {
		port = 3306
	}
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 30
	}
	tlsParam, err := dsnTLSParam(cfg)
	if err != nil {
		return "", err
	}
	creds := ""
	if cfg.Username != "" {
		creds = cfg.Username
		if cfg.Password != "" {
			creds += ":" + cfg.Password
		}
		creds += "@"
	}
	// charset is stated rather than left to the driver's default so the
	// connection matches what the SSH path is told and what the dump header
	// declares. The default happens to be utf8mb4 today; relying on that would
	// make the three disagree the moment it changes.
	return fmt.Sprintf("%stcp(%s:%d)/%s?timeout=%ds&parseTime=true&tls=%s&charset=%s",
		creds, cfg.Host, port, loc.Database, timeout, tlsParam, mariadbDumpCharset), nil
}

// dsnTLSParam maps a TLS mode onto the driver's tls= parameter.
//
// MariaDB speaks the MySQL wire protocol and is driven by the same Go package,
// so the mapping is the same one mysql.dsnTLSParam makes. It is repeated here
// rather than shared, as the rest of this driver is: the two engines have
// begun to diverge, and a shared helper would leave nowhere to express it.
//
// Three of the five modes are built into go-sql-driver and cost nothing: false,
// preferred and skip-verify line up with disable, prefer and require. A
// registered configuration is needed only where the standard verifier cannot
// do the job on its own — always for verify-ca, which has no built-in
// equivalent, and for verify-full once the CA is outside the system trust
// store.
func dsnTLSParam(cfg *base.DirectConfig) (string, error) {
	s, err := base.ResolveTLS(cfg)
	if err != nil {
		return "", err
	}
	switch s.Mode {
	case base.TLSModeDisable:
		return "false", nil
	case base.TLSModePrefer:
		return "preferred", nil
	case base.TLSModeRequire:
		return "skip-verify", nil
	case base.TLSModeVerifyFull:
		if !s.HasCustomRoots() {
			// System trust store plus a hostname check is exactly what the
			// driver's own tls=true does.
			return "true", nil
		}
	}

	tlsCfg, err := s.GoTLSConfig()
	if err != nil {
		return "", err
	}
	name := base.TLSConfigName(s)
	if err := mysqldrv.RegisterTLSConfig(name, tlsCfg); err != nil {
		return "", fmt.Errorf("register TLS config for tlsMode %q: %w", s.Mode, err)
	}
	return name, nil
}

// openDB opens a *sql.DB for the given direct-mode location.
func (d *MariaDBDriver) openDB(loc base.DBMSLocation) (*sql.DB, error) {
	dsn, err := d.buildDSN(loc)
	if err != nil {
		return nil, fmt.Errorf("open MariaDB connection: %w", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open MariaDB connection: %w", err)
	}
	return db, nil
}

// buildMariaDBCmd returns the base mariadb CLI command for SSH tunnel mode.
func (d *MariaDBDriver) buildMariaDBCmd(cfg *base.SSHTunnelConfig, database string) string {
	dbHost := cfg.DBHost
	if dbHost == "" {
		dbHost = "127.0.0.1"
	}
	dbPort := cfg.DBPort
	if dbPort == 0 {
		dbPort = 3306
	}
	// --default-character-set pins the connection for both roles this command
	// serves: the catalog queries Inspect runs through it, and the restore that
	// streams a dump into it. Left to the client's compiled-in default, an older
	// remote host reads 4-byte characters as utf8mb3 and stores mojibake.
	parts := []string{"mariadb", "--batch", "--silent",
		"--default-character-set=" + mariadbDumpCharset,
		"-h", dbHost, "-P", strconv.Itoa(dbPort)}
	if cfg.Username != "" {
		parts = append(parts, "-u", cfg.Username)
	}
	if cfg.Password != "" {
		parts = append(parts, "-p"+cfg.Password)
	}
	if database != "" {
		parts = append(parts, database)
	}
	return strings.Join(parts, " ")
}

// mariadbSSHMaxLine bounds a single result line read from the mariadb CLI. The
// default bufio.Scanner limit is 64 KiB, which a long column comment or default
// expression can exceed; the query would then fail with "token too long"
// instead of returning the row.
const mariadbSSHMaxLine = 16 << 20

// mariadbQueryViaSSH runs a SQL query on the remote MariaDB host and returns tab-separated rows.
//
// In --batch mode the client escapes characters that would otherwise break the
// layout — a tab inside a value is written as \t, a newline as \n — so fields
// are split on the literal separators first and unescaped afterwards. Without
// that second step a multi-line column comment would come back with a stray
// backslash sequence where its newline was.
// exec carries the SSH connection: callers that issue several queries dial once
// with base.DialSSHExec so the handshake is not repeated per query.
func (d *MariaDBDriver) mariadbQueryViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, query string) ([][]string, error) {
	var buf bytes.Buffer
	cmd := d.buildMariaDBCmd(cfg, database)
	if err := exec.Run(cmd, strings.NewReader(query+"\n"), &buf); err != nil {
		return nil, err
	}
	var result [][]string
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 0, 64*1024), mariadbSSHMaxLine)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for i, f := range fields {
			fields[i] = mariadbUnescapeBatch(f)
		}
		result = append(result, fields)
	}
	return result, sc.Err()
}

// mariadbUnescapeBatch reverses the escaping the mariadb client applies to
// values in --batch mode.
func mariadbUnescapeBatch(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case '\\':
			b.WriteByte('\\')
		default:
			// Not an escape the client produces; keep both bytes verbatim.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// =============================================================================
// Internal Helpers — Dump Command Builder
// =============================================================================

// buildSSHDumpArgs builds the mariadb-dump argument list for SSH tunnel mode.
func (d *MariaDBDriver) buildSSHDumpArgs(cfg *base.SSHTunnelConfig, database, scope string, filter *filterpkg.DBMSFilterOption) []string {
	dbHost := cfg.DBHost
	if dbHost == "" {
		dbHost = "127.0.0.1"
	}
	dbPort := cfg.DBPort
	if dbPort == 0 {
		dbPort = 3306
	}

	// --default-character-set pins the connection mariadb-dump reads through.
	// Without it the client falls back to its compiled-in default, which on an
	// older remote host is utf8mb3 and silently mangles 4-byte characters.
	args := []string{
		"--no-tablespaces", "--routines", "--triggers", "--events",
		"--default-character-set=" + mariadbDumpCharset,
		"-h", dbHost, "-P", strconv.Itoa(dbPort),
	}
	if cfg.Username != "" {
		args = append(args, "-u", cfg.Username)
	}
	if cfg.Password != "" {
		args = append(args, "-p"+cfg.Password)
	}
	switch scope {
	case base.ScopeSchemaOnly:
		args = append(args, "--no-data")
	case base.ScopeDataOnly:
		args = append(args, "--no-create-info")
	}
	args = append(args, database)

	// Column exclusion is impossible with mariadb-dump (it dumps whole tables),
	// so over ssh-tunnel only whole-table exclusions apply; column rules are
	// rejected by validation before reaching here.
	for _, t := range filter.ExcludedTableNames() {
		args = append(args, "--ignore-table="+database+"."+t)
	}
	return args
}

// =============================================================================
// Internal Helpers — Schema Discovery
// =============================================================================

// showCreateObject executes a SHOW CREATE query and returns the column at createColIdx.
// Handles MariaDB's varying column count across versions gracefully.
func (d *MariaDBDriver) showCreateObject(ctx context.Context, db *sql.DB, query string, createColIdx int) (string, error) {
	stmt, _, err := d.showCreateObjectWithCollation(ctx, db, query, createColIdx)
	return stmt, err
}

// showCreateObjectWithCollation additionally returns the collation_connection
// SHOW CREATE reports beside the statement — the session collation the object
// was defined under, which MariaDB stores with it and applies to the string
// comparisons in its body.
//
// Every SHOW CREATE that reports it places character_set_client and
// collation_connection immediately after the statement, whatever the object:
// a view puts the statement at index 1 and those at 2 and 3, a routine or
// trigger at 2 with 3 and 4, an event at 3 with 4 and 5. So the collation is
// read at createColIdx+2 rather than from a per-object table.
//
// A server that reports neither — or a SHOW CREATE TABLE, which has no session
// context to report — yields an empty collation, and the caller emits the object
// without a wrapper.
func (d *MariaDBDriver) showCreateObjectWithCollation(ctx context.Context, db *sql.DB, query string, createColIdx int) (string, string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", "", err
	}
	if createColIdx >= len(cols) {
		return "", "", fmt.Errorf("createColIdx %d out of range (have %d cols)", createColIdx, len(cols))
	}
	if !rows.Next() {
		return "", "", fmt.Errorf("no result for: %s", query)
	}

	collationColIdx := createColIdx + 2
	dest := make([]interface{}, len(cols))
	var result, collation sql.NullString
	for i := range dest {
		switch i {
		case createColIdx:
			dest[i] = &result
		case collationColIdx:
			dest[i] = &collation
		default:
			dest[i] = new(sql.RawBytes)
		}
	}
	if err := rows.Scan(dest...); err != nil {
		return "", "", err
	}
	if !result.Valid {
		return "", "", fmt.Errorf("NULL create statement — check privileges")
	}
	// The DEFINER the source stamped on the object names an account that need not
	// exist on the destination, and that the restoring account is rarely allowed
	// to speak for. It is dropped here, at the one point every SHOW CREATE in this
	// driver passes through, so that no object type can be missed. SHOW CREATE
	// TABLE carries no DEFINER and is returned unchanged. See base.StripDefiner.
	return base.StripDefiner(result.String), collation.String, nil
}

// mariadbWithCollation wraps a CREATE statement so the object is defined under
// the collation it had on the source instead of the target session's.
//
// MariaDB stores collation_connection with a view, routine, trigger or event and
// uses it for the string comparisons inside it, so an object recreated under the
// target's default compares differently while looking identical. mariadb-dump
// writes the same guard; without it a migration over accessType "direct" and one
// over "ssh-tunnel" produce objects that behave differently.
//
// Only collation_connection is set. character_set_client stays as the dump
// header left it, because that one governs how the bytes of this file are read
// and the file is written in mariadbDumpCharset regardless of what the source
// session used — restoring the source's value there would misread the statement
// rather than reproduce it.
//
// stmt is written between the two SET statements exactly as given, so a caller
// that needs a DELIMITER block passes it already wrapped in one.
func mariadbWithCollation(stmt, collation string) string {
	if collation == "" {
		return stmt
	}
	// The value is a string literal, not a back-quoted identifier: back quotes
	// would make the server read it as a column reference and reject the
	// statement.
	return "SET @saved_col_connection = @@collation_connection;\n" +
		"SET collation_connection = '" + mariadbEscape(collation) + "';\n" +
		stmt +
		"SET collation_connection = @saved_col_connection;\n"
}

func (d *MariaDBDriver) listTables(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME",
		database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return filter.FilterTables(names), rows.Err()
}

func (d *MariaDBDriver) listViews(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME",
		database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return filter.FilterTables(names), rows.Err()
}

func (d *MariaDBDriver) listRoutines(ctx context.Context, db *sql.DB, database, routineType string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_TYPE=? AND ROUTINE_SCHEMA=? ORDER BY ROUTINE_NAME",
		routineType, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// listTriggers returns trigger names, filtered by the table they belong to.
func (d *MariaDBDriver) listTriggers(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? ORDER BY TRIGGER_NAME",
		database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var triggerName, tableName string
		if err := rows.Scan(&triggerName, &tableName); err != nil {
			return nil, err
		}
		if len(filter.FilterTables([]string{tableName})) > 0 {
			names = append(names, triggerName)
		}
	}
	return names, rows.Err()
}

func (d *MariaDBDriver) listEvents(ctx context.Context, db *sql.DB, database string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA=? ORDER BY EVENT_NAME",
		database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// =============================================================================
// Internal Helpers — Data Dump
// =============================================================================

// buildSelectQuery builds the SELECT used to dump a table's rows. When the
// table has column exclusions only the kept columns are selected (so INSERT can
// list them); otherwise SELECT * is used. Any row_exclude rules add a WHERE
// clause that drops the matching rows.
func (d *MariaDBDriver) buildSelectQuery(ctx context.Context, db *sql.DB, database, table string, filter *filterpkg.DBMSFilterOption) (string, error) {
	selectList := "*"
	if filter.HasColumnExclusion(table) {
		allCols, err := d.listColumns(ctx, db, database, table)
		if err != nil {
			return "", err
		}
		kept := filter.KeepColumns(table, allCols)
		quoted := make([]string, len(kept))
		for i, c := range kept {
			quoted[i] = "`" + c + "`"
		}
		selectList = strings.Join(quoted, ", ")
	}
	query := fmt.Sprintf("SELECT %s FROM `%s`", selectList, table)
	if where := mariadbRowExcludeClause(filter, table); where != "" {
		query += " WHERE " + where
	}
	return query, nil
}

// mariadbRowExcludeClause builds the WHERE body that keeps every row not matched
// by a row_exclude predicate. Each predicate m is negated as
// "(NOT (m) OR (m) IS NULL)" so a row survives when m is FALSE or UNKNOWN (NULL);
// only rows for which m is definitively TRUE are dropped. Multiple predicates
// are AND-joined. Returns "" when the table has no row_exclude rules.
func mariadbRowExcludeClause(filter *filterpkg.DBMSFilterOption, table string) string {
	matches := filter.RowExcludeMatches(table)
	if len(matches) == 0 {
		return ""
	}
	terms := make([]string, len(matches))
	for i, m := range matches {
		terms[i] = fmt.Sprintf("(NOT (%s) OR (%s) IS NULL)", m, m)
	}
	return strings.Join(terms, " AND ")
}

// listColumns returns the column names of table in ordinal order.
func (d *MariaDBDriver) listColumns(ctx context.Context, db *sql.DB, database, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// =============================================================================
// Internal Helpers — Statement Splitting and Value Formatting
// =============================================================================

// splitMariaDBStatements splits a MariaDB SQL script into individual statements,
// correctly handling DELIMITER changes used by stored routines and triggers.
func splitMariaDBStatements(script string) []string {
	var stmts []string
	delim := ";"
	var buf strings.Builder

	sc := bufio.NewScanner(strings.NewReader(script))
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for sc.Scan() {
		line := sc.Text()
		upper := strings.ToUpper(strings.TrimSpace(line))

		if strings.HasPrefix(upper, "DELIMITER") {
			if s := strings.TrimSpace(buf.String()); s != "" {
				stmts = append(stmts, s)
			}
			buf.Reset()
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				delim = parts[1]
			}
			continue
		}

		buf.WriteString(line)
		buf.WriteString("\n")

		current := strings.TrimRight(buf.String(), " \t\r\n")
		if strings.HasSuffix(current, delim) {
			stmt := strings.TrimSpace(current[:len(current)-len(delim)])
			if stmt != "" {
				stmts = append(stmts, stmt)
			}
			buf.Reset()
		}
	}

	if s := strings.TrimSpace(buf.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

var (
	mariadbInsertRe      = regexp.MustCompile("(?i)^INSERT\\s+(?:IGNORE\\s+)?INTO\\s+`?(\\w+)`?")
	mariadbCreateTableRe = regexp.MustCompile("(?i)^CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?`?(\\w+)`?")
)

func extractMariaDBInsertTable(stmt string) string {
	m := mariadbInsertRe.FindStringSubmatch(stmt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractMariaDBCreateTable(stmt string) string {
	m := mariadbCreateTableRe.FindStringSubmatch(stmt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// mariadbFormatValue converts a scanned database/sql value to a MariaDB literal.
func mariadbFormatValue(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	switch val := v.(type) {
	case int64:
		return strconv.FormatInt(val, 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "1"
		}
		return "0"
	case []byte:
		return "'" + mariadbEscape(string(val)) + "'"
	case string:
		return "'" + mariadbEscape(val) + "'"
	case time.Time:
		return "'" + val.Format("2006-01-02 15:04:05") + "'"
	default:
		return "'" + mariadbEscape(fmt.Sprintf("%v", v)) + "'"
	}
}

// mariadbEscape escapes a string for safe use inside single-quoted MariaDB literals.
func mariadbEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\x00", `\0`)
	s = strings.ReplaceAll(s, "\x1a", `\Z`)
	return s
}

// =============================================================================
// Internal Helpers — FK Extraction
// =============================================================================

// mariadbExtractFKs removes CONSTRAINT ... FOREIGN KEY lines from a CREATE TABLE
// statement and returns ALTER TABLE ... ADD CONSTRAINT statements to apply them
// after all tables have been created. This prevents circular FK failures.
func mariadbExtractFKs(tableName, createSQL string) (cleanSQL string, alters []string) {
	lines := strings.Split(createSQL, "\n")
	clean := make([]string, 0, len(lines))
	var fkDefs []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "CONSTRAINT") && strings.Contains(trimmed, "FOREIGN KEY") {
			fkDefs = append(fkDefs, strings.TrimRight(trimmed, ", \t"))
		} else {
			clean = append(clean, line)
		}
	}

	// Strip trailing comma from the last content line before the closing paren.
	if len(fkDefs) > 0 {
		for i := len(clean) - 1; i >= 0; i-- {
			t := strings.TrimSpace(clean[i])
			if t == "" || strings.HasPrefix(t, ")") {
				continue
			}
			if strings.HasSuffix(strings.TrimRight(clean[i], " \t"), ",") {
				clean[i] = strings.TrimRight(clean[i], " \t,")
			}
			break
		}
	}

	cleanSQL = strings.Join(clean, "\n")
	for _, def := range fkDefs {
		alters = append(alters, fmt.Sprintf("ALTER TABLE `%s` ADD %s", tableName, def))
	}
	return cleanSQL, alters
}

// mariadbFKRef returns the referenced table and columns of an
// "ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY (...) REFERENCES `t` (`c`, ...)"
// statement. Both results are empty when no REFERENCES clause is present.
func mariadbFKRef(alter string) (refTable string, refCols []string) {
	i := strings.Index(strings.ToUpper(alter), "REFERENCES")
	if i < 0 {
		return "", nil
	}
	rest := alter[i+len("REFERENCES"):]
	refTable = mariadbBacktickIdent(rest)
	if refTable == "" {
		return "", nil
	}
	// Skip past the table identifier so the first parenthesised group found is
	// the referenced column list, not part of the table name.
	if j := strings.Index(rest, "`"+refTable+"`"); j >= 0 {
		rest = rest[j+len(refTable)+2:]
	}
	return refTable, mariadbParenIdents(rest)
}

// mariadbFKRefDropped reports whether the parent side of an FK ALTER statement
// was removed by filter — either the referenced table is wholly excluded or one
// of the referenced columns is. It returns the referenced table name and a short
// reason, with an empty reason meaning the FK is safe to emit.
func mariadbFKRefDropped(alter string, filter *filterpkg.DBMSFilterOption) (refTable, reason string) {
	refTable, refCols := mariadbFKRef(alter)
	if refTable == "" {
		return "", ""
	}
	if filter.IsTableExcluded(refTable) {
		return refTable, "referenced table excluded"
	}
	dropped := filter.ExcludedColumns(refTable)
	for _, c := range refCols {
		if dropped[c] {
			return refTable, "referenced column " + c + " excluded"
		}
	}
	return refTable, ""
}

// =============================================================================
// Internal Helpers — Column Exclusion (DDL rewrite)
// =============================================================================

// mariadbDropColumns rewrites a SHOW CREATE TABLE statement, removing the
// excluded columns and any index/key/constraint that references one of them (a
// composite key touching an excluded column is dropped whole). Separator commas
// are renormalised so the last remaining definition carries no trailing comma.
// Returns createSQL unchanged when excluded is empty.
func mariadbDropColumns(createSQL string, excluded map[string]bool) string {
	if len(excluded) == 0 {
		return createSQL
	}
	lines := strings.Split(createSQL, "\n")
	var out []string
	var bodyIdx []int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TABLE") ||
			strings.HasPrefix(trimmed, ")") {
			out = append(out, line)
			continue
		}
		if mariadbLineRefsExcluded(trimmed, excluded) {
			continue
		}
		out = append(out, line)
		bodyIdx = append(bodyIdx, len(out)-1)
	}
	for i, idx := range bodyIdx {
		l := strings.TrimRight(out[idx], " \t")
		l = strings.TrimRight(l, ",")
		if i < len(bodyIdx)-1 {
			l += ","
		}
		out[idx] = l
	}
	return strings.Join(out, "\n")
}

// mariadbLineRefsExcluded reports whether a CREATE TABLE body line defines or
// references an excluded column. Column definitions start with a backtick-quoted
// name; index/key/constraint lines start with a keyword and carry their columns
// in the first parenthesised group.
func mariadbLineRefsExcluded(line string, excluded map[string]bool) bool {
	if strings.HasPrefix(line, "`") {
		if name := mariadbBacktickIdent(line); name != "" {
			return excluded[name]
		}
		return false
	}
	for _, c := range mariadbParenIdents(line) {
		if excluded[c] {
			return true
		}
	}
	return false
}

// mariadbDropIndexes rewrites a SHOW CREATE TABLE statement, removing any named
// index (KEY/UNIQUE KEY/FULLTEXT KEY/SPATIAL KEY) excluded by an object_exclude
// rule of kind "index" for table. PRIMARY KEY (unnamed) and FK CONSTRAINT lines
// are never touched. Separator commas are renormalised so the last remaining
// definition carries no trailing comma. Returns createSQL unchanged when nothing
// is dropped.
func mariadbDropIndexes(createSQL, table string, filter *filterpkg.DBMSFilterOption) string {
	lines := strings.Split(createSQL, "\n")
	var out []string
	var bodyIdx []int
	dropped := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TABLE") ||
			strings.HasPrefix(trimmed, ")") {
			out = append(out, line)
			continue
		}
		if name := mariadbIndexName(trimmed); name != "" &&
			filter.IsObjectExcludedInTable(filterpkg.ObjectKindIndex, table, name) {
			dropped = true
			continue
		}
		out = append(out, line)
		bodyIdx = append(bodyIdx, len(out)-1)
	}
	if !dropped {
		return createSQL
	}
	for i, idx := range bodyIdx {
		l := strings.TrimRight(out[idx], " \t")
		l = strings.TrimRight(l, ",")
		if i < len(bodyIdx)-1 {
			l += ","
		}
		out[idx] = l
	}
	return strings.Join(out, "\n")
}

// mariadbIndexName returns the index name of a CREATE TABLE body line that
// defines a named index (KEY/UNIQUE KEY/FULLTEXT KEY/SPATIAL KEY/INDEX). It
// returns "" for PRIMARY KEY (unnamed), FK CONSTRAINT lines, and plain column
// definitions. The name is the backtick-quoted identifier preceding the paren.
func mariadbIndexName(line string) string {
	t := strings.TrimSpace(line)
	up := strings.ToUpper(t)
	if strings.HasPrefix(up, "PRIMARY KEY") || strings.HasPrefix(up, "CONSTRAINT") {
		return ""
	}
	prefix := t
	if p := strings.IndexByte(t, '('); p >= 0 {
		prefix = t[:p]
	}
	pu := strings.ToUpper(prefix)
	if !strings.Contains(pu, "KEY") && !strings.Contains(pu, "INDEX") {
		return ""
	}
	return mariadbBacktickIdent(prefix)
}

// mariadbConstraintName returns the constraint identifier of an
// "ALTER TABLE `t` ADD CONSTRAINT `name` FOREIGN KEY ..." statement — the first
// backtick-quoted identifier following the CONSTRAINT keyword. Returns "" when
// no CONSTRAINT clause is present.
func mariadbConstraintName(alter string) string {
	i := strings.Index(strings.ToUpper(alter), "CONSTRAINT")
	if i < 0 {
		return ""
	}
	return mariadbBacktickIdent(alter[i:])
}

// mariadbBacktickIdent returns the identifier inside the first `...` pair.
func mariadbBacktickIdent(s string) string {
	i := strings.IndexByte(s, '`')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i+1:], '`')
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

// mariadbParenIdents returns the backtick-quoted identifiers inside the first
// top-level parenthesised group of s (the column list of an index/constraint).
func mariadbParenIdents(s string) []string {
	start := strings.IndexByte(s, '(')
	if start < 0 {
		return nil
	}
	depth, end := 0, -1
	for i := start; i < len(s) && end < 0; i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		end = len(s)
	}
	inner := s[start+1 : end]
	var idents []string
	for {
		i := strings.IndexByte(inner, '`')
		if i < 0 {
			break
		}
		rest := inner[i+1:]
		j := strings.IndexByte(rest, '`')
		if j < 0 {
			break
		}
		idents = append(idents, rest[:j])
		inner = rest[j+1:]
	}
	return idents
}

// =============================================================================
// ListSchemas
// =============================================================================

// ListSchemas reports no schemas: MariaDB uses "schema" and "database" as
// synonyms, so what a caller would look for here is what ListDatabases already
// returns. See base.DBDriver.ListSchemas.
func (d *MariaDBDriver) ListSchemas(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	return nil, nil
}

// =============================================================================
// Target preparation
// =============================================================================

// DropAllObjects is a no-op for MariaDB: rollbackDrop already rolls back via a
// generated DROP script, so this is never called for this engine.
func (d *MariaDBDriver) DropAllObjects(ctx context.Context, loc base.DBMSLocation) error {
	return nil
}

// PrepareTarget creates the target database and user before migration.
func (d *MariaDBDriver) PrepareTarget(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	if loc.IsDirect() {
		return d.prepareTargetViaSQL(ctx, loc, grant)
	}
	return d.prepareTargetViaSSH(ctx, loc, grant)
}

// prepareTargetViaSQL grants access to a pre-existing target database via a
// direct SQL connection. The database must already exist and be empty;
// dbmsx does not create it.
func (d *MariaDBDriver) prepareTargetViaSQL(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	// Connect to server root without selecting a database.
	adminLoc := loc
	adminLoc.Database = ""
	db, err := d.openDB(adminLoc)
	if err != nil {
		return fmt.Errorf("prepareTarget open admin connection: %w", err)
	}
	defer db.Close()

	// The target database must already exist.
	exists, err := d.databaseExistsSQL(ctx, db, loc.Database)
	if err != nil {
		return fmt.Errorf("prepareTarget: %w", err)
	}
	if !exists {
		return &base.TargetDatabaseNotFoundError{DBMSType: base.DBMSTypeMariaDB, Database: loc.Database}
	}

	// Database exists — require it to be empty before granting access.
	if err := d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	host := grant.Host
	if host == "" {
		host = "%"
	}
	if _, err = db.ExecContext(ctx,
		"CREATE USER IF NOT EXISTS ?@? IDENTIFIED BY ?",
		grant.Username, host, grant.Password); err != nil {
		return fmt.Errorf("prepareTarget CREATE USER: %w", err)
	}
	if _, err = db.ExecContext(ctx,
		"GRANT ALL PRIVILEGES ON `"+loc.Database+"`.* TO ?@?",
		grant.Username, host); err != nil {
		return fmt.Errorf("prepareTarget GRANT: %w", err)
	}
	return nil
}

// prepareTargetViaSSH grants access to a pre-existing target database via the
// SSH-tunnel CLI. The database must already exist and be empty; dbmsx does not
// create it.
func (d *MariaDBDriver) prepareTargetViaSSH(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	cfg := loc.SSHTunnel

	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	// The target database must already exist.
	exists, err := d.databaseExistsSSH(exec, cfg, loc.Database)
	if err != nil {
		return fmt.Errorf("prepareTarget: %w", err)
	}
	if !exists {
		return &base.TargetDatabaseNotFoundError{DBMSType: base.DBMSTypeMariaDB, Database: loc.Database}
	}

	// Database exists — require it to be empty before granting access.
	if err = d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	host := grant.Host
	if host == "" {
		host = "%"
	}
	createUser := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY '%s'",
		mariadbEscape(grant.Username), mariadbEscape(host), mariadbEscape(grant.Password))
	if _, err = d.mariadbQueryViaSSH(exec, cfg, "", createUser); err != nil {
		return fmt.Errorf("prepareTarget CREATE USER via SSH: %w", err)
	}
	grantSQL := fmt.Sprintf(
		"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s'",
		loc.Database, mariadbEscape(grant.Username), mariadbEscape(host))
	if _, err = d.mariadbQueryViaSSH(exec, cfg, "", grantSQL); err != nil {
		return fmt.Errorf("prepareTarget GRANT via SSH: %w", err)
	}
	return nil
}
