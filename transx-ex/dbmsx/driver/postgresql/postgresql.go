package postgresql

import (
	"bufio"
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// =============================================================================
// PostgreSQLDriver
// =============================================================================

// PostgreSQLDriver implements DBDriver for PostgreSQL databases.
// Direct mode uses github.com/jackc/pgx/v5.
// SSH-tunnel mode uses pg_dump and psql CLI tools.
type PostgreSQLDriver struct{}

// pgMaintenanceDB is the database PostgreSQL always ships with. Server-level
// queries (e.g. version) connect here instead of the target database, which
// may not exist yet.
//
// ⚠ It is the preferred entry, not a guaranteed one. A managed service may deny
//
//	its own master account CONNECT on it - NCP Cloud DB for PostgreSQL does -
//	and then every server-level call fails with
//
//	  FATAL: permission denied for database "postgres"
//
//	even though the account administers its databases perfectly well. Reads go
//	through openServerConn / pgServerQueryViaSSH, which fall back to the
//	connection's own database; the catalogs they read (pg_database, pg_roles) are
//	cluster-wide and SHOW server_version is a server property, so any connectable
//	database answers identically.
//
//	CREATE DATABASE and DROP DATABASE do NOT use that fallback. They need a
//	database other than the one being created or dropped, and the connection's
//	own database is exactly the one they cannot be connected to.
const pgMaintenanceDB = "postgres"

// NewPostgreSQLDriver returns a new PostgreSQLDriver.
func NewPostgreSQLDriver() *PostgreSQLDriver { return &PostgreSQLDriver{} }

var _ base.DBDriver = (*PostgreSQLDriver)(nil)

// =============================================================================
// Dump
// =============================================================================

// Dump exports the PostgreSQL database at loc to outputPath according to scope.
// If scope is empty it defaults to ScopeFull.
// w, when non-nil, receives the bytes written for progress tracking.
// opt may carry a schema rename; see pgDumpScope for what that changes.
func (d *PostgreSQLDriver) Dump(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer, opt *base.DumpOption) error {
	if scope == "" {
		scope = base.ScopeFull
	}
	logger.Info("postgresql dump started", "database", loc.Database, "scope", scope, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.dumpViaSQL(ctx, loc, scope, outputPath, w, opt)
	}
	return d.dumpViaSSH(ctx, loc, scope, outputPath, w, opt)
}

// =============================================================================
// pgDumpScope
// =============================================================================

// pgDumpScope carries the schema decisions every statement of a dump is written
// under. There are two shapes of dump, and the number of schemas picks between
// them.
//
// One schema dumps to bare names. Nothing in the file says where the objects
// belong, so the restore places them through its own search_path — which is what
// lets a schema be migrated into a differently named one without touching a
// single statement.
//
// Several schemas dump to names qualified with the SOURCE schema, and the file
// opens with the CREATE SCHEMA statements that make them resolvable. A rename is
// then applied by appending ALTER SCHEMA … RENAME TO at the end: PostgreSQL
// tracks what a view, routine or constraint depends on by object identity rather
// than by the text that created it, so renaming the schema afterwards carries
// every internal reference with it. That is why the dump never rewrites a
// rendered definition — the server-rendered body of a view or a routine can name
// its own schema, and editing SQL text to chase those names is exactly the kind
// of guesswork a migration must not do.
type pgDumpScope struct {
	// schemas are the source schemas, in resolution order.
	schemas []string
	// rename maps a source schema to the name it should end up under. A schema
	// absent from the map keeps its own name; a nil map renames nothing.
	rename map[string]string
	// qualify reports whether statements name their schema. It is derived from
	// len(schemas) rather than set by the caller, so the dump and the restore
	// cannot disagree about the form of the file between them.
	qualify bool
}

func newPgDumpScope(schemas []string, rename map[string]string) pgDumpScope {
	return pgDumpScope{schemas: schemas, rename: rename, qualify: len(schemas) > 1}
}

// out returns the name schema ends up under once the dump has been restored.
func (s pgDumpScope) out(schema string) string {
	if to, ok := s.rename[schema]; ok && to != "" {
		return to
	}
	return schema
}

// ident renders an object of schema as it appears in the dump: qualified with
// its source schema when the dump covers several, bare when it covers one.
func (s pgDumpScope) ident(schema, name string) string {
	if !s.qualify {
		return pgQuoteIdent(name)
	}
	return pgQualifyIdent(schema, name)
}

// covers reports whether schema is one of the schemas this dump writes.
func (s pgDumpScope) covers(schema string) bool {
	for _, have := range s.schemas {
		if have == schema {
			return true
		}
	}
	return false
}

// pgRestorePrologue opens every dump this driver writes. Routines are dumped in
// name order, so one that calls another it sorts before cannot resolve the
// callee while the body is being checked; the check is deferred to the first
// call instead, exactly as a pg_dump restore does it.
const pgRestorePrologue = "SET check_function_bodies = false;\n\n"

// preamble returns the CREATE SCHEMA statements a qualified dump opens with,
// since the restore cannot create a table in a schema that is not there yet. A
// single-schema dump needs none: its bare names land wherever the restore's
// search_path points, and the restore creates that schema itself.
func (s pgDumpScope) preamble() string {
	if !s.qualify {
		return ""
	}
	var b strings.Builder
	for _, schema := range s.schemas {
		fmt.Fprintf(&b, "CREATE SCHEMA IF NOT EXISTS %s;\n", pgQuoteIdent(schema))
	}
	b.WriteString("\n")
	return b.String()
}

// renameTail returns the ALTER SCHEMA statements that close a qualified dump,
// one per schema whose name changes. A single-schema dump returns none — the
// restore's search_path has already placed its objects.
func (s pgDumpScope) renameTail() string {
	if !s.qualify {
		return ""
	}
	return pgRenameStatements(s.schemas, s.rename)
}

// pgRenameStatements builds the ALTER SCHEMA block that closes a dump whose
// objects were written under their source schema names, visiting the schemas in
// the given order. It returns "" when nothing changes name.
func pgRenameStatements(order []string, rename map[string]string) string {
	if len(rename) == 0 {
		return ""
	}
	var b strings.Builder
	for _, schema := range order {
		to, ok := rename[schema]
		if !ok || to == "" || to == schema {
			continue
		}
		fmt.Fprintf(&b, "ALTER SCHEMA %s RENAME TO %s;\n", pgQuoteIdent(schema), pgQuoteIdent(to))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n" + b.String()
}

func (d *PostgreSQLDriver) dumpViaSQL(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer, opt *base.DumpOption) error {
	conn, err := d.openConn(ctx, loc)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	schemas, err := d.resolvePgSchemasViaSQL(ctx, conn, loc)
	if err != nil {
		return err
	}
	if len(schemas) == 0 {
		return fmt.Errorf("database %q has no user schemas to dump", loc.Database)
	}
	sc := newPgDumpScope(schemas, base.PgSchemaRenameOpt(opt))
	pgWarnRulesOutOfScope(loc.Filter, sc)

	if err := d.setDumpSearchPath(ctx, conn, sc); err != nil {
		return err
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}

	// The dump carries the setting itself so a restore run through psql — which
	// takes no session setup from this driver — gets it too. See the matching
	// SET in restoreViaSQL for why routines need it.
	if _, err := io.WriteString(dst, pgRestorePrologue); err != nil {
		return err
	}

	if _, err := io.WriteString(dst, sc.preamble()); err != nil {
		return err
	}

	if scope == base.ScopeSchemaOnly || scope == base.ScopeFull {
		if err := d.dumpSchemaViaSQL(ctx, conn, sc, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump schema: %w", err)
		}
	}
	if scope == base.ScopeDataOnly || scope == base.ScopeFull {
		if err := d.dumpDataViaSQL(ctx, conn, sc, loc.Filter, dst); err != nil {
			return fmt.Errorf("dump data: %w", err)
		}
	}

	_, err = io.WriteString(dst, sc.renameTail())
	return err
}

// setDumpSearchPath decides how the server renders the definitions this dump
// asks it for — view bodies, routine sources, index and rule definitions. The
// server qualifies a name that the current search_path does not already resolve,
// and leaves it bare when it does, so the setting has to match the shape of the
// file being written.
//
// A single-schema dump puts that schema on the path, and the definitions come
// back bare like the statements around them — which is what allows the restore
// to redirect the whole file to another schema.
//
// A multi-schema dump empties the path, so every rendered definition names its
// schema explicitly. Bare names would be ambiguous in a file that spans schemas:
// the restore would resolve them through its own search_path and could well pick
// a different table of the same name.
func (d *PostgreSQLDriver) setDumpSearchPath(ctx context.Context, conn *pgx.Conn, sc pgDumpScope) error {
	path := "''"
	if !sc.qualify {
		path = pgQuoteIdent(sc.schemas[0])
	}
	if _, err := conn.Exec(ctx, "SET search_path = "+path); err != nil {
		return fmt.Errorf("set search_path for dump: %w", err)
	}
	return nil
}

// dumpSchemaViaSQL writes DDL in PostgreSQL dependency order:
// Extensions → Types → Sequences → Tables(FK excluded) → Indexes → FK Constraints →
// Views → Materialized Views → Functions/Procedures → Triggers → Rules.
//
// The order is by object kind, not by schema, and that is what makes several
// schemas safe to dump together: every table exists before the first foreign key
// is added, so a constraint pointing across schemas cannot be applied before its
// parent table is there.
func (d *PostgreSQLDriver) dumpSchemaViaSQL(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	// --- Extensions ---
	// Database-scoped, so they are dumped whole regardless of the schemas
	// selected: a type or index method the selected schemas depend on may well be
	// installed elsewhere in the database.
	exts, err := d.listExtensions(ctx, conn, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindExtension}.Fail(base.ActionList, err)
	}
	for _, ext := range exts {
		fmt.Fprintf(dst, "CREATE EXTENSION IF NOT EXISTS %s;\n\n", pgQuoteIdent(ext))
	}

	// --- Custom Types (enum, composite, domain) ---
	if err := d.dumpTypes(ctx, conn, sc, filter, dst); err != nil {
		return err
	}

	// --- Sequences ---
	seqs, err := d.listSequences(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindSequence}.Fail(base.ActionList, err)
	}
	for _, seq := range seqs {
		fmt.Fprintf(dst, "%s;\n\n", seq)
	}

	// --- Tables (without FK constraints) ---
	tables, err := d.listTables(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: base.ObjectKindTable}.Fail(base.ActionList, err)
	}
	for i, table := range tables {
		obj := base.Obj{
			Kind: base.ObjectKindTable, Owner: table.Schema, Name: table.Name,
			Index: i + 1, Total: len(tables),
		}
		createSQL, err := d.buildCreateTable(ctx, conn, sc, table, pgExcludedColumns(filter, table))
		if err != nil {
			return obj.Fail(base.ActionReadDDL, err)
		}
		fmt.Fprintf(dst, "%s\n\n", createSQL)
	}

	// --- Indexes (non-primary) ---
	indexes, err := d.listIndexDefs(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindIndex}.Fail(base.ActionList, err)
	}
	for _, idx := range indexes {
		fmt.Fprintf(dst, "%s;\n", idx)
	}
	if len(indexes) > 0 {
		fmt.Fprintln(dst)
	}

	// --- FK Constraints ---
	fks, err := d.listFKConstraints(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindForeignKey}.Fail(base.ActionList, err)
	}
	for _, fk := range fks {
		fmt.Fprintf(dst, "%s;\n", fk)
	}
	if len(fks) > 0 {
		fmt.Fprintln(dst)
	}

	// --- Views ---
	views, err := d.listViews(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindView}.Fail(base.ActionList, err)
	}
	for _, v := range views {
		fmt.Fprintf(dst, "%s\n\n", v)
	}

	// --- Materialized Views ---
	matviews, err := d.listMatViews(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindMaterializedView}.Fail(base.ActionList, err)
	}
	for _, mv := range matviews {
		fmt.Fprintf(dst, "%s\n\n", mv)
	}

	// --- Functions ---
	funcs, err := d.listFunctions(ctx, conn, sc, filter, "f")
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindFunction}.Fail(base.ActionList, err)
	}
	for _, fn := range funcs {
		fmt.Fprintf(dst, "%s\n\n", fn)
	}

	// --- Procedures ---
	procs, err := d.listFunctions(ctx, conn, sc, filter, "p")
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindProcedure}.Fail(base.ActionList, err)
	}
	for _, p := range procs {
		fmt.Fprintf(dst, "%s\n\n", p)
	}

	// --- Triggers ---
	triggers, err := d.listTriggers(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindTrigger}.Fail(base.ActionList, err)
	}
	for _, trig := range triggers {
		fmt.Fprintf(dst, "%s;\n\n", trig)
	}

	// --- Rules ---
	rules, err := d.listRules(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: filterpkg.ObjectKindRule}.Fail(base.ActionList, err)
	}
	for _, rule := range rules {
		fmt.Fprintf(dst, "%s\n\n", rule)
	}

	return nil
}

// dumpDataViaSQL writes COPY ... FROM STDIN blocks for all rows in each table.
func (d *PostgreSQLDriver) dumpDataViaSQL(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	tables, err := d.listTables(ctx, conn, sc, filter)
	if err != nil {
		return base.Obj{Kind: base.ObjectKindTable}.Fail(base.ActionList, err)
	}

	for i, table := range tables {
		obj := base.Obj{
			Kind: base.ObjectKindTable, Owner: table.Schema, Name: table.Name,
			Index: i + 1, Total: len(tables),
		}
		query, colClause, err := d.buildCopyQuery(ctx, conn, sc, table, filter)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}

		rows, err := conn.Query(ctx, query)
		if err != nil {
			return obj.Fail(base.ActionReadRows, err)
		}

		fmt.Fprintf(dst, "COPY %s%s FROM STDIN;\n", sc.ident(table.Schema, table.Name), colClause)
		// A row that cannot be read is reported by its position in the table
		// rather than the table's position in the dump: with thousands of rows
		// alike, which one stopped the scan is the part that is hard to find.
		rowNum := 0
		rowObj := base.Obj{
			Kind: base.ObjectKindTable, Owner: table.Schema, Name: table.Name, Unit: "row",
		}
		for rows.Next() {
			rowNum++
			vals, err := rows.Values()
			if err != nil {
				rows.Close()
				rowObj.Index = rowNum
				return rowObj.Fail(base.ActionReadRows, err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = pgFormatCopyValue(v)
			}
			fmt.Fprintf(dst, "%s\n", strings.Join(parts, "\t"))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			rowObj.Index = rowNum
			return rowObj.Fail(base.ActionReadRows, err)
		}
		fmt.Fprintf(dst, "\\.\n\n")
	}
	return nil
}

func (d *PostgreSQLDriver) dumpViaSSH(ctx context.Context, loc base.DBMSLocation, scope, outputPath string, w io.Writer, opt *base.DumpOption) error {
	cfg := loc.SSHTunnel
	args := d.buildSSHDumpArgs(cfg, loc, scope)
	cmd := "pg_dump " + strings.Join(args, " ")

	// The schemas are whatever loc names, since this path hands them to pg_dump
	// rather than resolving them against the database. A location naming none
	// covers every schema, so no rule can fall outside it and there is nothing
	// to check — and nothing to check it against without a query this path does
	// not otherwise make.
	if len(loc.PgSchema) > 0 {
		pgWarnRulesOutOfScope(loc.Filter, newPgDumpScope(loc.PgSchema, nil))
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer f.Close()

	dst := io.Writer(f)
	if w != nil {
		dst = io.MultiWriter(f, w)
	}

	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, dst); err != nil {
		return &base.OperationError{
			Operation:   base.OperationDump,
			DBMSType:    base.DBMSTypePostgreSQL,
			Source:      loc.Database,
			Destination: outputPath,
			Command:     cmd,
			Output:      base.SSHStderr(err),
			Err:         err,
		}
	}

	// pg_dump writes its own CREATE SCHEMA and SET search_path lines, so a rename
	// cannot be arranged by the restore the way it is for a single-schema dump
	// built here. It is appended instead, exactly as the multi-schema case does:
	// restore under the source names, then rename the schemas.
	_, err = io.WriteString(dst, pgRenameStatements(pgSortedRenameKeys(base.PgSchemaRenameOpt(opt)),
		base.PgSchemaRenameOpt(opt)))
	return err
}

// pgSortedRenameKeys returns the source schema names of a rename map in sorted
// order, so the statements built from it come out the same way every run.
func pgSortedRenameKeys(rename map[string]string) []string {
	keys := make([]string, 0, len(rename))
	for from := range rename {
		keys = append(keys, from)
	}
	sort.Strings(keys)
	return keys
}

// =============================================================================
// Restore
// =============================================================================

// Restore imports the SQL file at inputPath into the database at loc.
func (d *PostgreSQLDriver) Restore(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	logger.Info("postgresql restore started", "database", loc.Database, "inputPath", inputPath, "access", loc.AccessType)
	if loc.IsDirect() {
		return d.restoreViaSQL(ctx, loc, inputPath, notify)
	}
	return d.restoreViaSSH(ctx, loc, inputPath, notify)
}

func (d *PostgreSQLDriver) restoreViaSQL(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	conn, err := d.openConn(ctx, loc)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	content, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read dump file: %w", err)
	}

	// A dump of a single schema carries bare names, so where its objects land is
	// decided here rather than in the file. That is what lets one schema be
	// migrated into a differently named one — and it is why loc.PgSchema is read
	// on a destination at all. A dump spanning several schemas names them itself
	// and is left alone.
	if err := d.setRestoreSearchPath(ctx, conn, loc); err != nil {
		return err
	}

	// session_replication_role = replica silences FK checks and user triggers for
	// the duration of the load. It needs superuser (or, from PostgreSQL 15, an
	// explicit GRANT SET on the parameter), which a managed service does not give
	// its master account:
	//
	//   ERROR: permission denied to set parameter "session_replication_role" (42501)
	//
	// So it is attempted, not required. What makes the restore correct without it
	// is the deferral below: the FK constraints and triggers this dump carries are
	// applied after the rows, the same order pg_dump writes a full dump in and the
	// same order this driver's own SSH path therefore restores in. The parameter is
	// still worth setting where it is allowed - it also suppresses triggers on
	// tables the dump did not create, which a data-only load runs into.
	replicaRole := true
	if _, err := conn.Exec(ctx, "SET session_replication_role = replica"); err != nil {
		replicaRole = false
		logger.Info("postgresql restore could not set session_replication_role; "+
			"FK constraints and triggers will be applied after the rows instead",
			"database", loc.Database, "err", err.Error())
	}

	// Routines are restored in name order, so one that calls another it sorts
	// before would fail to resolve the callee. The body is validated when the
	// routine is first called instead, which is what pg_dump's restore does too.
	if _, err := conn.Exec(ctx, "SET check_function_bodies = false"); err != nil {
		return fmt.Errorf("set check_function_bodies: %w", err)
	}

	stmts := splitPGStatements(string(content))
	var matViews []string
	// postData holds the statements that must not be in force while the rows are
	// loaded: FK constraints, which the COPY order across tables can violate, and
	// triggers, which would fire on every restored row. Their order among
	// themselves is preserved.
	var postData []string
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

		// The same test the splitter used, so the two agree on what a COPY block
		// is: pg_dump writes "FROM stdin;" in lower case while this driver's own
		// dump writes it upper, and a case-sensitive test here recognises only
		// the latter — the rows of the other then reach the server as SQL.
		if firstLine, _, ok := strings.Cut(stmt, "\n"); ok && pgIsCopyFromStdin(firstLine) {
			// COPY takes its rows over the protocol's copy-in stream, not in the
			// query text: running the header through Exec would leave the server
			// waiting for data it was never sent.
			header, data := pgSplitCopyBlock(stmt)
			if _, err := conn.PgConn().CopyFrom(ctx, strings.NewReader(data), header); err != nil {
				// The header names the table; the rows below it would only
				// bury it.
				return base.RestoreStatementError(
					base.DBMSTypePostgreSQL, loc.Database, header, i+1, len(stmts), err)
			}
		} else if pgIsPostDataStmt(stmt) {
			postData = append(postData, stmt)
			continue
		} else {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return base.RestoreStatementError(
					base.DBMSTypePostgreSQL, loc.Database, stmt, i+1, len(stmts), err)
			}
		}

		if notify != nil {
			if tbl := pgExtractCreateTable(stmt); tbl != "" && tbl != lastNotifyTable {
				lastNotifyTable = tbl
				notify(tbl)
			} else if tbl := pgExtractCopyTable(stmt); tbl != "" && tbl != lastNotifyTable {
				lastNotifyTable = tbl
				notify(tbl)
			}
		}

		if strings.Contains(strings.ToUpper(stmt), "CREATE MATERIALIZED VIEW") {
			if mv := pgExtractMatViewIdent(stmt); mv != "" {
				matViews = append(matViews, mv)
			}
		}
	}

	// The rows are in; the constraints and triggers they were held back for can go
	// on now. A failure here is a real one - a FK that the migrated data does not
	// satisfy - so it is reported as the statement error it is.
	for i, stmt := range postData {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return base.RestoreStatementError(
				base.DBMSTypePostgreSQL, loc.Database, stmt, i+1, len(postData), err)
		}
	}

	if replicaRole {
		if _, err := conn.Exec(ctx, "SET session_replication_role = DEFAULT"); err != nil {
			return fmt.Errorf("reset session_replication_role: %w", err)
		}
	}

	// Refresh materialized views after all data is loaded. The name is re-quoted
	// from the (schema, name) pair the statement was read as, not from the joined
	// string: quoting "sales.mv_daily" whole would name a single view with a dot
	// in it, which is not the view that was just created.
	for i, mv := range matViews {
		if _, err := conn.Exec(ctx, "REFRESH MATERIALIZED VIEW "+mv); err != nil {
			owner, name := base.SplitQualifiedIdent(mv)
			return base.Obj{
				Kind: filterpkg.ObjectKindMaterializedView, Owner: owner, Name: name,
				Index: i + 1, Total: len(matViews),
			}.Fail(base.ActionRefresh, err)
		}
	}

	return nil
}

// setRestoreSearchPath creates and selects the destination schema when loc names
// exactly one, so that a dump written with bare names lands there.
//
// One name is the only case this handles, and deliberately so. A dump that spans
// several schemas qualifies every statement and creates the schemas itself, so
// there is nothing for a search_path to decide; a destination naming several
// schemas is describing that kind of dump, and the rename it asks for is applied
// by the ALTER SCHEMA statements the dump ends with.
func (d *PostgreSQLDriver) setRestoreSearchPath(ctx context.Context, conn *pgx.Conn, loc base.DBMSLocation) error {
	if len(loc.PgSchema) != 1 {
		return nil
	}
	schema := pgQuoteIdent(loc.PgSchema[0])
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		return fmt.Errorf("create destination schema %s: %w", schema, err)
	}
	if _, err := conn.Exec(ctx, "SET search_path = "+schema); err != nil {
		return fmt.Errorf("set search_path for restore: %w", err)
	}
	return nil
}

func (d *PostgreSQLDriver) restoreViaSSH(ctx context.Context, loc base.DBMSLocation, inputPath string, notify func(string)) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open input file: %w", err)
	}
	defer f.Close()

	cmd := d.BuildRestoreCmd(loc)
	if err := base.ExecuteViaSSH(loc.SSHTunnel.SSH, cmd, f, nil); err != nil {
		return &base.OperationError{
			Operation:   base.OperationRestore,
			DBMSType:    base.DBMSTypePostgreSQL,
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
func (d *PostgreSQLDriver) TestConnection(ctx context.Context, loc base.DBMSLocation) error {
	if loc.IsDirect() {
		conn, err := d.openConn(ctx, loc)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		return conn.Ping(ctx)
	}

	cfg := loc.SSHTunnel
	cmd := d.buildPsqlCmd(cfg, loc.Database) + ` -c "SELECT 1"`
	var out bytes.Buffer
	if err := base.ExecuteViaSSH(cfg.SSH, cmd, nil, &out); err != nil {
		return &base.OperationError{
			Operation: base.OperationConnect,
			DBMSType:  base.DBMSTypePostgreSQL,
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

// BuildDumpCmd returns the pg_dump CLI command used by PipeExecutor (T13).
func (d *PostgreSQLDriver) BuildDumpCmd(loc base.DBMSLocation, scope string) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	if scope == "" {
		scope = base.ScopeFull
	}
	args := d.buildSSHDumpArgs(loc.SSHTunnel, loc, scope)
	return "pg_dump " + strings.Join(args, " ")
}

// BuildRestoreCmd returns the psql CLI command used by PipeExecutor (T13).
func (d *PostgreSQLDriver) BuildRestoreCmd(loc base.DBMSLocation) string {
	if !loc.IsSSHTunnel() {
		return ""
	}
	return d.buildPsqlCmd(loc.SSHTunnel, loc.Database)
}

// =============================================================================
// CheckTargetEmpty
// =============================================================================

// pgTargetTableCountQuery counts the tables a migration into loc would land on
// top of.
//
// The schemas it looks at are the ones loc covers. A location naming schemas is
// asking about those and no others — a table sitting in a schema this migration
// will not write to is not in its way. A location naming none covers the whole
// database, so every user schema is counted; the pg_ prefix and
// information_schema are excluded the same way ListSchemas excludes them.
//
// Naming a schema that does not exist yet is normal rather than an error here:
// it is precisely the state a target is in before its first migration, and it
// holds no tables, which is the question being asked.
func pgTargetTableCountQuery(loc base.DBMSLocation) string {
	const prefix = `SELECT COUNT(*) AS table_count FROM information_schema.tables ` +
		`WHERE table_type='BASE TABLE' AND `
	if len(loc.PgSchema) == 0 {
		return prefix + `table_schema NOT LIKE 'pg\_%' AND table_schema <> 'information_schema'`
	}
	return prefix + `table_schema IN ` + pgSchemaLiteralList(loc.PgSchema)
}

// CheckTargetEmpty returns *TargetNotEmptyError when the destination already has tables.
// A target database that doesn't exist yet (the normal case before a first
// migration) is treated as empty rather than an error — PostgreSQL requires
// connecting to an existing database, so checking table counts on loc.Database
// directly would fail outright on a fresh target.
func (d *PostgreSQLDriver) CheckTargetEmpty(ctx context.Context, loc base.DBMSLocation) error {
	logger.Info("postgresql check target empty", "database", loc.Database, "access", loc.AccessType)
	var count int64
	query := pgTargetTableCountQuery(loc)

	if loc.IsDirect() {
		exists, err := d.databaseExistsSQL(ctx, loc)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		conn, err := d.openConn(ctx, loc)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		if err := conn.QueryRow(ctx, query).Scan(&count); err != nil {
			return err
		}
	} else {
		// Two queries (existence, then table count) share one connection.
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
			return nil
		}
		res, err := d.pgQueryViaSSH(exec, loc.SSHTunnel, loc.Database, query)
		if err != nil {
			return err
		}
		if len(res) > 0 && len(res[0]) > 0 {
			count, _ = strconv.ParseInt(strings.TrimSpace(res[0][0]), 10, 64)
		}
	}

	if count > 0 {
		return &base.TargetNotEmptyError{
			DBMSType:   base.DBMSTypePostgreSQL,
			Database:   loc.Database,
			TableCount: int(count),
		}
	}
	return nil
}

// databaseExistsSQL reports whether loc.Database exists, connecting via the
// "postgres" maintenance database (which always exists) rather than loc.Database
// itself.
func (d *PostgreSQLDriver) databaseExistsSQL(ctx context.Context, loc base.DBMSLocation) (bool, error) {
	conn, err := d.openServerConn(ctx, loc)
	if err != nil {
		return false, fmt.Errorf("open PostgreSQL connection for database existence check: %w", err)
	}
	defer conn.Close(ctx)

	return pgDatabaseExists(ctx, conn, loc.Database)
}

// pgDatabaseExists reports whether database exists, using a connection the
// caller already holds. CreateDatabase and DropDatabase go through this variant
// so their existence check and their DDL share one connection; databaseExistsSQL
// wraps it for callers that only need the single answer.
func pgDatabaseExists(ctx context.Context, conn *pgx.Conn, database string) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", database).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// databaseExistsSSH is the SSH-tunnel counterpart of databaseExistsSQL.
func (d *PostgreSQLDriver) databaseExistsSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string) (bool, error) {
	query := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='%s')", pgEscape(database))
	res, err := d.pgServerQueryViaSSH(exec, cfg, database, query)
	if err != nil {
		return false, err
	}
	if len(res) == 0 || len(res[0]) == 0 {
		return false, nil
	}
	v := strings.TrimSpace(res[0][0])
	return v == "t" || v == "true", nil
}

// =============================================================================
// ListDatabases
// =============================================================================

// pgListDatabasesQuery enumerates the connectable, non-template databases of a
// server. datistemplate drops template0/template1 and datallowconn drops any
// database explicitly closed to connections; base.UserDatabases then removes
// "postgres" itself. Every column is aliased because the SSH path wraps this
// query in json_agg, where the aliases become the JSON keys.
//
// has_database_privilege drops what a managed service keeps for its own control
// plane. RDS ("rdsadmin"), Cloud SQL ("cloudsqladmin") and Azure
// ("azure_maintenance") leave those databases enumerable and datallowconn, but
// withhold CONNECT even from the master user, so the name-based list in
// base.UserDatabases is backed up by the privilege the engine itself reports —
// a provider naming its next one differently is then still filtered. A database
// this login simply has no rights to disappears along with them, which is what
// the caller wants: it cannot serve as a migration source either.
const pgListDatabasesQuery = `SELECT datname AS datname FROM pg_database ` +
	`WHERE NOT datistemplate AND datallowconn ` +
	`AND has_database_privilege(current_user, datname, 'CONNECT')`

// ListDatabases returns the user databases on loc's server. The query runs
// through the "postgres" maintenance database, which always exists, so
// loc.Database is ignored — on a fresh server it may not exist yet.
func (d *PostgreSQLDriver) ListDatabases(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	logger.Info("postgresql list databases", "access", loc.AccessType)

	var names []string

	if loc.IsDirect() {
		conn, err := d.openServerConn(ctx, loc)
		if err != nil {
			return nil, err
		}
		defer conn.Close(ctx)

		rows, err := conn.Query(ctx, pgListDatabasesQuery)
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
		// A database name may itself contain a tab or a newline, which psql's
		// tab-separated output does not escape, so the rows come back as JSON.
		// A single query, so a per-command connection is all this needs.
		res, err := d.pgServerQueryJSONViaSSH(
			base.NewSSHExec(loc.SSHTunnel.SSH), loc.SSHTunnel, loc.Database, pgListDatabasesQuery)
		if err != nil {
			return nil, fmt.Errorf("list databases via SSH: %w", err)
		}
		for _, row := range res {
			names = append(names, row["datname"])
		}
	}

	return base.UserDatabases(base.DBMSTypePostgreSQL, names), nil
}

// =============================================================================
// ListSchemas / schema resolution
// =============================================================================

// pgListSchemasQuery enumerates the user schemas of a database. The pg_ prefix
// covers pg_catalog, pg_toast and the per-session pg_temp_N / pg_toast_temp_N
// pairs in one predicate; base.UserSchemas applies the same rule again to the
// result, so a name that slips through either path is still dropped. The column
// is aliased because the SSH path wraps this query in json_agg, where the alias
// becomes the JSON key.
const pgListSchemasQuery = `SELECT nspname AS nspname FROM pg_namespace ` +
	`WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'`

// ListSchemas returns the user schemas inside loc.Database, sorted, with
// PostgreSQL's own schemas omitted. loc.PgSchema is ignored — this is the call
// that tells a caller what may go there.
func (d *PostgreSQLDriver) ListSchemas(ctx context.Context, loc base.DBMSLocation) ([]string, error) {
	logger.Info("postgresql list schemas", "database", loc.Database, "access", loc.AccessType)

	if loc.IsDirect() {
		conn, err := d.openConn(ctx, loc)
		if err != nil {
			return nil, err
		}
		defer conn.Close(ctx)
		return d.listSchemasViaSQL(ctx, conn)
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()
	return d.listSchemasViaSSH(exec, loc.SSHTunnel, loc.Database)
}

// listSchemasViaSQL and listSchemasViaSSH take a connection the caller already
// holds. Inspect and DescribeTable resolve the schema selection as their first
// step and then issue many more queries, so opening a second connection just to
// enumerate schemas would double the SSH handshakes an inspection pays for.

func (d *PostgreSQLDriver) listSchemasViaSQL(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	names, err := d.queryNames(ctx, conn, pgListSchemasQuery)
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	return base.UserSchemas(base.DBMSTypePostgreSQL, names), nil
}

func (d *PostgreSQLDriver) listSchemasViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string) ([]string, error) {
	// A schema name may contain a tab or a newline, which psql's tab-separated
	// output does not escape, so the rows come back as JSON.
	res, err := d.pgQueryJSONViaSSH(exec, cfg, database, pgListSchemasQuery)
	if err != nil {
		return nil, fmt.Errorf("list schemas via SSH: %w", err)
	}
	names := make([]string, 0, len(res))
	for _, row := range res {
		names = append(names, row["nspname"])
	}
	return base.UserSchemas(base.DBMSTypePostgreSQL, names), nil
}

// resolvePgSchemas returns the schemas loc covers, given the schemas the
// database actually holds: the names loc.PgSchema lists, in the order it lists
// them, or all of available when it is empty.
//
// The caller's order is preserved rather than sorted, because a destination's
// PgSchema is matched to the source's by position — reordering it here would
// silently repoint a rename. A discovered list arrives already sorted from
// ListSchemas, which makes the empty case deterministic too.
//
// A name that does not exist in the database is an error rather than an empty
// result: a typo in a schema name would otherwise migrate nothing and report
// success.
func resolvePgSchemas(loc base.DBMSLocation, available []string) ([]string, error) {
	if len(loc.PgSchema) == 0 {
		return available, nil
	}

	exists := make(map[string]bool, len(available))
	for _, name := range available {
		exists[name] = true
	}
	for _, name := range loc.PgSchema {
		if !exists[name] {
			return nil, &base.SchemaNotFoundError{
				DBMSType: base.DBMSTypePostgreSQL,
				Database: loc.Database,
				Schema:   name,
			}
		}
	}
	return loc.PgSchema, nil
}

func (d *PostgreSQLDriver) resolvePgSchemasViaSQL(ctx context.Context, conn *pgx.Conn, loc base.DBMSLocation) ([]string, error) {
	available, err := d.listSchemasViaSQL(ctx, conn)
	if err != nil {
		return nil, err
	}
	return resolvePgSchemas(loc, available)
}

func (d *PostgreSQLDriver) resolvePgSchemasViaSSH(exec *base.SSHExec, loc base.DBMSLocation) ([]string, error) {
	available, err := d.listSchemasViaSSH(exec, loc.SSHTunnel, loc.Database)
	if err != nil {
		return nil, err
	}
	return resolvePgSchemas(loc, available)
}

// pgSchemaLiteralList renders schemas as a parenthesised list of SQL string
// literals, for splicing into an IN (…) predicate. An empty list yields a list
// holding one empty string — a name no schema can have — so a predicate built
// from it matches nothing instead of collapsing into invalid syntax.
func pgSchemaLiteralList(schemas []string) string {
	if len(schemas) == 0 {
		return "('')"
	}
	quoted := make([]string, len(schemas))
	for i, s := range schemas {
		quoted[i] = "'" + pgEscape(s) + "'"
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}

// =============================================================================
// CreateDatabase / DropDatabase
// =============================================================================

// pgTerminateBackendsQuery disconnects every other session on a database so it
// can be dropped. It is used instead of DROP DATABASE ... WITH (FORCE), which the
// server only accepts from PostgreSQL 13 on; this query works on every version
// dbmsx supports. pg_backend_pid() keeps it from killing our own session.
const pgTerminateBackendsQuery = `SELECT pg_terminate_backend(pid) FROM pg_stat_activity ` +
	`WHERE datname = $1 AND pid <> pg_backend_pid()`

// CreateDatabase creates loc.Database on loc's server, entering through the
// "postgres" maintenance database — the same entry ListDatabases uses, and the
// only one available while the target does not exist yet.
//
// CREATE DATABASE cannot run inside a transaction block. Both paths here are
// autocommit (a pgx Exec outside an explicit transaction, and psql executing a
// statement from stdin), so no wrapping is added.
func (d *PostgreSQLDriver) CreateDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.CreateDatabaseOption) error {
	logger.Info("postgresql create database", "database", loc.Database, "access", loc.AccessType)

	stmt := pgBuildCreateDatabase(loc.Database, base.PostgreSQLCreateOpt(opt))

	if loc.IsDirect() {
		adminLoc := loc
		adminLoc.Database = pgMaintenanceDB
		conn, err := d.openConn(ctx, adminLoc)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)

		exists, err := pgDatabaseExists(ctx, conn, loc.Database)
		if err != nil {
			return fmt.Errorf("createDatabase pg_database check: %w", err)
		}
		if exists {
			return base.CreateExistsResult(opt, base.DBMSTypePostgreSQL, loc.Database)
		}
		if _, err = conn.Exec(ctx, stmt); err != nil {
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
		return fmt.Errorf("createDatabase pg_database check via SSH: %w", err)
	}
	if exists {
		return base.CreateExistsResult(opt, base.DBMSTypePostgreSQL, loc.Database)
	}
	if _, err = d.pgQueryViaSSH(exec, loc.SSHTunnel, pgMaintenanceDB, stmt); err != nil {
		return fmt.Errorf("createDatabase CREATE DATABASE via SSH: %w", err)
	}
	return nil
}

// DropDatabase drops loc.Database and everything in it, entering through the
// "postgres" maintenance database: PostgreSQL refuses to drop the database the
// session is connected to. As with CREATE, DROP DATABASE cannot run inside a
// transaction block and both paths here are autocommit.
//
// A database with live sessions cannot be dropped at all; opt.PostgreSQL.Force
// terminates those sessions first.
func (d *PostgreSQLDriver) DropDatabase(ctx context.Context, loc base.DBMSLocation, opt *base.DropDatabaseOption) error {
	logger.Info("postgresql drop database", "database", loc.Database, "access", loc.AccessType)

	stmt := "DROP DATABASE " + pgQuoteIdent(loc.Database)
	force := base.PostgreSQLDropOpt(opt).Force

	if loc.IsDirect() {
		adminLoc := loc
		adminLoc.Database = pgMaintenanceDB
		conn, err := d.openConn(ctx, adminLoc)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)

		exists, err := pgDatabaseExists(ctx, conn, loc.Database)
		if err != nil {
			return fmt.Errorf("dropDatabase pg_database check: %w", err)
		}
		if !exists {
			return base.DropMissingResult(opt, base.DBMSTypePostgreSQL, loc.Database)
		}
		// CheckTargetEmpty opens its own connection to loc.Database, as it does
		// for PrepareTarget; the guard is opt-in and runs at most once. It has to
		// run before the backends are terminated, since it is itself a session.
		if base.DropRequiresEmpty(opt) {
			if err = d.CheckTargetEmpty(ctx, loc); err != nil {
				return err
			}
		}
		if force {
			if _, err = conn.Exec(ctx, pgTerminateBackendsQuery, loc.Database); err != nil {
				return fmt.Errorf("dropDatabase terminate backends: %w", err)
			}
		}
		if _, err = conn.Exec(ctx, stmt); err != nil {
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
		return fmt.Errorf("dropDatabase pg_database check via SSH: %w", err)
	}
	if !exists {
		return base.DropMissingResult(opt, base.DBMSTypePostgreSQL, loc.Database)
	}
	if base.DropRequiresEmpty(opt) {
		if err = d.CheckTargetEmpty(ctx, loc); err != nil {
			return err
		}
	}
	if force {
		// psql has no bind parameters, so the name is inlined as a literal.
		terminate := strings.Replace(pgTerminateBackendsQuery, "$1",
			"'"+pgEscape(loc.Database)+"'", 1)
		if _, err = d.pgQueryViaSSH(exec, loc.SSHTunnel, pgMaintenanceDB, terminate); err != nil {
			return fmt.Errorf("dropDatabase terminate backends via SSH: %w", err)
		}
	}
	if _, err = d.pgQueryViaSSH(exec, loc.SSHTunnel, pgMaintenanceDB, stmt); err != nil {
		return fmt.Errorf("dropDatabase DROP DATABASE via SSH: %w", err)
	}
	return nil
}

// pgBuildCreateDatabase renders CREATE DATABASE with its optional clauses. Owner
// and template are quoted identifiers; encoding and the locale names are string
// literals, already constrained to a safe token shape by
// base.ValidateCreateOption — DDL cannot carry any of them as a bound parameter.
func pgBuildCreateDatabase(name string, o base.PostgreSQLCreateOption) string {
	var b strings.Builder
	b.WriteString("CREATE DATABASE ")
	b.WriteString(pgQuoteIdent(name))

	var with []string
	if o.Owner != "" {
		with = append(with, "OWNER "+pgQuoteIdent(o.Owner))
	}
	// TEMPLATE precedes the encoding and locale clauses it makes acceptable.
	if o.Template != "" {
		with = append(with, "TEMPLATE "+pgQuoteIdent(o.Template))
	}
	if o.Encoding != "" {
		with = append(with, "ENCODING '"+o.Encoding+"'")
	}
	if o.LcCollate != "" {
		with = append(with, "LC_COLLATE '"+o.LcCollate+"'")
	}
	if o.LcCtype != "" {
		with = append(with, "LC_CTYPE '"+o.LcCtype+"'")
	}
	// LOCALE sets LC_COLLATE and LC_CTYPE together; ValidateCreateOption has
	// already refused it alongside either of them.
	if o.Locale != "" {
		with = append(with, "LOCALE '"+o.Locale+"'")
	}
	// LOCALE_PROVIDER and ICU_LOCALE are PostgreSQL 15 and later. An older
	// server rejects the clause outright rather than ignoring it, which is the
	// right outcome: a database created under libc rules when icu was asked for
	// would sort differently and say nothing about it.
	if o.LocaleProvider != "" {
		with = append(with, "LOCALE_PROVIDER '"+o.LocaleProvider+"'")
	}
	if o.IcuLocale != "" {
		with = append(with, "ICU_LOCALE '"+o.IcuLocale+"'")
	}
	if len(with) > 0 {
		b.WriteString(" WITH ")
		b.WriteString(strings.Join(with, " "))
	}
	return b.String()
}

// =============================================================================
// Inspect
// =============================================================================

// pgDatabaseCharsetQuery reads the encoding and the two locale settings of the
// database the connection is already on, which is why it asks about
// current_database() rather than taking a name: both paths reach it holding a
// connection to loc.Database.
//
// Every column is aliased for the SSH path's json_agg, as the other shared
// queries are. The encoding is reported as its name rather than its numeric id,
// so the value matches what CREATE DATABASE ... ENCODING accepts.
const pgDatabaseCharsetQuery = `SELECT pg_encoding_to_char(encoding) AS character_set,
		datcollate AS collation,
		datctype   AS ctype
	FROM pg_database WHERE datname = current_database()`

// Inspect returns server-level and schema-level metadata for the database at loc.
func (d *PostgreSQLDriver) Inspect(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	if loc.IsDirect() {
		return d.inspectViaSQL(ctx, loc)
	}
	return d.inspectViaSSH(ctx, loc)
}

func (d *PostgreSQLDriver) inspectViaSQL(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := pgMetric(loc)
	info := &base.DBMSInfo{Database: loc.Database}

	var err error
	if info.ServerVersion, err = d.serverVersionViaSQL(ctx, loc); err != nil {
		return nil, err
	}

	// Table/size details require loc.Database itself to exist. When it
	// doesn't yet (the common case before a first migration), return the
	// version-only info instead of failing outright.
	conn, err := d.openConn(ctx, loc)
	if err != nil {
		return info, nil
	}
	defer conn.Close(ctx)

	// Encoding and locale, read through the connection once loc.Database is
	// known to exist. It has to sit below the early return above: the query is
	// about the database being inspected, so there is nothing to ask before one
	// is there.
	if err := conn.QueryRow(ctx, pgDatabaseCharsetQuery).Scan(
		&info.CharacterSet, &info.Collation, &info.Ctype); err != nil {
		return nil, fmt.Errorf("read database encoding: %w", err)
	}

	schemas, err := d.resolvePgSchemasViaSQL(ctx, conn, loc)
	if err != nil {
		return nil, err
	}

	// Per-schema size rollup. The four database-wide figures are summed from
	// this rather than queried separately, so the two can never disagree.
	statRows, err := d.pgSchemaStatsViaSQL(ctx, conn, schemas)
	if err != nil {
		return nil, err
	}
	info.PgSchema = statRows
	for _, s := range statRows {
		info.TotalSize += s.TotalSize
		info.DataSize += s.DataSize
		info.IndexSize += s.IndexSize
		info.TableCount += s.TableCount
	}

	// Table list with planner statistics row estimates (always collected).
	rows, err := conn.Query(ctx,
		`SELECT n.nspname,
			c.relname,
			c.reltuples::bigint,
			pg_total_relation_size(c.oid),
			pg_relation_size(c.oid),
			pg_indexes_size(c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN `+pgSchemaLiteralList(schemas)+` AND c.relkind='r'
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t base.TableInfo
		if err := rows.Scan(&t.PgSchema, &t.Name, &t.RowCount, &t.TotalSize, &t.DataSize, &t.IndexSize); err != nil {
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
				Kind: base.ObjectKindTable, Owner: t.PgSchema, Name: t.Name,
				Index: i + 1, Total: len(info.Tables),
			}
			if m.rowCountExact {
				// pg_class.reltuples is a planner statistics estimate (refreshed
				// by ANALYZE/autovacuum) that can read 0 or stale right after a
				// bulk restore; use an exact COUNT(*) when accuracy is requested.
				if err := conn.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s",
					pgQualifyIdent(t.PgSchema, t.Name))).Scan(&t.RowCount); err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
			}
			if m.columns || m.indexes {
				ti, err := d.describeTableViaSQL(ctx, conn, t.PgSchema, t.Name)
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

	// Schema objects across the selected schemas (opt-in).
	if m.needSchemaScan() {
		for _, item := range pgSchemaObjectFields(m, info, schemas) {
			names, err := d.queryNames(ctx, conn, item.query)
			if err != nil {
				return nil, err
			}
			*item.dst = names
		}
	}
	return info, nil
}

// pgSchemaStatsQuery aggregates table sizes per schema. GROUP BY emits no row
// for a schema holding no tables, so the caller fills the gaps — see
// pgFillSchemaStats. Every column is aliased for the SSH path's json_agg.
func pgSchemaStatsQuery(schemas []string) string {
	return `SELECT n.nspname AS name,
		COALESCE(SUM(pg_total_relation_size(c.oid)),0) AS total_size,
		COALESCE(SUM(pg_relation_size(c.oid)),0) AS data_size,
		COALESCE(SUM(pg_indexes_size(c.oid)),0) AS index_size,
		COUNT(*) AS table_count
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) + ` AND c.relkind='r'
	GROUP BY n.nspname
	ORDER BY n.nspname`
}

// pgFillSchemaStats returns one entry per schema in schemas, in that order,
// taking the figures from found where the schema appeared there and leaving
// them at zero where it did not.
//
// The zero entries are the point: a schema the caller selected — or one the
// database simply holds — must show up in the result even when it is empty,
// which is exactly the state a freshly created target is in. Deriving the list
// from the resolved selection rather than from the query result also keeps the
// order stable and makes the totals a sum over a known set.
func pgFillSchemaStats(schemas []string, found map[string]base.PgSchemaInfo) []base.PgSchemaInfo {
	out := make([]base.PgSchemaInfo, 0, len(schemas))
	for _, name := range schemas {
		if stat, ok := found[name]; ok {
			out = append(out, stat)
			continue
		}
		out = append(out, base.PgSchemaInfo{Name: name})
	}
	return out
}

func (d *PostgreSQLDriver) pgSchemaStatsViaSQL(ctx context.Context, conn *pgx.Conn, schemas []string) ([]base.PgSchemaInfo, error) {
	rows, err := conn.Query(ctx, pgSchemaStatsQuery(schemas))
	if err != nil {
		return nil, fmt.Errorf("schema statistics: %w", err)
	}
	defer rows.Close()

	found := make(map[string]base.PgSchemaInfo, len(schemas))
	for rows.Next() {
		var s base.PgSchemaInfo
		if err := rows.Scan(&s.Name, &s.TotalSize, &s.DataSize, &s.IndexSize, &s.TableCount); err != nil {
			return nil, err
		}
		found[s.Name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pgFillSchemaStats(schemas, found), nil
}

// pgSchemaObjectField pairs a schema-object enumeration query with the DBMSInfo
// slice it populates.
type pgSchemaObjectField struct {
	query string
	dst   *[]string
}

// pgObjectQuery* build the statements that enumerate schema-object names across
// the schemas an inspection covers. Both the direct and SSH paths run them
// unchanged; the single output column is aliased "name" because the SSH path
// wraps the query in json_agg, where the alias becomes the JSON key.
//
// Every name comes back schema-qualified and quoted by the server —
// quote_ident(nspname) || '.' || quote_ident(objname) — which makes each entry
// unambiguous when two schemas hold an object of the same name, and leaves it
// directly usable as a DROP target. buildTeardownScript relies on that: it emits
// these strings verbatim rather than quoting them again. quote_ident adds the
// double quotes only where the engine needs them, so an ordinary lower-case name
// still reads as sales.v_monthly rather than "sales"."v_monthly".
//
// Ordering is by the qualified name, which groups the result by schema.

// pgQualifiedName renders the SELECT expression for a schema-qualified object
// name, given the SQL expressions for its namespace and its own name.
func pgQualifiedName(nsExpr, nameExpr string) string {
	return "quote_ident(" + nsExpr + ") || '.' || quote_ident(" + nameExpr + ")"
}

func pgObjectQueryForeignKeys(schemas []string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "con.conname") + ` AS name ` +
		`FROM pg_constraint con JOIN pg_namespace n ON n.oid=con.connamespace ` +
		`WHERE con.contype='f' AND n.nspname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgObjectQueryViews(schemas []string) string {
	return `SELECT ` + pgQualifiedName("table_schema", "table_name") + ` AS name ` +
		`FROM information_schema.views ` +
		`WHERE table_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgObjectQueryMatViews(schemas []string) string {
	return `SELECT ` + pgQualifiedName("schemaname", "matviewname") + ` AS name ` +
		`FROM pg_matviews ` +
		`WHERE schemaname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

// pgObjectQueryRoutines enumerates functions (prokind 'f') or procedures ('p')
// with their full identity signature (e.g. sales.user_count(integer, text)) so
// overloaded routines stay distinct and can be dropped unambiguously. The
// argument type list is left bare — it is a type list, not an identifier.
func pgObjectQueryRoutines(schemas []string, prokind string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "p.proname") +
		` || '(' || pg_get_function_identity_arguments(p.oid) || ')' AS name ` +
		`FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace ` +
		`WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) +
		` AND p.prokind='` + prokind + `' ORDER BY 1`
}

func pgObjectQueryTriggers(schemas []string) string {
	return `SELECT DISTINCT ` + pgQualifiedName("trigger_schema", "trigger_name") + ` AS name ` +
		`FROM information_schema.triggers ` +
		`WHERE trigger_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgObjectQuerySequences(schemas []string) string {
	return `SELECT ` + pgQualifiedName("sequence_schema", "sequence_name") + ` AS name ` +
		`FROM information_schema.sequences ` +
		`WHERE sequence_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgObjectQueryTypes(schemas []string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "t.typname") + ` AS name ` +
		`FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace ` +
		`WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) +
		` AND (t.typtype IN ('e','d') OR (t.typtype='c' AND t.typrelid IN ` +
		`(SELECT oid FROM pg_class WHERE relkind='c'))) ORDER BY 1`
}

func pgObjectQueryRules(schemas []string) string {
	return `SELECT ` + pgQualifiedName("schemaname", "rulename") + ` AS name ` +
		`FROM pg_rules ` +
		`WHERE schemaname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

// pgObjectQueryExtensions is the one inventory that is not schema-qualified and
// not schema-filtered: an extension is installed once per database, DROP
// EXTENSION takes an unqualified name, and the dump has to recreate every
// extension the selected schemas might depend on — including one installed
// elsewhere in the database.
const pgObjectQueryExtensions = `SELECT quote_ident(extname) AS name FROM pg_extension ` +
	`WHERE extname <> 'plpgsql' ORDER BY 1`

// pgSchemaObjectFields returns the (query, destination) pairs for every schema
// object requested by m, bound to info's fields and scoped to schemas.
func pgSchemaObjectFields(m pgInspectMetric, info *base.DBMSInfo, schemas []string) []pgSchemaObjectField {
	var fields []pgSchemaObjectField
	add := func(want bool, query string, dst *[]string) {
		if want {
			fields = append(fields, pgSchemaObjectField{query: query, dst: dst})
		}
	}
	add(m.foreignKeys, pgObjectQueryForeignKeys(schemas), &info.ForeignKeys)
	add(m.views, pgObjectQueryViews(schemas), &info.Views)
	add(m.materializedViews, pgObjectQueryMatViews(schemas), &info.MaterializedViews)
	add(m.functions, pgObjectQueryRoutines(schemas, "f"), &info.Functions)
	add(m.procedures, pgObjectQueryRoutines(schemas, "p"), &info.Procedures)
	add(m.triggers, pgObjectQueryTriggers(schemas), &info.Triggers)
	add(m.sequences, pgObjectQuerySequences(schemas), &info.Sequences)
	add(m.types, pgObjectQueryTypes(schemas), &info.Types)
	add(m.extensions, pgObjectQueryExtensions, &info.Extensions)
	add(m.rules, pgObjectQueryRules(schemas), &info.Rules)
	return fields
}

// queryNames runs a single-column query and returns the string values.
func (d *PostgreSQLDriver) queryNames(ctx context.Context, conn *pgx.Conn, query string) ([]string, error) {
	rows, err := conn.Query(ctx, query)
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

func (d *PostgreSQLDriver) inspectViaSSH(ctx context.Context, loc base.DBMSLocation) (*base.DBMSInfo, error) {
	m := pgMetric(loc)
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

	if info.ServerVersion, err = d.serverVersionViaSSH(exec, cfg, loc.Database); err != nil {
		return nil, err
	}

	// Table/size details require loc.Database itself to exist. When it
	// doesn't yet (the common case before a first migration), return the
	// version-only info instead of failing outright — which is also why the
	// schema resolution below is allowed to fail quietly here.
	schemas, err := d.resolvePgSchemasViaSSH(exec, loc)
	if err != nil {
		if _, missing := err.(*base.SchemaNotFoundError); missing {
			return nil, err
		}
		return info, nil
	}

	// Encoding and locale. Resolving the schemas above already established that
	// loc.Database exists. A failure here leaves the three fields empty rather
	// than failing the call, which is how the size and table queries below treat
	// their own failures on this path.
	if charsetRes, cerr := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, pgDatabaseCharsetQuery); cerr == nil &&
		len(charsetRes) > 0 {
		info.CharacterSet = charsetRes[0]["character_set"]
		info.Collation = charsetRes[0]["collation"]
		info.Ctype = charsetRes[0]["ctype"]
	}

	// Per-schema size rollup; the database-wide figures are its sum.
	statRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, pgSchemaStatsQuery(schemas))
	if err != nil {
		return info, nil
	}
	found := make(map[string]base.PgSchemaInfo, len(statRes))
	for _, row := range statRes {
		s := base.PgSchemaInfo{Name: row["name"]}
		s.TotalSize, _ = strconv.ParseInt(row["total_size"], 10, 64)
		s.DataSize, _ = strconv.ParseInt(row["data_size"], 10, 64)
		s.IndexSize, _ = strconv.ParseInt(row["index_size"], 10, 64)
		s.TableCount, _ = strconv.Atoi(row["table_count"])
		found[s.Name] = s
	}
	info.PgSchema = pgFillSchemaStats(schemas, found)
	for _, s := range info.PgSchema {
		info.TotalSize += s.TotalSize
		info.DataSize += s.DataSize
		info.IndexSize += s.IndexSize
		info.TableCount += s.TableCount
	}

	// The sort key is the qualified name so the table list comes back grouped by
	// schema, matching the direct path's ORDER BY n.nspname, c.relname.
	tableQ := `SELECT n.nspname AS pg_schema,
		c.relname AS name,
		quote_ident(n.nspname) || '.' || quote_ident(c.relname) AS qualified_name,
		c.reltuples::bigint AS row_count,
		pg_total_relation_size(c.oid) AS total_size,
		pg_relation_size(c.oid) AS data_size,
		pg_indexes_size(c.oid) AS index_size
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) + ` AND c.relkind='r'
	ORDER BY 3`
	tableRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, tableQ)
	if err != nil {
		return info, nil
	}
	pgSortRows(tableRes, "qualified_name")
	for _, row := range tableRes {
		t := base.TableInfo{PgSchema: row["pg_schema"], Name: row["name"]}
		t.RowCount, _ = strconv.ParseInt(row["row_count"], 10, 64)
		t.TotalSize, _ = strconv.ParseInt(row["total_size"], 10, 64)
		t.DataSize, _ = strconv.ParseInt(row["data_size"], 10, 64)
		t.IndexSize, _ = strconv.ParseInt(row["index_size"], 10, 64)
		info.Tables = append(info.Tables, t)
	}

	// Per-table detail (opt-in).
	if m.needPerTableScan() {
		for i := range info.Tables {
			t := &info.Tables[i]
			qualified := pgQualifyIdent(t.PgSchema, t.Name)
			obj := base.Obj{
				Kind: base.ObjectKindTable, Owner: t.PgSchema, Name: t.Name,
				Index: i + 1, Total: len(info.Tables),
			}
			if m.rowCountExact {
				// pg_class.reltuples is a planner statistics estimate (refreshed
				// by ANALYZE/autovacuum) that can read 0 or stale right after a
				// bulk restore; use an exact COUNT(*) when accuracy is requested.
				countQ := fmt.Sprintf("SELECT COUNT(*) AS row_count FROM %s", qualified)
				countRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, countQ)
				if err != nil {
					return nil, obj.Fail(base.ActionInspect, err)
				}
				if len(countRes) > 0 {
					if n, perr := strconv.ParseInt(countRes[0]["row_count"], 10, 64); perr == nil {
						t.RowCount = n
					}
				}
			}
			if m.columns || m.indexes {
				ti, err := d.describeTableViaSSH(ctx, exec, loc, t.PgSchema, t.Name)
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

	// Schema objects across the selected schemas (opt-in).
	if m.needSchemaScan() {
		for _, item := range pgSchemaObjectFields(m, info, schemas) {
			rows, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, item.query)
			if err != nil {
				return nil, err
			}
			pgSortRows(rows, "name")
			names := make([]string, 0, len(rows))
			for _, row := range rows {
				names = append(names, row["name"])
			}
			*item.dst = names
		}
	}
	return info, nil
}

// =============================================================================
// CharsetProfile
// =============================================================================

// pgUsedCollationQuery lists every column that states a collation of its own,
// tagged with the table and schema holding it so loc.Filter can be applied
// before the names are collected.
//
// Only explicit collations are listed. attcollation is 0 on a column of a
// non-collatable type, and "default" names the database's collation rather than
// one the DDL will carry — that one is already reported as the profile's
// Collation.
const pgUsedCollationQueryBody = `SELECT c.relname AS table_name,
		a.attname   AS column_name,
		co.collname AS collation
	FROM pg_attribute a
	JOIN pg_class     c  ON c.oid = a.attrelid
	JOIN pg_namespace n  ON n.oid = c.relnamespace
	JOIN pg_collation co ON co.oid = a.attcollation
	WHERE c.relkind='r' AND a.attnum > 0 AND NOT a.attisdropped
	  AND a.attcollation <> 0 AND co.collname <> 'default'
	  AND n.nspname IN `

// pgAvailableCollationQuery enumerates the collations the server recognises.
// Unlike MySQL's, this list is not fixed by the server version: a libc collation
// exists only if the host has that locale installed, so the same PostgreSQL
// release answers differently on a full distribution and on a slim container.
// That is exactly why the question is asked of the server rather than inferred.
const pgAvailableCollationQuery = `SELECT collname AS name FROM pg_collation`

// =============================================================================
// ServerVersion
// =============================================================================

// ServerVersion returns what SHOW server_version reports. See base.DBDriver.
//
// Both paths enter through the "postgres" maintenance database rather than
// loc.Database, because PostgreSQL has no server-level connection — every
// session selects a database — and the version has to be readable on a target
// whose database does not exist yet.
//
// SHOW server_version yields "14.23 (Ubuntu 14.23-1.pgdg22.04+1)", whose leading
// numeric token base.ParseServerVersion can read. SELECT version() is not used:
// it returns a descriptive string starting with a non-numeric word ("PostgreSQL
// 14.23 (Ubuntu ...) on x86_64..."), which would make every comparison skip.
func (d *PostgreSQLDriver) ServerVersion(ctx context.Context, loc base.DBMSLocation) (string, error) {
	if loc.IsDirect() {
		return d.serverVersionViaSQL(ctx, loc)
	}

	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return "", err
	}
	defer exec.Close()
	return d.serverVersionViaSSH(exec, loc.SSHTunnel, loc.Database)
}

// serverVersionViaSQL opens its own connection rather than taking one the caller
// holds, unlike its counterparts on the other drivers: the maintenance database
// it needs is not the one an inspection is otherwise connected to.
func (d *PostgreSQLDriver) serverVersionViaSQL(ctx context.Context, loc base.DBMSLocation) (string, error) {
	conn, err := d.openServerConn(ctx, loc)
	if err != nil {
		return "", fmt.Errorf("open PostgreSQL connection for version check: %w", err)
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		return "", fmt.Errorf("SHOW server_version: %w", err)
	}
	return strings.TrimSpace(version), nil
}

// serverVersionViaSSH takes fallbackDB - the caller's own database - because
// SHOW server_version is a server property that any connectable database answers,
// and pgMaintenanceDB is not reachable on every managed service. See the note on
// pgMaintenanceDB.
func (d *PostgreSQLDriver) serverVersionViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig,
	fallbackDB string) (string, error) {
	res, err := d.pgServerQueryViaSSH(exec, cfg, fallbackDB, "SHOW server_version")
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

// CharsetProfile reports the database's encoding and locale, the collations its
// columns name explicitly, and the collations its server recognises.
//
// PostgreSQL has no per-object character sets — the database encoding is the
// whole story — so the charset lists stay empty and the encoding is compared
// on its own.
func (d *PostgreSQLDriver) CharsetProfile(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	if loc.IsDirect() {
		return d.charsetProfileViaSQL(ctx, loc)
	}
	return d.charsetProfileViaSSH(ctx, loc)
}

func (d *PostgreSQLDriver) charsetProfileViaSQL(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	conn, err := d.openConn(ctx, loc)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile: open connection: %w", err)
	}
	defer conn.Close(ctx)

	p := &base.CharsetProfile{}
	if err := conn.QueryRow(ctx, pgDatabaseCharsetQuery).Scan(&p.Encoding, &p.Collation, &p.Ctype); err != nil {
		return nil, fmt.Errorf("charsetProfile database encoding: %w", err)
	}

	schemas, err := d.resolvePgSchemasViaSQL(ctx, conn, loc)
	if err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, pgUsedCollationQueryBody+pgSchemaLiteralList(schemas))
	if err != nil {
		return nil, fmt.Errorf("charsetProfile used collations: %w", err)
	}
	var used [][]string
	for rows.Next() {
		r := make([]string, 3)
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			rows.Close()
			return nil, fmt.Errorf("charsetProfile used collations: %w", err)
		}
		used = append(used, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("charsetProfile used collations: %w", err)
	}
	pgCollectUsed(p, used, loc.Filter)

	if p.AvailableCollations, err = d.queryNames(ctx, conn, pgAvailableCollationQuery); err != nil {
		return nil, fmt.Errorf("charsetProfile available collations: %w", err)
	}
	p.AvailableCollations = base.SortUnique(p.AvailableCollations)
	return p, nil
}

func (d *PostgreSQLDriver) charsetProfileViaSSH(ctx context.Context, loc base.DBMSLocation) (*base.CharsetProfile, error) {
	cfg := loc.SSHTunnel
	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()

	p := &base.CharsetProfile{}
	res, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, pgDatabaseCharsetQuery)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile database encoding via SSH: %w", err)
	}
	if len(res) > 0 {
		p.Encoding = res[0]["character_set"]
		p.Collation = res[0]["collation"]
		p.Ctype = res[0]["ctype"]
	}

	schemas, err := d.resolvePgSchemasViaSSH(exec, loc)
	if err != nil {
		return nil, err
	}

	usedRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database,
		pgUsedCollationQueryBody+pgSchemaLiteralList(schemas))
	if err != nil {
		return nil, fmt.Errorf("charsetProfile used collations via SSH: %w", err)
	}
	used := make([][]string, 0, len(usedRes))
	for _, row := range usedRes {
		used = append(used, []string{row["table_name"], row["column_name"], row["collation"]})
	}
	pgCollectUsed(p, used, loc.Filter)

	availRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, pgAvailableCollationQuery)
	if err != nil {
		return nil, fmt.Errorf("charsetProfile available collations via SSH: %w", err)
	}
	names := make([]string, 0, len(availRes))
	for _, row := range availRes {
		names = append(names, row["name"])
	}
	p.AvailableCollations = base.SortUnique(names)
	return p, nil
}

// pgCollectUsed fills p's used collations from the (table, column, collation)
// rows of pgUsedCollationQueryBody, dropping what the filter removes from the
// dump. A collation kept only by an object the migration will not carry must not
// block it.
func pgCollectUsed(p *base.CharsetProfile, rows [][]string, filter *filterpkg.DBMSFilterOption) {
	var collations []string
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		table, column := r[0], r[1]
		if filter.IsTableExcluded(table) {
			continue
		}
		if filter.ExcludedColumns(table)[column] {
			continue
		}
		collations = append(collations, r[2])
	}
	p.UsedCollations = base.SortUnique(collations)
}

// =============================================================================
// DescribeTable
// =============================================================================

// pgColumnQuery and pgIndexQuery are shared by the direct and SSH paths so both
// return identical per-table detail; each caller appends its own table predicate
// and ordering. Every output column is aliased because the SSH path feeds the
// query through json_agg, where the aliases become the JSON object's keys.
//
// Both are scoped to a single schema, since that is what identifies a table:
// with several schemas in play, a predicate on the table name alone would pull
// in a same-named table from every one of them.
//
// The primary-key flag is resolved by a correlated EXISTS rather than a join on
// key_column_usage: joining would emit one row per constraint a column
// participates in, duplicating columns that sit in a primary key and a unique
// index at once. The pg_class join is likewise constrained by namespace, so a
// same-named table in another schema cannot attach its comments here.
func pgColumnQuery(schema string) string {
	return pgColumnQueryBody + `='` + pgEscape(schema) + `'`
}

func pgIndexQuery(schema string) string {
	return pgIndexQueryBody + `='` + pgEscape(schema) + `' AND NOT ix.indisprimary`
}

const pgColumnQueryBody = `SELECT
		c.column_name AS column_name,
		c.udt_name AS data_type,
		c.is_nullable AS is_nullable,
		COALESCE(c.column_default,'') AS column_default,
		EXISTS(
			SELECT 1 FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage k2
				ON k2.constraint_schema = tc.constraint_schema
				AND k2.constraint_name = tc.constraint_name
				AND k2.table_schema = tc.table_schema
				AND k2.table_name = tc.table_name
			WHERE tc.table_schema = c.table_schema
			  AND tc.table_name = c.table_name
			  AND k2.column_name = c.column_name
			  AND tc.constraint_type = 'PRIMARY KEY'
		) AS is_primary,
		COALESCE(pgd.description, '') AS comment,
		COALESCE(c.collation_name,'') AS collation,
		c.ordinal_position::int AS ordinal_position
	FROM information_schema.columns c
	LEFT JOIN pg_namespace pgn ON pgn.nspname = c.table_schema
	LEFT JOIN pg_class pgc ON pgc.relname = c.table_name AND pgc.relnamespace = pgn.oid
	LEFT JOIN pg_description pgd ON pgd.objoid = pgc.oid
		AND pgd.objsubid = c.ordinal_position::int
	WHERE c.table_schema`

const pgIndexQueryBody = `SELECT
		i.relname AS index_name,
		ix.indisunique AS is_unique,
		am.amname AS index_type,
		array_to_string(ARRAY(
			SELECT a.attname
			FROM pg_attribute a
			WHERE a.attrelid = t.oid
			  AND a.attnum = ANY(ix.indkey)
			ORDER BY array_position(ix.indkey, a.attnum)
		), ',') AS index_columns
	FROM pg_class t
	JOIN pg_index ix ON t.oid = ix.indrelid
	JOIN pg_class i ON i.oid = ix.indexrelid
	JOIN pg_am am ON am.oid = i.relam
	JOIN pg_namespace n ON n.oid = t.relnamespace
	WHERE n.nspname`

// DescribeTable returns column and index metadata for a single table.
//
// table is a bare table name, so the schema holding it comes from loc: the one
// schema loc.PgSchema names, or — when it names none — the one schema of
// loc.Database that actually holds a table by this name. A name present in
// several of them is an error rather than an arbitrary pick; narrowing
// loc.PgSchema resolves it.
func (d *PostgreSQLDriver) DescribeTable(ctx context.Context, loc base.DBMSLocation, table string) (*base.TableInfo, error) {
	if loc.IsDirect() {
		conn, err := d.openConn(ctx, loc)
		if err != nil {
			return nil, err
		}
		defer conn.Close(ctx)

		schema, err := d.tableSchemaViaSQL(ctx, conn, loc, table)
		if err != nil {
			return nil, err
		}
		return d.describeTableViaSQL(ctx, conn, schema, table)
	}
	exec, err := base.DialSSHExec(loc.SSHTunnel.SSH)
	if err != nil {
		return nil, err
	}
	defer exec.Close()

	schema, err := d.tableSchemaViaSSH(ctx, exec, loc, table)
	if err != nil {
		return nil, err
	}
	return d.describeTableViaSSH(ctx, exec, loc, schema, table)
}

// pgTableSchemaQuery finds which of the covered schemas hold a table by this
// name. It returns every match rather than the first, so an ambiguous name can
// be reported as one.
func pgTableSchemaQuery(schemas []string, table string) string {
	return `SELECT table_schema AS table_schema FROM information_schema.tables ` +
		`WHERE table_schema IN ` + pgSchemaLiteralList(schemas) +
		` AND table_name='` + pgEscape(table) + `' AND table_type='BASE TABLE' ORDER BY 1`
}

// pgResolveTableSchema turns the matches of pgTableSchemaQuery into the single
// schema DescribeTable should read, or the error explaining why there is none.
func pgResolveTableSchema(database, table string, matches []string) (string, error) {
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("table %q does not exist in the selected schemas of database %q", table, database)
	default:
		return "", fmt.Errorf(
			"table %q exists in more than one schema of database %q (%s): "+
				"set pgSchema to the one to describe",
			table, database, strings.Join(matches, ", "))
	}
}

func (d *PostgreSQLDriver) tableSchemaViaSQL(ctx context.Context, conn *pgx.Conn, loc base.DBMSLocation, table string) (string, error) {
	schemas, err := d.resolvePgSchemasViaSQL(ctx, conn, loc)
	if err != nil {
		return "", err
	}
	matches, err := d.queryNames(ctx, conn, pgTableSchemaQuery(schemas, table))
	if err != nil {
		return "", err
	}
	return pgResolveTableSchema(loc.Database, table, matches)
}

func (d *PostgreSQLDriver) tableSchemaViaSSH(ctx context.Context, exec *base.SSHExec, loc base.DBMSLocation, table string) (string, error) {
	schemas, err := d.resolvePgSchemasViaSSH(exec, loc)
	if err != nil {
		return "", err
	}
	rows, err := d.pgQueryJSONViaSSH(exec, loc.SSHTunnel, loc.Database, pgTableSchemaQuery(schemas, table))
	if err != nil {
		return "", err
	}
	pgSortRows(rows, "table_schema")
	matches := make([]string, 0, len(rows))
	for _, row := range rows {
		matches = append(matches, row["table_schema"])
	}
	return pgResolveTableSchema(loc.Database, table, matches)
}

func (d *PostgreSQLDriver) describeTableViaSQL(ctx context.Context, conn *pgx.Conn, schema, table string) (*base.TableInfo, error) {
	info := &base.TableInfo{PgSchema: schema, Name: table}

	colRows, err := conn.Query(ctx, pgColumnQuery(schema)+" AND c.table_name=$1 ORDER BY c.ordinal_position", table)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()

	for colRows.Next() {
		var col base.ColumnInfo
		var nullable string
		var ordinal int
		// PostgreSQL has no per-column character set — the database encoding is
		// the whole story — so only Collation is read, and only for columns that
		// declare one; an inherited default reports nothing.
		if err := colRows.Scan(&col.Name, &col.DataType, &nullable, &col.Default, &col.IsPrimary,
			&col.Comment, &col.Collation, &ordinal); err != nil {
			return nil, err
		}
		col.IsNullable = strings.EqualFold(nullable, "YES")
		info.Columns = append(info.Columns, col)
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	idxRows, err := conn.Query(ctx, pgIndexQuery(schema)+" AND t.relname=$1 ORDER BY i.relname", table)
	if err != nil {
		return nil, err
	}
	defer idxRows.Close()

	for idxRows.Next() {
		var idx base.IndexInfo
		var colList string
		if err := idxRows.Scan(&idx.Name, &idx.IsUnique, &idx.Type, &colList); err != nil {
			return nil, err
		}
		if colList != "" {
			idx.Columns = strings.Split(colList, ",")
		}
		info.Indexes = append(info.Indexes, idx)
	}
	return info, idxRows.Err()
}

func (d *PostgreSQLDriver) describeTableViaSSH(ctx context.Context, exec *base.SSHExec, loc base.DBMSLocation, schema, table string) (*base.TableInfo, error) {
	cfg := loc.SSHTunnel
	info := &base.TableInfo{PgSchema: schema, Name: table}

	// Same queries the direct path runs, so isPrimary, the column comment and
	// each index's column list are all populated here too. Ordering is restored
	// in Go because json_agg does not promise to keep the subquery's ORDER BY.
	colQ := fmt.Sprintf("%s AND c.table_name='%s'", pgColumnQuery(schema), pgEscape(table))
	res, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, colQ)
	if err != nil {
		return nil, err
	}
	pgSortRows(res, "ordinal_position")
	for _, row := range res {
		info.Columns = append(info.Columns, base.ColumnInfo{
			Name:       row["column_name"],
			DataType:   row["data_type"],
			IsNullable: strings.EqualFold(strings.TrimSpace(row["is_nullable"]), "YES"),
			Default:    row["column_default"],
			IsPrimary:  pgTruthy(row["is_primary"]),
			Comment:    row["comment"],
			Collation:  row["collation"],
		})
	}

	idxQ := fmt.Sprintf("%s AND t.relname='%s'", pgIndexQuery(schema), pgEscape(table))
	idxRes, err := d.pgQueryJSONViaSSH(exec, cfg, loc.Database, idxQ)
	if err != nil {
		return nil, err
	}
	pgSortRows(idxRes, "index_name")
	for _, row := range idxRes {
		idx := base.IndexInfo{
			Name:     row["index_name"],
			IsUnique: pgTruthy(row["is_unique"]),
			// amname is kept verbatim ("btree", "gin", …) to match the direct path.
			Type: row["index_type"],
		}
		if cols := row["index_columns"]; cols != "" {
			idx.Columns = strings.Split(cols, ",")
		}
		info.Indexes = append(info.Indexes, idx)
	}
	return info, nil
}

// =============================================================================
// Internal Helpers — Connection
// =============================================================================

// buildDSN constructs a libpq-style DSN for the given direct-mode location.
//
// The returned cleanup releases what the DSN points at and is never nil: a CA
// supplied as PEM text has to reach libpq as a file, since sslrootcert takes a
// path. Call it once the connection attempt is over — libpq reads the file
// during the handshake and not after.
func (d *PostgreSQLDriver) buildDSN(loc base.DBMSLocation) (dsn string, cleanup func(), err error) {
	noop := func() {}
	cfg := loc.Direct
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 30
	}

	// The mode names pass through unchanged: this vocabulary is libpq's own,
	// and the other drivers translate into it rather than the other way round.
	settings, err := base.ResolveTLS(cfg)
	if err != nil {
		return "", noop, err
	}
	sslmode, sslrootcert, cleanup, err := settings.PgParams()
	if err != nil {
		return "", noop, err
	}

	dsn = fmt.Sprintf("host=%s port=%d dbname=%s sslmode=%s connect_timeout=%d",
		cfg.Host, port, loc.Database, sslmode, timeout)
	if sslrootcert != "" {
		dsn += " sslrootcert=" + sslrootcert
	}
	if cfg.Username != "" {
		dsn += " user=" + cfg.Username
	}
	if cfg.Password != "" {
		dsn += " password=" + cfg.Password
	}
	return dsn, cleanup, nil
}

// openConn opens a *pgx.Conn for the given direct-mode location.
func (d *PostgreSQLDriver) openConn(ctx context.Context, loc base.DBMSLocation) (*pgx.Conn, error) {
	dsn, cleanup, err := d.buildDSN(loc)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL connection: %w", err)
	}
	defer cleanup()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		err = base.ExplainTLSError(err, base.TLSModeOf(loc.Direct), loc.Direct.Host)
		return nil, fmt.Errorf("open PostgreSQL connection: %w", err)
	}
	return conn, nil
}

// openServerConn opens a connection for a server-level READ - a cluster-wide
// catalog query or SHOW. It prefers pgMaintenanceDB, which exists on a stock
// server and stays reachable while the target database is being created or
// dropped, and falls back to loc.Database when that database refuses this login.
//
// See the note on pgMaintenanceDB for why the fallback exists. When both fail the
// maintenance error is returned, since that is the one that names the cause.
func (d *PostgreSQLDriver) openServerConn(ctx context.Context, loc base.DBMSLocation) (*pgx.Conn, error) {
	serverLoc := loc
	serverLoc.Database = pgMaintenanceDB
	conn, err := d.openConn(ctx, serverLoc)
	if err == nil {
		return conn, nil
	}
	if loc.Database == "" || loc.Database == pgMaintenanceDB {
		return nil, err
	}
	fallback, ferr := d.openConn(ctx, loc)
	if ferr != nil {
		return nil, err
	}
	logger.Info("postgresql server-level query fell back to the connection's own database",
		"maintenance", pgMaintenanceDB, "database", loc.Database)
	return fallback, nil
}

// pgServerQueryViaSSH is openServerConn's tunnelled counterpart: the same query
// through pgMaintenanceDB, retried through fallbackDB when that database refuses
// this login.
func (d *PostgreSQLDriver) pgServerQueryViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig,
	fallbackDB, query string) ([][]string, error) {
	res, err := d.pgQueryViaSSH(exec, cfg, pgMaintenanceDB, query)
	if err == nil {
		return res, nil
	}
	if fallbackDB == "" || fallbackDB == pgMaintenanceDB {
		return nil, err
	}
	fallback, ferr := d.pgQueryViaSSH(exec, cfg, fallbackDB, query)
	if ferr != nil {
		return nil, err
	}
	logger.Info("postgresql server-level query fell back to the connection's own database",
		"maintenance", pgMaintenanceDB, "database", fallbackDB)
	return fallback, nil
}

// pgServerQueryJSONViaSSH is pgServerQueryViaSSH for the json_agg form.
func (d *PostgreSQLDriver) pgServerQueryJSONViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig,
	fallbackDB, query string) ([]map[string]string, error) {
	res, err := d.pgQueryJSONViaSSH(exec, cfg, pgMaintenanceDB, query)
	if err == nil {
		return res, nil
	}
	if fallbackDB == "" || fallbackDB == pgMaintenanceDB {
		return nil, err
	}
	fallback, ferr := d.pgQueryJSONViaSSH(exec, cfg, fallbackDB, query)
	if ferr != nil {
		return nil, err
	}
	logger.Info("postgresql server-level query fell back to the connection's own database",
		"maintenance", pgMaintenanceDB, "database", fallbackDB)
	return fallback, nil
}

// buildPsqlCmd returns the base psql CLI command for SSH tunnel mode.
func (d *PostgreSQLDriver) buildPsqlCmd(cfg *base.SSHTunnelConfig, database string) string {
	dbHost := cfg.DBHost
	if dbHost == "" {
		dbHost = "127.0.0.1"
	}
	dbPort := cfg.DBPort
	if dbPort == 0 {
		dbPort = 5432
	}
	// ON_ERROR_STOP makes psql abort — and exit non-zero — on the first failing
	// statement. Without it a restore that violates a foreign key (or any other
	// constraint) prints to stderr, exits 0, and would be reported as success.
	parts := []string{"psql", "-v", "ON_ERROR_STOP=1", "-h", dbHost, "-p", strconv.Itoa(dbPort)}
	if cfg.Username != "" {
		parts = append(parts, "-U", cfg.Username)
	}
	if database != "" {
		parts = append(parts, "-d", database)
	}
	parts = append(parts, "--no-password")
	cmd := strings.Join(parts, " ")
	if cfg.Password != "" {
		cmd = "PGPASSWORD=" + pgShellEscape(cfg.Password) + " " + cmd
	}
	return cmd
}

// pgQueryJSONViaSSH runs query via psql with its result set wrapped in json_agg,
// and returns one map per row keyed by the query's output column aliases.
//
// The plain tab-separated transport used by pgQueryViaSSH cannot carry values
// that themselves contain a tab or a newline: psql's --no-align output does not
// escape them, so a multi-line column comment or default expression silently
// shifts every following field. Wrapping the result in json_agg makes psql emit
// a single JSON document in which such characters are escaped, giving the SSH
// path the same fidelity the direct path gets from the binary protocol.
//
// json_agg requires PostgreSQL 9.4+ (the spec's floor is 15). Every column must
// be aliased by the caller, since duplicate or missing keys would collide in the
// resulting JSON objects. Row order is NOT preserved — callers that care must
// sort explicitly (see pgSortRows).
func (d *PostgreSQLDriver) pgQueryJSONViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, query string) ([]map[string]string, error) {
	wrapped := fmt.Sprintf("SELECT COALESCE(json_agg(t), '[]'::json) FROM (%s) t", query)

	var buf bytes.Buffer
	cmd := d.buildPsqlCmd(cfg, database) + " --tuples-only --no-align"
	if err := exec.Run(cmd, strings.NewReader(wrapped+"\n"), &buf); err != nil {
		return nil, err
	}

	// json_agg separates array elements with newlines, so the payload is read
	// whole rather than line by line — which also sidesteps bufio.Scanner's
	// 64 KiB per-line ceiling on wide result sets.
	payload := bytes.TrimSpace(buf.Bytes())
	if len(payload) == 0 {
		return nil, nil
	}

	var raw []map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber() // keep int64 sizes exact instead of rounding through float64
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse psql json output: %w", err)
	}

	rows := make([]map[string]string, 0, len(raw))
	for _, r := range raw {
		row := make(map[string]string, len(r))
		for k, v := range r {
			row[k] = pgJSONScalarString(v)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// pgJSONScalarString renders a decoded JSON value as the string form the
// callers compare and parse. Nulls become "", numbers keep their exact literal,
// and anything structural is re-marshalled rather than dropped.
func pgJSONScalarString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// pgTruthy reports whether a psql boolean reads true, accepting both the
// tab-transport form ("t") and the JSON form ("true").
func pgTruthy(s string) bool {
	s = strings.TrimSpace(s)
	return s == "t" || s == "true"
}

// pgSortRows orders rows by the named column, numerically when every value
// parses as an integer and lexicographically otherwise. json_agg does not
// guarantee it preserves the subquery's ORDER BY, so ordering that callers rely
// on (ordinal_position, object names) is re-established here.
func pgSortRows(rows []map[string]string, key string) {
	numeric := true
	for _, r := range rows {
		if _, err := strconv.ParseInt(strings.TrimSpace(r[key]), 10, 64); err != nil {
			numeric = false
			break
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if numeric {
			a, _ := strconv.ParseInt(strings.TrimSpace(rows[i][key]), 10, 64)
			b, _ := strconv.ParseInt(strings.TrimSpace(rows[j][key]), 10, 64)
			return a < b
		}
		return rows[i][key] < rows[j][key]
	})
}

// pgQueryViaSSH runs a SQL query via psql and returns tab-separated rows.
// Used for scalar and DDL statements whose values cannot contain a delimiter
// (counts, booleans, CREATE ROLE/GRANT); result sets carrying free text go
// through pgQueryJSONViaSSH instead.
func (d *PostgreSQLDriver) pgQueryViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database, query string) ([][]string, error) {
	var buf bytes.Buffer
	cmd := d.buildPsqlCmd(cfg, database) + " --tuples-only --no-align --field-separator='\t'"
	if err := exec.Run(cmd, strings.NewReader(query+"\n"), &buf); err != nil {
		return nil, err
	}
	var result [][]string
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		result = append(result, strings.Split(line, "\t"))
	}
	return result, sc.Err()
}

// =============================================================================
// Internal Helpers — Dump Command Builder
// =============================================================================

// buildSSHDumpArgs builds the pg_dump argument list for SSH tunnel mode.
func (d *PostgreSQLDriver) buildSSHDumpArgs(cfg *base.SSHTunnelConfig, loc base.DBMSLocation, scope string) []string {
	database, filter := loc.Database, loc.Filter
	dbHost := cfg.DBHost
	if dbHost == "" {
		dbHost = "127.0.0.1"
	}
	dbPort := cfg.DBPort
	if dbPort == 0 {
		dbPort = 5432
	}

	var args []string
	if cfg.Password != "" {
		// pg_dump reads PGPASSWORD from environment; prepend with shell var.
		// This is handled in buildPsqlCmd via PGPASSWORD=; here we build the URI.
	}

	connStr := fmt.Sprintf("postgresql://")
	if cfg.Username != "" {
		connStr += cfg.Username
		if cfg.Password != "" {
			connStr += ":" + cfg.Password
		}
		connStr += "@"
	}
	connStr += fmt.Sprintf("%s:%d/%s", dbHost, dbPort, database)

	args = append(args, connStr)

	switch scope {
	case base.ScopeSchemaOnly:
		args = append(args, "--schema-only")
	case base.ScopeDataOnly:
		// A data-only dump loads rows into a target whose FK constraints already
		// exist, and pg_dump gives no ordering guarantee across tables, so the
		// COPY order alone can violate a FK. --disable-triggers wraps the data in
		// ALTER TABLE ... DISABLE TRIGGER ALL, the CLI counterpart of the
		// session_replication_role switch used in direct mode. It requires
		// superuser (or rds_superuser) rights on the target.
		args = append(args, "--data-only", "--disable-triggers")
	}

	// Selecting schemas needs no work beyond naming them: pg_dump with no -n
	// already dumps every schema, which is what an empty PgSchema means here too.
	for _, schema := range loc.PgSchema {
		args = append(args, "-n", pgDumpPattern(schema))
	}

	for _, t := range filter.ExcludedTableNames() {
		args = append(args, "-T", pgDumpPattern(t))
	}
	return args
}

// pgDumpPattern renders a name as a pg_dump object pattern that matches it
// literally.
//
// Two layers of quoting are in play and both are needed. pg_dump treats its
// pattern arguments as wildcards unless the name is double-quoted, so a table
// called "report_2024" would otherwise be matched as a pattern; and the command
// is assembled into a string a remote shell interprets, which would eat those
// double quotes before pg_dump ever saw them. The result is wrapped in single
// quotes so the shell passes the double-quoted form through intact.
//
// A name carrying a dot is read as schema.table, which is how a filter rule
// singles out one of two same-named tables. That does mean a table whose own
// name contains a dot cannot be addressed here — pg_dump has no way to express
// it either.
func pgDumpPattern(name string) string {
	if schema, table, ok := strings.Cut(name, "."); ok {
		return pgShellEscape(pgQuoteIdent(schema) + "." + pgQuoteIdent(table))
	}
	return pgShellEscape(pgQuoteIdent(name))
}

// =============================================================================
// Internal Helpers — Filter matching across schemas
// =============================================================================

// pgTableRef names one table by the schema that holds it. The dump works in
// these rather than bare names, because a bare name stops identifying a table as
// soon as a second schema is in play.
type pgTableRef struct {
	Schema string
	Name   string
}

// String renders the reference the way a filter rule and an error message both
// want to read it: qualified, unquoted.
func (t pgTableRef) String() string { return t.Schema + "." + t.Name }

// A filter rule names its target as a bare name ("orders") or as a qualified
// one ("sales.orders"). Both forms are accepted everywhere: a bare rule written
// before schemas were selectable keeps working, and a qualified rule can single
// out one of two same-named tables. The bare form deliberately matches in every
// schema — a rule that named no schema never promised to apply to only one.
//
// Matching reads the schema off the object itself, never off the dump's output
// form. Whether statements come out qualified (pgDumpScope.qualify) decides how
// a name is rendered, which is a different question from whether a rule picks
// the object out: a dump covering the single schema "sales" still has to honour
// a rule that says "sales.orders". Tying the two together would also make the
// two access modes disagree, since the ssh-tunnel path builds its pg_dump -T
// patterns straight from the rule names with no scope in hand.

// pgWarnRulesOutOfScope reports each filter rule whose schema qualifier names a
// schema the dump does not cover. Such a rule is inert: it can never match, so
// it excludes nothing.
//
// It is reported rather than refused because the two reasons a rule ends up
// inert are indistinguishable from here. One filter config shared across several
// migrations legitimately carries rules for schemas this run does not touch; a
// misspelled schema name looks exactly the same and silently migrates data the
// caller meant to leave behind. Refusing would break the first case, so the
// warning names the rule and lets the caller judge.
//
// A bare rule is never reported — it deliberately applies in every schema — and
// neither is anything on an engine whose names carry no qualifier.
func pgWarnRulesOutOfScope(filter *filterpkg.DBMSFilterOption, sc pgDumpScope) {
	for _, target := range filter.RuleTargets() {
		schema, _, qualified := strings.Cut(target, ".")
		if !qualified || sc.covers(schema) {
			continue
		}
		logger.Warn("postgresql filter rule names a schema this dump does not cover",
			"rule", target, "schema", schema, "dumpSchemas", strings.Join(sc.schemas, ", "))
	}
}

func pgTableExcluded(filter *filterpkg.DBMSFilterOption, t pgTableRef) bool {
	return filter.IsTableExcluded(t.Name) || filter.IsTableExcluded(t.String())
}

func pgObjectExcluded(filter *filterpkg.DBMSFilterOption, kind, schema, name string) bool {
	return filter.IsObjectExcluded(kind, name) || filter.IsObjectExcluded(kind, schema+"."+name)
}

func pgObjectExcludedInTable(filter *filterpkg.DBMSFilterOption, kind string, t pgTableRef, name string) bool {
	return filter.IsObjectExcludedInTable(kind, t.Name, name) ||
		filter.IsObjectExcludedInTable(kind, t.String(), name)
}

// pgExcludedColumns unions the column exclusions of both rule forms, so a table
// addressed either way drops the same columns.
func pgExcludedColumns(filter *filterpkg.DBMSFilterOption, t pgTableRef) map[string]bool {
	cols := filter.ExcludedColumns(t.Name)
	for c := range filter.ExcludedColumns(t.String()) {
		cols[c] = true
	}
	return cols
}

// pgRowExcludeMatches concatenates the row predicates of both rule forms; they
// are AND-combined by the caller, so a row dropped by either rule is dropped.
func pgRowExcludeMatches(filter *filterpkg.DBMSFilterOption, t pgTableRef) []string {
	matches := filter.RowExcludeMatches(t.Name)
	return append(matches, filter.RowExcludeMatches(t.String())...)
}

// =============================================================================
// Internal Helpers — Schema Discovery
// =============================================================================

func (d *PostgreSQLDriver) listExtensions(ctx context.Context, conn *pgx.Conn, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		"SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname")
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
	return filter.FilterObjects(filterpkg.ObjectKindExtension, names), rows.Err()
}

// dumpTypes writes CREATE TYPE statements for enums, composites, and domains.
func (d *PostgreSQLDriver) dumpTypes(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption, dst io.Writer) error {
	// --- Enum types ---
	rows, err := conn.Query(ctx,
		`SELECT n.nspname, t.typname, string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder)
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname IN `+pgSchemaLiteralList(sc.schemas)+`
		GROUP BY n.nspname, t.typname ORDER BY n.nspname, t.typname`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, typname, labels string
		if err := rows.Scan(&schema, &typname, &labels); err != nil {
			return err
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindType, schema, typname) {
			continue
		}
		parts := strings.Split(labels, ",")
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = "'" + pgEscape(p) + "'"
		}
		fmt.Fprintf(dst, "CREATE TYPE %s AS ENUM (%s);\n\n", sc.ident(schema, typname), strings.Join(quoted, ", "))
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// --- Domain types ---
	drows, err := conn.Query(ctx,
		`SELECT n.nspname, t.typname, pg_catalog.format_type(t.typbasetype, t.typtypmod),
			COALESCE(t.typdefault,''),
			CASE WHEN t.typnotnull THEN 'NOT NULL' ELSE '' END
		FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname IN `+pgSchemaLiteralList(sc.schemas)+` AND t.typtype = 'd'
		ORDER BY n.nspname, t.typname`)
	if err != nil {
		return err
	}
	defer drows.Close()
	for drows.Next() {
		var schema, name, baseType, defVal, notNull string
		if err := drows.Scan(&schema, &name, &baseType, &defVal, &notNull); err != nil {
			return err
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindType, schema, name) {
			continue
		}
		stmt := fmt.Sprintf("CREATE DOMAIN %s AS %s", sc.ident(schema, name), baseType)
		if defVal != "" {
			stmt += " DEFAULT " + defVal
		}
		if notNull == "NOT NULL" {
			stmt += " NOT NULL"
		}
		fmt.Fprintf(dst, "%s;\n\n", stmt)
	}
	return drows.Err()
}

func (d *PostgreSQLDriver) listSequences(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT sequence_schema, sequence_name, data_type, start_value, minimum_value, maximum_value, increment
		FROM information_schema.sequences
		WHERE sequence_schema IN `+pgSchemaLiteralList(sc.schemas)+`
		ORDER BY sequence_schema, sequence_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var schema, name, dtype, start, minv, maxv, incr string
		if err := rows.Scan(&schema, &name, &dtype, &start, &minv, &maxv, &incr); err != nil {
			return nil, err
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindSequence, schema, name) {
			continue
		}
		stmt := fmt.Sprintf(
			"CREATE SEQUENCE IF NOT EXISTS %s AS %s START WITH %s MINVALUE %s MAXVALUE %s INCREMENT BY %s",
			sc.ident(schema, name), dtype, start, minv, maxv, incr)
		stmts = append(stmts, stmt)
	}
	return stmts, rows.Err()
}

func (d *PostgreSQLDriver) listTables(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]pgTableRef, error) {
	rows, err := conn.Query(ctx,
		`SELECT table_schema, table_name FROM information_schema.tables
		WHERE table_schema IN `+pgSchemaLiteralList(sc.schemas)+` AND table_type='BASE TABLE'
		ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []pgTableRef
	for rows.Next() {
		var t pgTableRef
		if err := rows.Scan(&t.Schema, &t.Name); err != nil {
			return nil, err
		}
		if pgTableExcluded(filter, t) {
			continue
		}
		refs = append(refs, t)
	}
	return refs, rows.Err()
}

// buildCreateTable generates a CREATE TABLE statement. FK constraints are not
// part of it: listFKConstraints emits them as ALTER TABLE statements after every
// table exists, so circular references cannot break the restore.
// Columns present in excluded are omitted, along with any PRIMARY KEY or UNIQUE
// constraint referencing an excluded column.
func (d *PostgreSQLDriver) buildCreateTable(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, table pgTableRef, excluded map[string]bool) (string, error) {
	rows, err := conn.Query(ctx,
		`SELECT
			c.column_name,
			c.data_type,
			c.udt_name,
			c.character_maximum_length,
			c.numeric_precision,
			c.numeric_scale,
			c.is_nullable,
			c.column_default,
			c.is_identity,
			c.identity_generation
		FROM information_schema.columns c
		WHERE c.table_schema=$1 AND c.table_name=$2
		ORDER BY c.ordinal_position`, table.Schema, table.Name)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var colDefs []string
	for rows.Next() {
		var colName, dataType, udtName, isNullable, isIdentity string
		var charMax, numPrec, numScale *int
		var colDefault *string
		var identGen *string
		if err := rows.Scan(&colName, &dataType, &udtName, &charMax, &numPrec, &numScale,
			&isNullable, &colDefault, &isIdentity, &identGen); err != nil {
			return "", err
		}
		if excluded[colName] {
			continue
		}

		typStr := pgBuildTypeName(dataType, udtName, charMax, numPrec, numScale)
		def := pgQuoteIdent(colName) + " " + typStr
		if isIdentity == "YES" && identGen != nil {
			def += fmt.Sprintf(" GENERATED %s AS IDENTITY", *identGen)
		} else if colDefault != nil {
			def += " DEFAULT " + *colDefault
		}
		if strings.EqualFold(isNullable, "NO") {
			def += " NOT NULL"
		}
		colDefs = append(colDefs, "  "+def)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	// Primary key constraint
	pkRows, err := conn.Query(ctx,
		`SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON kcu.constraint_name = tc.constraint_name
			AND kcu.table_schema = tc.table_schema
		WHERE tc.table_schema=$1 AND tc.table_name=$2 AND tc.constraint_type='PRIMARY KEY'
		ORDER BY kcu.ordinal_position`, table.Schema, table.Name)
	if err != nil {
		return "", err
	}
	defer pkRows.Close()
	var pkCols []string
	pkHasExcluded := false
	for pkRows.Next() {
		var col string
		if err := pkRows.Scan(&col); err != nil {
			return "", err
		}
		if excluded[col] {
			pkHasExcluded = true
		}
		pkCols = append(pkCols, pgQuoteIdent(col))
	}
	if err := pkRows.Err(); err != nil {
		return "", err
	}
	// A primary key on a dropped column can no longer exist; omit it entirely.
	if len(pkCols) > 0 && !pkHasExcluded {
		colDefs = append(colDefs, fmt.Sprintf("  PRIMARY KEY (%s)", strings.Join(pkCols, ", ")))
	}

	// Unique constraints
	ucRows, err := conn.Query(ctx,
		`SELECT tc.constraint_name, string_agg(kcu.column_name, ',' ORDER BY kcu.ordinal_position)
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON kcu.constraint_name = tc.constraint_name
			AND kcu.table_schema = tc.table_schema
		WHERE tc.table_schema=$1 AND tc.table_name=$2 AND tc.constraint_type='UNIQUE'
		GROUP BY tc.constraint_name ORDER BY tc.constraint_name`, table.Schema, table.Name)
	if err != nil {
		return "", err
	}
	defer ucRows.Close()
	for ucRows.Next() {
		var cname, colList string
		if err := ucRows.Scan(&cname, &colList); err != nil {
			return "", err
		}
		cols := strings.Split(colList, ",")
		skip := false
		quoted := make([]string, len(cols))
		for i, c := range cols {
			if excluded[c] {
				skip = true
				break
			}
			quoted[i] = pgQuoteIdent(c)
		}
		if skip {
			continue
		}
		colDefs = append(colDefs, fmt.Sprintf("  CONSTRAINT %s UNIQUE (%s)", pgQuoteIdent(cname), strings.Join(quoted, ", ")))
	}
	if err := ucRows.Err(); err != nil {
		return "", err
	}

	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n);",
		sc.ident(table.Schema, table.Name), strings.Join(colDefs, ",\n"))

	return createSQL, nil
}

// listIndexDefs returns the CREATE INDEX statements the server renders for every
// non-primary index in scope.
//
// The definition text is taken as the server produced it. Whether it names the
// index's schema is decided by the search_path this dump set: bare in a
// single-schema dump, qualified in a multi-schema one — which is exactly the
// form the surrounding statements are written in either way.
func (d *PostgreSQLDriver) listIndexDefs(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	// Fetch each non-primary index along with its owning table and the column
	// names it covers, so indexes on wholly-excluded tables or on excluded
	// columns can be skipped (their referenced columns no longer exist).
	rows, err := conn.Query(ctx,
		`SELECT n.nspname AS schemaname, t.relname AS tablename, pi.indexname, pi.indexdef,
			COALESCE(string_agg(a.attname, ',' ORDER BY k.ord), '') AS cols
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_indexes pi ON pi.indexname = i.relname AND pi.schemaname = n.nspname
		JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		WHERE n.nspname IN `+pgSchemaLiteralList(sc.schemas)+` AND NOT ix.indisprimary
			-- An index backing a UNIQUE or EXCLUDE constraint is created by the
			-- constraint itself, so emitting CREATE INDEX for it too makes the
			-- restore fail with "relation already exists" (42P07).
			AND NOT EXISTS (SELECT 1 FROM pg_constraint pc WHERE pc.conindid = i.oid)
		GROUP BY n.nspname, t.relname, pi.indexname, pi.indexdef
		ORDER BY n.nspname, t.relname, pi.indexname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var defs []string
	for rows.Next() {
		var schema, tblName, idxName, def, cols string
		if err := rows.Scan(&schema, &tblName, &idxName, &def, &cols); err != nil {
			return nil, err
		}
		tbl := pgTableRef{Schema: schema, Name: tblName}
		if pgObjectExcludedInTable(filter, filterpkg.ObjectKindIndex, tbl, idxName) {
			continue
		}
		if pgTableExcluded(filter, tbl) {
			continue
		}
		if dropped := pgExcludedColumns(filter, tbl); len(dropped) > 0 {
			skip := false
			for _, c := range strings.Split(cols, ",") {
				if c != "" && dropped[c] {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		defs = append(defs, def)
	}
	return defs, rows.Err()
}

// pgAnyColExcluded reports whether any column in cols — a comma-separated list
// as returned by string_agg — is excluded from table by filter.
func pgAnyColExcluded(filter *filterpkg.DBMSFilterOption, table pgTableRef, cols string) bool {
	dropped := pgExcludedColumns(filter, table)
	if len(dropped) == 0 {
		return false
	}
	for _, c := range strings.Split(cols, ",") {
		if dropped[c] {
			return true
		}
	}
	return false
}

// listFKConstraints returns the ALTER TABLE statements that add every foreign
// key in scope. They are emitted after all tables exist, so a constraint may
// point at a table in another schema of the same dump without ordering trouble —
// which also means both sides have to be named with their own schema.
func (d *PostgreSQLDriver) listFKConstraints(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT
			tc.table_schema,
			tc.table_name,
			tc.constraint_name,
			string_agg(kcu.column_name, ',' ORDER BY kcu.ordinal_position),
			ccu.table_schema,
			ccu.table_name,
			string_agg(ccu.column_name, ',' ORDER BY kcu.ordinal_position),
			rc.update_rule,
			rc.delete_rule
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON ccu.constraint_name = tc.constraint_name
		JOIN information_schema.referential_constraints rc
			ON rc.constraint_name = tc.constraint_name
		WHERE tc.table_schema IN `+pgSchemaLiteralList(sc.schemas)+` AND tc.constraint_type='FOREIGN KEY'
		GROUP BY tc.table_schema, tc.table_name, tc.constraint_name,
			ccu.table_schema, ccu.table_name, rc.update_rule, rc.delete_rule
		ORDER BY tc.table_schema, tc.table_name, tc.constraint_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var alters []string
	for rows.Next() {
		var tblSchema, tblName, cname, cols, refSchema, refName, refCols, updRule, delRule string
		if err := rows.Scan(&tblSchema, &tblName, &cname, &cols, &refSchema, &refName, &refCols, &updRule, &delRule); err != nil {
			return nil, err
		}
		tbl := pgTableRef{Schema: tblSchema, Name: tblName}
		refTbl := pgTableRef{Schema: refSchema, Name: refName}
		// Skip an explicitly excluded FK, or one whose child or parent side was
		// removed by the filter: a wholly-excluded table, or a column excluded
		// from it (the table/column no longer exists after the dump, so the
		// constraint could not be created on the target).
		if pgObjectExcluded(filter, filterpkg.ObjectKindForeignKey, tblSchema, cname) {
			continue
		}
		if pgTableExcluded(filter, tbl) {
			continue
		}
		if pgAnyColExcluded(filter, tbl, cols) {
			continue
		}
		// A constraint whose parent is not part of this dump cannot be created on
		// the target either, so it is dropped with a warning rather than emitted
		// and left to fail.
		if !sc.covers(refTbl.Schema) {
			logger.Warn("postgresql skip foreign key with filtered parent",
				"table", tbl.String(), "constraint", cname, "referencedTable", refTbl.String(),
				"reason", "referenced table is in a schema outside the dump")
			continue
		}
		if pgTableExcluded(filter, refTbl) {
			logger.Warn("postgresql skip foreign key with filtered parent",
				"table", tbl.String(), "constraint", cname, "referencedTable", refTbl.String(),
				"reason", "referenced table excluded")
			continue
		}
		if pgAnyColExcluded(filter, refTbl, refCols) {
			logger.Warn("postgresql skip foreign key with filtered parent",
				"table", tbl.String(), "constraint", cname, "referencedTable", refTbl.String(),
				"reason", "referenced column excluded")
			continue
		}
		srcCols := pgQuoteColList(strings.Split(cols, ","))
		dstCols := pgQuoteColList(strings.Split(refCols, ","))
		alter := fmt.Sprintf(
			"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON UPDATE %s ON DELETE %s",
			sc.ident(tbl.Schema, tbl.Name), pgQuoteIdent(cname), srcCols,
			sc.ident(refTbl.Schema, refTbl.Name), dstCols, updRule, delRule)
		alters = append(alters, alter)
	}
	return alters, rows.Err()
}

func (d *PostgreSQLDriver) listViews(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT table_schema, table_name, view_definition
		FROM information_schema.views
		WHERE table_schema IN `+pgSchemaLiteralList(sc.schemas)+`
		ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var schema, name, def string
		if err := rows.Scan(&schema, &name, &def); err != nil {
			return nil, err
		}
		view := pgTableRef{Schema: schema, Name: name}
		if pgTableExcluded(filter, view) {
			continue
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindView, schema, name) {
			continue
		}
		stmts = append(stmts, fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s;", sc.ident(schema, name), strings.TrimRight(def, ";")))
	}
	return stmts, rows.Err()
}

func (d *PostgreSQLDriver) listMatViews(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT schemaname, matviewname, definition
		FROM pg_matviews
		WHERE schemaname IN `+pgSchemaLiteralList(sc.schemas)+`
		ORDER BY schemaname, matviewname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var schema, name, def string
		if err := rows.Scan(&schema, &name, &def); err != nil {
			return nil, err
		}
		mv := pgTableRef{Schema: schema, Name: name}
		if pgTableExcluded(filter, mv) {
			continue
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindMaterializedView, schema, name) {
			continue
		}
		stmts = append(stmts, fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n%s;", sc.ident(schema, name), strings.TrimRight(def, ";")))
	}
	return stmts, rows.Err()
}

// listFunctions returns CREATE OR REPLACE FUNCTION/PROCEDURE statements.
// prokind: 'f' = function, 'p' = procedure. Objects named by an object_exclude
// rule of the matching kind are skipped.
// listFunctions returns the routine definitions the server renders, unchanged.
//
// pg_get_functiondef writes the whole CREATE statement, including the routine's
// own schema, and this driver does not edit that text: a routine body is
// arbitrary SQL, and rewriting names inside it to chase a rename is guesswork.
// A rename is applied to the schema itself once the restore is done (see
// pgDumpScope), which carries the routine along without anyone parsing its body.
func (d *PostgreSQLDriver) listFunctions(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption, prokind string) ([]string, error) {
	kind := filterpkg.ObjectKindFunction
	if prokind == "p" {
		kind = filterpkg.ObjectKindProcedure
	}
	rows, err := conn.Query(ctx,
		`SELECT n.nspname, p.proname, pg_get_functiondef(p.oid)
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname IN `+pgSchemaLiteralList(sc.schemas)+` AND p.prokind=$1
		ORDER BY n.nspname, p.proname`, prokind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var schema, name, def string
		if err := rows.Scan(&schema, &name, &def); err != nil {
			return nil, err
		}
		if pgObjectExcluded(filter, kind, schema, name) {
			continue
		}
		stmts = append(stmts, strings.TrimRight(def, ";")+";")
	}
	return stmts, rows.Err()
}

func (d *PostgreSQLDriver) listTriggers(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT trigger_name, event_manipulation, event_object_schema, event_object_table,
			action_timing, action_orientation, action_statement
		FROM information_schema.triggers
		WHERE trigger_schema IN `+pgSchemaLiteralList(sc.schemas)+`
		ORDER BY event_object_schema, event_object_table, trigger_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var name, event, tblSchema, tblName, timing, orient, action string
		if err := rows.Scan(&name, &event, &tblSchema, &tblName, &timing, &orient, &action); err != nil {
			return nil, err
		}
		tbl := pgTableRef{Schema: tblSchema, Name: tblName}
		if pgTableExcluded(filter, tbl) {
			continue
		}
		// A trigger name is unique per table, not per schema, so it is matched
		// against the table it belongs to as well as on its own.
		if pgObjectExcludedInTable(filter, filterpkg.ObjectKindTrigger, tbl, name) ||
			pgObjectExcluded(filter, filterpkg.ObjectKindTrigger, tblSchema, name) {
			continue
		}
		stmt := fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH %s %s",
			pgQuoteIdent(name), timing, event, sc.ident(tbl.Schema, tbl.Name), orient, action)
		stmts = append(stmts, stmt)
	}
	return stmts, rows.Err()
}

func (d *PostgreSQLDriver) listRules(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT schemaname, rulename, tablename, definition
		FROM pg_rules
		WHERE schemaname IN `+pgSchemaLiteralList(sc.schemas)+`
		ORDER BY schemaname, tablename, rulename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stmts []string
	for rows.Next() {
		var schema, name, tblName, def string
		if err := rows.Scan(&schema, &name, &tblName, &def); err != nil {
			return nil, err
		}
		tbl := pgTableRef{Schema: schema, Name: tblName}
		if pgTableExcluded(filter, tbl) {
			continue
		}
		if pgObjectExcluded(filter, filterpkg.ObjectKindRule, schema, name) {
			continue
		}
		// Like a routine definition, this is the server's own rendering and is
		// emitted as-is; the search_path the dump set decides whether it names
		// its schema.
		stmts = append(stmts, def)
	}
	return stmts, rows.Err()
}

// =============================================================================
// Internal Helpers — Data Dump
// =============================================================================

// buildCopyQuery builds the SELECT statement used for COPY output and the COPY
// column clause (e.g. " (a, b)"). When the table has column exclusions, only the
// kept columns are selected and listed; otherwise SELECT * and an empty clause
// (COPY defaults to all columns in table order) are returned.
func (d *PostgreSQLDriver) buildCopyQuery(ctx context.Context, conn *pgx.Conn, sc pgDumpScope, table pgTableRef, filter *filterpkg.DBMSFilterOption) (query, colClause string, err error) {
	selectList := "*"
	if excluded := pgExcludedColumns(filter, table); len(excluded) > 0 {
		allCols, err := d.listColumns(ctx, conn, table)
		if err != nil {
			return "", "", err
		}
		kept := make([]string, 0, len(allCols))
		for _, c := range allCols {
			if !excluded[c] {
				kept = append(kept, c)
			}
		}
		quoted := make([]string, len(kept))
		for i, c := range kept {
			quoted[i] = pgQuoteIdent(c)
		}
		list := strings.Join(quoted, ", ")
		selectList = list
		colClause = " (" + list + ")"
	}
	// The SELECT reads the source, so it names the table by its own schema —
	// unlike the COPY header around it, which names the table as the dump writes
	// it. The two coincide unless the dump is renaming schemas.
	query = fmt.Sprintf("SELECT %s FROM %s", selectList, pgQualifyIdent(table.Schema, table.Name))
	if where := pgRowExcludeClause(filter, table); where != "" {
		query += " WHERE " + where
	}
	return query, colClause, nil
}

// pgRowExcludeClause builds the WHERE body that keeps every row not matched by a
// row_exclude predicate. Each predicate m is negated as "((m) IS NOT TRUE)",
// which holds when m is FALSE or UNKNOWN (NULL), so only rows for which m is
// definitively TRUE are dropped. Multiple predicates are AND-joined. Returns ""
// when the table has no row_exclude rules.
func pgRowExcludeClause(filter *filterpkg.DBMSFilterOption, table pgTableRef) string {
	matches := pgRowExcludeMatches(filter, table)
	if len(matches) == 0 {
		return ""
	}
	terms := make([]string, len(matches))
	for i, m := range matches {
		terms[i] = fmt.Sprintf("((%s) IS NOT TRUE)", m)
	}
	return strings.Join(terms, " AND ")
}

// listColumns returns the column names of table in ordinal order.
func (d *PostgreSQLDriver) listColumns(ctx context.Context, conn *pgx.Conn, table pgTableRef) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		WHERE table_schema=$1 AND table_name=$2
		ORDER BY ordinal_position`, table.Schema, table.Name)
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
// Internal Helpers — Statement Splitting
// =============================================================================

// pgIsMetaCommand reports whether one trimmed line is a psql meta-command
// (\restrict, \unrestrict, \connect …). The COPY terminator "\." is not one:
// it ends a data block the splitter consumes whole.
func pgIsMetaCommand(line string) bool {
	if !strings.HasPrefix(line, `\`) || line == `\.` {
		return false
	}
	return !strings.Contains(line, "\n")
}

// splitPGStatements splits a PostgreSQL SQL dump into individual statements.
// Handles dollar-quoting ($$...$$) used by function/procedure definitions.
func splitPGStatements(script string) []string {
	var stmts []string
	var buf strings.Builder
	sc := bufio.NewScanner(strings.NewReader(script))
	sc.Buffer(make([]byte, 4<<20), 4<<20)

	inDollarQuote := false
	dollarTag := ""
	inCopyData := false

	for sc.Scan() {
		line := sc.Text()

		// psql meta-commands are instructions to psql, not SQL for the server.
		// pg_dump has emitted \restrict / \unrestrict around its output since the
		// August 2025 minor releases (13.22, 14.19, 15.14, 16.10, 17.6), so a dump
		// staged from an ssh-tunnel source and replayed through the SQL path fails
		// on the first one unless it is dropped. The line is dropped on its own,
		// before it joins the buffer: a dump writes comment lines above it, and
		// keeping it would glue the meta-command to the statement below.
		// COPY data (whose terminator is "\.") and dollar-quoted bodies are
		// excluded — a backslash there is content.
		if !inDollarQuote && !inCopyData && pgIsMetaCommand(strings.TrimSpace(line)) {
			continue
		}

		buf.WriteString(line)
		buf.WriteString("\n")

		current := buf.String()

		// A COPY ... FROM STDIN block is one statement: its header, the tab-
		// separated rows that follow, and the \. that ends them. Splitting on the
		// header's semicolon would hand the server a COPY with no data, and it
		// would wait for rows that never arrive.
		if inCopyData {
			if strings.TrimRight(line, "\r") == `\.` {
				inCopyData = false
				stmts = append(stmts, strings.TrimSpace(current))
				buf.Reset()
			}
			continue
		}
		if !inDollarQuote && pgIsCopyFromStdin(line) {
			inCopyData = true
			continue
		}

		if !inDollarQuote {
			// Detect start of dollar-quote block. Only the line just read is
			// scanned: searching the whole buffer would keep rediscovering the
			// dollar-quoted body of a routine already closed earlier in this
			// statement, and every line after it would be swallowed as if the
			// quote were still open.
			if idx := pgFindDollarQuoteStart(line); idx >= 0 {
				tag := pgExtractDollarTag(line[idx:])
				// A block that also closes on this line needs no state.
				if tag != "" && !strings.Contains(line[idx+len(tag):], tag) {
					inDollarQuote = true
					dollarTag = tag
				}
			}
		} else {
			// Check if the line ends the dollar-quote block.
			if strings.Contains(line, dollarTag) {
				after := line[strings.Index(line, dollarTag)+len(dollarTag):]
				remaining := strings.TrimSpace(after)
				// Only end dollar-quote if the tag is not starting another.
				if !strings.Contains(remaining, "$") {
					inDollarQuote = false
					dollarTag = ""
					// Check if we have a complete statement ending with ;
					trimmed := strings.TrimRight(strings.TrimSpace(current), "\n")
					if strings.HasSuffix(trimmed, ";") {
						stmts = append(stmts, strings.TrimSpace(current))
						buf.Reset()
					}
				}
			}
			continue
		}

		// Outside dollar-quote: split on semicolon at end of line.
		trimmed := strings.TrimRight(strings.TrimSpace(current), "\n")
		if strings.HasSuffix(trimmed, ";") && !inDollarQuote {
			stmt := strings.TrimSpace(current)
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

var pgDollarTagRe = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

// pgIsPostDataStmt reports whether a statement belongs after the rows rather than
// before them.
//
// Two kinds qualify, and for the same reason - both are enforcement that would
// act on data being restored rather than on data already settled:
//
//	ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY
//	  The dump writes tables without their FK constraints and adds them in a
//	  later section, but still ahead of the data. COPY gives no ordering
//	  guarantee across tables, so a child row can arrive before its parent and
//	  the constraint rejects it.
//
//	CREATE TRIGGER / CREATE CONSTRAINT TRIGGER
//	  A trigger created before the load fires once per restored row. An audit
//	  trigger then writes rows the source never had; a BEFORE trigger rewrites
//	  the values being migrated.
//
// pg_dump puts both in its post-data section for these reasons, and this driver's
// SSH path inherits that ordering by using pg_dump. This is the direct path
// arriving at the same place.
func pgIsPostDataStmt(stmt string) bool {
	head := strings.ToUpper(strings.TrimSpace(stmt))
	if strings.HasPrefix(head, "CREATE TRIGGER") || strings.HasPrefix(head, "CREATE CONSTRAINT TRIGGER") {
		return true
	}
	// Only a FK constraint is held back. A CHECK or UNIQUE constraint added by the
	// same statement shape is satisfied row by row and does not depend on the order
	// tables are loaded in, so moving it would delay an error without preventing one.
	return strings.HasPrefix(head, "ALTER TABLE") &&
		strings.Contains(head, "ADD CONSTRAINT") &&
		strings.Contains(head, "FOREIGN KEY")
}

// pgIsCopyFromStdin reports whether line opens a COPY ... FROM STDIN data block.
func pgIsCopyFromStdin(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	return strings.HasPrefix(upper, "COPY ") &&
		strings.Contains(upper, "FROM STDIN") &&
		strings.HasSuffix(upper, ";")
}

// pgSplitCopyBlock separates a COPY block into the statement the server is given
// and the row data streamed into it. The trailing \. terminator is psql's, not
// the protocol's, so it is dropped here.
func pgSplitCopyBlock(stmt string) (header, data string) {
	nl := strings.Index(stmt, "\n")
	if nl < 0 {
		return strings.TrimSpace(stmt), ""
	}
	header = strings.TrimSpace(stmt[:nl])
	rows := stmt[nl+1:]
	if idx := strings.LastIndex(rows, "\\."); idx >= 0 {
		rows = rows[:idx]
	}
	return header, rows
}

func pgFindDollarQuoteStart(s string) int {
	m := pgDollarTagRe.FindStringIndex(s)
	if m == nil {
		return -1
	}
	return m[0]
}

func pgExtractDollarTag(s string) string {
	m := pgDollarTagRe.FindString(s)
	return m
}

// pgIdentPattern matches one identifier as the dump writes it — double-quoted or
// bare — optionally preceded by its schema. The schema and the name are captured
// separately so a qualified target reads back as the pair it was written as.
const pgIdentPattern = `(?:(?:"((?:[^"]|"")+)"|(\w+))\s*\.\s*)?(?:"((?:[^"]|"")+)"|(\w+))`

var (
	pgCreateTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + pgIdentPattern)
	pgCopyTableRe   = regexp.MustCompile(`(?i)COPY\s+` + pgIdentPattern)
	pgMatViewRe     = regexp.MustCompile(`(?i)CREATE\s+MATERIALIZED\s+VIEW\s+` + pgIdentPattern)
)

// pgExtractParts pulls the (schema, name) pair out of a pgIdentPattern match.
// schema is empty when the statement did not carry one. Doubled quotes inside a
// quoted identifier are collapsed back to one, so the results are the names
// themselves rather than their SQL spelling.
func pgExtractParts(re *regexp.Regexp, stmt string) (schema, name string) {
	m := re.FindStringSubmatch(stmt)
	if len(m) < 5 {
		return "", ""
	}
	pick := func(quoted, bare string) string {
		if quoted != "" {
			return strings.ReplaceAll(quoted, `""`, `"`)
		}
		return bare
	}
	return pick(m[1], m[2]), pick(m[3], m[4])
}

// pgExtractQualified renders that pair the way the rest of the driver names a
// table: schema.name when the statement carried a schema, the bare name when it
// did not. Used for progress reporting, where the string is only ever displayed.
func pgExtractQualified(re *regexp.Regexp, stmt string) string {
	schema, name := pgExtractParts(re, stmt)
	if name == "" {
		return ""
	}
	if schema != "" {
		return schema + "." + name
	}
	return name
}

func pgExtractCreateTable(stmt string) string { return pgExtractQualified(pgCreateTableRe, stmt) }

func pgExtractCopyTable(stmt string) string { return pgExtractQualified(pgCopyTableRe, stmt) }

// pgExtractMatViewIdent returns the materialized view a statement creates as a
// quoted identifier ready to splice into SQL. Unlike the two above, this result
// is executed rather than displayed, so it keeps the schema and the name as
// separate quoted parts: quoting a joined "sales.mv_daily" whole would name a
// single view with a dot in it.
func pgExtractMatViewIdent(stmt string) string {
	schema, name := pgExtractParts(pgMatViewRe, stmt)
	if name == "" {
		return ""
	}
	return pgQualifyIdent(schema, name)
}

// =============================================================================
// Internal Helpers — Value Formatting
// =============================================================================

// pgFormatCopyValue formats a value for PostgreSQL COPY text format (tab-separated).
func pgFormatCopyValue(v interface{}) string {
	if v == nil {
		return `\N`
	}
	switch val := v.(type) {
	case int:
		return strconv.Itoa(val)
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		if val {
			return "t"
		}
		return "f"
	case []byte:
		return pgEscapeCopy(string(val))
	case string:
		return pgEscapeCopy(val)
	case time.Time:
		return val.Format("2006-01-02 15:04:05.999999-07:00")
	default:
		// pgx hands back its own types for numeric, uuid, interval and the like.
		// Their driver.Valuer form is the text the server parses back; printing
		// the Go value would write the struct itself into the dump ("{200000000000
		// -2 false finite true}" for a numeric), which the restore then rejects.
		if valuer, ok := v.(driver.Valuer); ok {
			dv, err := valuer.Value()
			if err == nil {
				if dv == nil {
					return `\N`
				}
				return pgFormatCopyValue(dv)
			}
		}
		return pgEscapeCopy(fmt.Sprintf("%v", v))
	}
}

// pgEscapeCopy escapes a string for PostgreSQL COPY text format.
func pgEscapeCopy(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

// pgEscape escapes a string for use inside single-quoted PostgreSQL literals.
func pgEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// pgShellEscape wraps a string in single quotes for shell usage.
func pgShellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// pgQuoteIdent wraps an identifier in double quotes.
func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// pgQualifyIdent renders schema.name as two quoted identifiers, or just the
// quoted name when schema is empty.
//
// An empty schema is how a single-schema dump is written: its statements carry
// bare names so the restore can place them wherever its search_path points,
// which is what makes migrating one schema into a differently named one
// possible. With more than one schema in play the names must say which is
// which, and the schema is always set.
func pgQualifyIdent(schema, name string) string {
	if schema == "" {
		return pgQuoteIdent(name)
	}
	return pgQuoteIdent(schema) + "." + pgQuoteIdent(name)
}

// pgQuoteColList returns a comma-separated list of quoted identifiers.
func pgQuoteColList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pgQuoteIdent(strings.TrimSpace(c))
	}
	return strings.Join(quoted, ", ")
}

// pgBuildTypeName converts information_schema column type info to a PostgreSQL type string.
func pgBuildTypeName(dataType, udtName string, charMax, numPrec, numScale *int) string {
	switch dataType {
	case "character varying":
		if charMax != nil {
			return fmt.Sprintf("character varying(%d)", *charMax)
		}
		return "character varying"
	case "character":
		if charMax != nil {
			return fmt.Sprintf("character(%d)", *charMax)
		}
		return "character"
	case "numeric":
		if numPrec != nil && numScale != nil {
			return fmt.Sprintf("numeric(%d,%d)", *numPrec, *numScale)
		}
		return "numeric"
	case "USER-DEFINED":
		return udtName
	case "ARRAY":
		return udtName
	default:
		return dataType
	}
}

// =============================================================================
// Target preparation
// =============================================================================

// DropAllObjects is a no-op for PostgreSQL: rollbackDrop already rolls back via
// a generated DROP script, so this is never called for this engine.
func (d *PostgreSQLDriver) DropAllObjects(ctx context.Context, loc base.DBMSLocation) error {
	return nil
}

// PrepareTarget creates the target database and role before migration.
func (d *PostgreSQLDriver) PrepareTarget(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	if loc.IsDirect() {
		return d.prepareTargetViaSQL(ctx, loc, grant)
	}
	return d.prepareTargetViaSSH(ctx, loc, grant)
}

// pgRoleExistsQuery and pgCreateRole build the two statements that provision a
// login role.
//
// PostgreSQL has no CREATE ROLE ... IF NOT EXISTS, so the existence check is a
// separate query — the same shape CreateDatabase uses for the same reason. A DO
// block would collapse the two into one statement, but it would also mean
// embedding the password inside a dollar-quoted body, and a password is the last
// value that should have to survive a second layer of quoting.
const pgRoleExistsQuery = `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`

func pgCreateRole(username, password string) string {
	return "CREATE ROLE " + pgQuoteIdent(username) +
		" WITH LOGIN PASSWORD '" + pgEscape(password) + "'"
}

// pgGrantSchemaTargets returns the schemas a grant user must be able to create
// in, out of those that exist right now.
//
// "public" is on the list whenever the location does not name schemas, because
// that is where a single-schema dump lands by default — and from PostgreSQL 15
// on it is exactly the schema a database-level grant no longer covers: CREATE on
// the public schema was revoked from PUBLIC in that release, so a role holding
// ALL PRIVILEGES ON DATABASE still cannot create a table there.
//
// Schemas the location names but that do not exist yet are left out on purpose.
// The migration creates them itself, which makes the grant user their owner, and
// granting on a schema that is not there would fail.
func pgGrantSchemaTargets(loc base.DBMSLocation, existing []string) []string {
	want := loc.PgSchema
	if len(want) == 0 {
		want = []string{"public"}
	}
	have := make(map[string]bool, len(existing))
	for _, name := range existing {
		have[name] = true
	}
	targets := make([]string, 0, len(want))
	for _, name := range want {
		if have[name] {
			targets = append(targets, name)
		}
	}
	return targets
}

// pgGrantSchema builds the schema-level grant. USAGE lets the role see what is
// in the schema and CREATE lets it add to it; together they are what "ALL" means
// for a schema.
func pgGrantSchema(schema, username string) string {
	return "GRANT ALL ON SCHEMA " + pgQuoteIdent(schema) + " TO " + pgQuoteIdent(username)
}

// prepareTargetViaSQL grants access to a pre-existing target database via a
// direct pgx connection. Connects to the "postgres" system database for
// server-level DDL. The database must already exist and be empty; dbmsx does
// not create it.
func (d *PostgreSQLDriver) prepareTargetViaSQL(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	// Connect to the "postgres" default database for server-level DDL.
	adminLoc := loc
	adminLoc.Database = pgMaintenanceDB
	conn, err := d.openConn(ctx, adminLoc)
	if err != nil {
		return fmt.Errorf("prepareTarget open admin connection: %w", err)
	}
	defer conn.Close(ctx)

	// The target database must already exist.
	exists, err := pgDatabaseExists(ctx, conn, loc.Database)
	if err != nil {
		return fmt.Errorf("prepareTarget pg_database check: %w", err)
	}
	if !exists {
		return &base.TargetDatabaseNotFoundError{DBMSType: base.DBMSTypePostgreSQL, Database: loc.Database}
	}

	// Database exists — require it to be empty before granting access.
	if err = d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	var roleExists bool
	if err = conn.QueryRow(ctx, pgRoleExistsQuery, grant.Username).Scan(&roleExists); err != nil {
		return fmt.Errorf("prepareTarget pg_roles check: %w", err)
	}
	if !roleExists {
		if _, err = conn.Exec(ctx, pgCreateRole(grant.Username, grant.Password)); err != nil {
			return fmt.Errorf("prepareTarget CREATE ROLE: %w", err)
		}
	}
	if _, err = conn.Exec(ctx,
		"GRANT ALL PRIVILEGES ON DATABASE "+pgQuoteIdent(loc.Database)+
			" TO "+pgQuoteIdent(grant.Username)); err != nil {
		return fmt.Errorf("prepareTarget GRANT: %w", err)
	}

	// The schema-level grants have to be made from inside the target database:
	// a schema belongs to one database, and the admin connection is on another.
	targetConn, err := d.openConn(ctx, loc)
	if err != nil {
		return fmt.Errorf("prepareTarget open target connection: %w", err)
	}
	defer targetConn.Close(ctx)

	schemas, err := d.listSchemasViaSQL(ctx, targetConn)
	if err != nil {
		return fmt.Errorf("prepareTarget list schemas: %w", err)
	}
	for _, schema := range pgGrantSchemaTargets(loc, schemas) {
		if _, err = targetConn.Exec(ctx, pgGrantSchema(schema, grant.Username)); err != nil {
			return fmt.Errorf("prepareTarget GRANT ON SCHEMA %s: %w", schema, err)
		}
	}
	return nil
}

// prepareTargetViaSSH grants access to a pre-existing target database via the
// SSH-tunnel CLI. The database must already exist and be empty; dbmsx does not
// create it.
func (d *PostgreSQLDriver) prepareTargetViaSSH(ctx context.Context, loc base.DBMSLocation, grant *base.TargetGrant) error {
	cfg := loc.SSHTunnel

	exec, err := base.DialSSHExec(cfg.SSH)
	if err != nil {
		return err
	}
	defer exec.Close()

	// The target database must already exist.
	dbExists, err := d.databaseExistsSSH(exec, cfg, loc.Database)
	if err != nil {
		return fmt.Errorf("prepareTarget pg_database check via SSH: %w", err)
	}
	if !dbExists {
		return &base.TargetDatabaseNotFoundError{DBMSType: base.DBMSTypePostgreSQL, Database: loc.Database}
	}

	// Database exists — require it to be empty before granting access.
	if err = d.CheckTargetEmpty(ctx, loc); err != nil {
		return err
	}

	// PostgreSQL has no CREATE ROLE ... IF NOT EXISTS; see pgRoleExistsQuery.
	// psql takes no bind parameters, so the name goes in as an escaped literal.
	roleRows, err := d.pgQueryViaSSH(exec, cfg, pgMaintenanceDB,
		strings.Replace(pgRoleExistsQuery, "$1", "'"+pgEscape(grant.Username)+"'", 1))
	if err != nil {
		return fmt.Errorf("prepareTarget pg_roles check via SSH: %w", err)
	}
	if !(len(roleRows) > 0 && len(roleRows[0]) > 0 && pgTruthy(roleRows[0][0])) {
		if _, err = d.pgQueryViaSSH(exec, cfg, pgMaintenanceDB,
			pgCreateRole(grant.Username, grant.Password)); err != nil {
			return fmt.Errorf("prepareTarget CREATE ROLE via SSH: %w", err)
		}
	}
	if _, err = d.pgQueryViaSSH(exec, cfg, pgMaintenanceDB,
		"GRANT ALL PRIVILEGES ON DATABASE "+pgQuoteIdent(loc.Database)+
			" TO "+pgQuoteIdent(grant.Username)); err != nil {
		return fmt.Errorf("prepareTarget GRANT via SSH: %w", err)
	}

	// The schema-level grants run against the target database rather than the
	// maintenance one: a schema belongs to a single database.
	schemas, err := d.listSchemasViaSSH(exec, cfg, loc.Database)
	if err != nil {
		return fmt.Errorf("prepareTarget list schemas via SSH: %w", err)
	}
	for _, schema := range pgGrantSchemaTargets(loc, schemas) {
		if _, err = d.pgQueryViaSSH(exec, cfg, loc.Database,
			pgGrantSchema(schema, grant.Username)); err != nil {
			return fmt.Errorf("prepareTarget GRANT ON SCHEMA %s via SSH: %w", schema, err)
		}
	}
	return nil
}
