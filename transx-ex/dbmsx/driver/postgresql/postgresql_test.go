package postgresql

import (
	"bytes"
	"context"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// =============================================================================
// Unit tests (no PostgreSQL server required)
// =============================================================================

func TestBuildPGDumpCmd_ContainsPgDump(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeFull)
	if !strings.HasPrefix(cmd, "pg_dump") {
		t.Errorf("BuildDumpCmd should start with 'pg_dump', got: %s", cmd)
	}
}

func TestBuildPGDumpCmd_SchemaOnly(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeSchemaOnly)
	if !strings.Contains(cmd, "--schema-only") {
		t.Errorf("schema-only should have --schema-only: %s", cmd)
	}
	if strings.Contains(cmd, "--data-only") {
		t.Errorf("schema-only must not have --data-only: %s", cmd)
	}
}

func TestBuildPGDumpCmd_DataOnly(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeDataOnly)
	if !strings.Contains(cmd, "--data-only") {
		t.Errorf("data-only should have --data-only: %s", cmd)
	}
	if strings.Contains(cmd, "--schema-only") {
		t.Errorf("data-only must not have --schema-only: %s", cmd)
	}
}

func TestBuildPGDumpCmd_Full_NoScopeFlag(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeFull)
	if strings.Contains(cmd, "--schema-only") || strings.Contains(cmd, "--data-only") {
		t.Errorf("full scope should have no scope flag: %s", cmd)
	}
}

func TestBuildPGDumpCmd_DirectMode_ReturnsEmpty(t *testing.T) {
	drv := NewPostgreSQLDriver()
	if cmd := drv.BuildDumpCmd(directPG(), base.ScopeFull); cmd != "" {
		t.Errorf("BuildDumpCmd for direct mode should be empty, got %q", cmd)
	}
}

func TestBuildPGRestoreCmd_ContainsPsql(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildRestoreCmd(sshTunnelPG())
	if !strings.HasPrefix(cmd, "PGPASSWORD=") && !strings.HasPrefix(cmd, "psql") {
		t.Errorf("BuildRestoreCmd should start with psql or PGPASSWORD=...: %s", cmd)
	}
	if !strings.Contains(cmd, "psql") {
		t.Errorf("BuildRestoreCmd should contain 'psql': %s", cmd)
	}
}

func TestBuildPGRestoreCmd_DirectMode_ReturnsEmpty(t *testing.T) {
	drv := NewPostgreSQLDriver()
	if cmd := drv.BuildRestoreCmd(directPG()); cmd != "" {
		t.Errorf("BuildRestoreCmd for direct mode should be empty, got %q", cmd)
	}
}

func TestBuildPGDumpCmd_WithExcludedTable(t *testing.T) {
	drv := NewPostgreSQLDriver()
	loc := sshTunnelPG()
	loc.Filter = &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("logs"),
	}}
	cmd := drv.BuildDumpCmd(loc, base.ScopeFull)
	// The pattern is double-quoted so pg_dump matches the name literally instead
	// of as a wildcard, and single-quoted so the remote shell hands those double
	// quotes through rather than eating them.
	if !strings.Contains(cmd, `-T '"logs"'`) {
		t.Errorf("whole-table exclusion should add -T flag: %s", cmd)
	}
}

func TestBuildPGDumpCmd_SelectsSchemas(t *testing.T) {
	drv := NewPostgreSQLDriver()

	// No selection means every schema, which is also pg_dump's own default, so
	// the command carries no -n at all.
	if cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeFull); strings.Contains(cmd, " -n ") {
		t.Errorf("an empty PgSchema should add no -n flag: %s", cmd)
	}

	loc := sshTunnelPG()
	loc.PgSchema = []string{"sales", "hr"}
	cmd := drv.BuildDumpCmd(loc, base.ScopeFull)
	for _, want := range []string{`-n '"sales"'`, `-n '"hr"'`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command is missing %s: %s", want, cmd)
		}
	}
}

func TestBuildPGDumpCmd_QualifiedExcludePattern(t *testing.T) {
	drv := NewPostgreSQLDriver()
	loc := sshTunnelPG()
	loc.PgSchema = []string{"public", "sales"}
	loc.Filter = &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("sales.logs"),
	}}
	cmd := drv.BuildDumpCmd(loc, base.ScopeFull)
	// A qualified rule has to reach pg_dump as schema.table with each part quoted
	// on its own, or it would name one table with a dot in it.
	if !strings.Contains(cmd, `-T '"sales"."logs"'`) {
		t.Errorf("qualified exclusion should keep the schema separate: %s", cmd)
	}
}

func TestPgDumpPattern(t *testing.T) {
	cases := []struct{ in, want string }{
		{"logs", `'"logs"'`},
		{"sales.logs", `'"sales"."logs"'`},
		// A wildcard in the name is matched literally rather than expanded.
		{"report_2024", `'"report_2024"'`},
		{`od"d`, `'"od""d"'`},
	}
	for _, c := range cases {
		if got := pgDumpPattern(c.in); got != c.want {
			t.Errorf("pgDumpPattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// =============================================================================
// CheckTargetEmpty scope
// =============================================================================

func TestPgTargetTableCountQuery(t *testing.T) {
	// No selection covers the database, so every user schema counts: a table
	// anywhere in it is in the migration's way.
	all := pgTargetTableCountQuery(base.DBMSLocation{})
	if !strings.Contains(all, `NOT LIKE 'pg\_%'`) || !strings.Contains(all, `<> 'information_schema'`) {
		t.Errorf("an unselected location should count every user schema: %s", all)
	}

	// A selection narrows the question to the schemas being written to.
	some := pgTargetTableCountQuery(base.DBMSLocation{PgSchema: []string{"sales", "hr"}})
	if !strings.Contains(some, `IN ('sales', 'hr')`) {
		t.Errorf("a selected location should count only its own schemas: %s", some)
	}
	if strings.Contains(some, "NOT LIKE") {
		t.Errorf("a selected location should not fall back to the database-wide predicate: %s", some)
	}
}

func TestSplitPGStatements_Simple(t *testing.T) {
	script := "CREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n"
	stmts := splitPGStatements(script)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "CREATE TABLE") {
		t.Errorf("first stmt should be CREATE TABLE, got: %s", stmts[0])
	}
}

func TestSplitPGStatements_DollarQuoteBlock(t *testing.T) {
	script := "CREATE TABLE t (id INT);\n" +
		"CREATE OR REPLACE FUNCTION add(a int, b int) RETURNS int AS $$\n" +
		"BEGIN\n" +
		"  RETURN a + b;\n" +
		"END;\n" +
		"$$ LANGUAGE plpgsql;\n" +
		"INSERT INTO t VALUES (2);\n"

	stmts := splitPGStatements(script)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[1], "CREATE OR REPLACE FUNCTION") {
		t.Errorf("second stmt should be CREATE FUNCTION, got: %s", stmts[1])
	}
	if !strings.Contains(stmts[1], "RETURN a + b;") {
		t.Errorf("function body should be intact, got: %s", stmts[1])
	}
}

func TestSplitPGStatements_EmptyInput(t *testing.T) {
	if stmts := splitPGStatements(""); len(stmts) != 0 {
		t.Errorf("expected 0 statements for empty input, got %d", len(stmts))
	}
}

// TestSplitPGStatements_DollarQuotedRoutines checks that a routine body keeps its
// own semicolons and, just as importantly, that the statements following it are
// still split off.
//
// The invariant this guards: the dollar-quote start must be searched only in
// the current line. Searching the whole accumulated buffer rediscovers the
// opening tag of an already closed $tag$...$tag$ block and reopens the quote,
// swallowing everything after a function definition into one statement that
// the server rejects as a syntax error.
func TestSplitPGStatements_DollarQuotedRoutines(t *testing.T) {
	fn := "CREATE OR REPLACE FUNCTION public.fn_annual_salary(p numeric)\n" +
		" RETURNS numeric\n LANGUAGE sql\n IMMUTABLE\n" +
		"AS $function$\n    SELECT ROUND(p * 12, 2);\n$function$\n;\n"
	// Opened and closed on one line - must not leave the quote open either.
	oneLine := "CREATE FUNCTION f2() RETURNS int LANGUAGE sql AS $function$ SELECT 1; $function$;\n"
	plpgsql := "CREATE OR REPLACE FUNCTION trg() RETURNS trigger LANGUAGE plpgsql AS $$\n" +
		"BEGIN\n  RAISE NOTICE 'x';\n  RETURN NEW;\nEND;\n$$;\n"

	script := "CREATE TABLE t (id int);\n\n" + fn + "\n" + oneLine + "\n" + plpgsql +
		"\nCREATE INDEX ix ON t(id);\n"

	stmts := splitPGStatements(script)
	if len(stmts) != 5 {
		t.Fatalf("want 5 statements, got %d:\n%q", len(stmts), stmts)
	}

	wantPrefix := []string{
		"CREATE TABLE t",
		"CREATE OR REPLACE FUNCTION public.fn_annual_salary",
		"CREATE FUNCTION f2()",
		"CREATE OR REPLACE FUNCTION trg()",
		"CREATE INDEX ix",
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(stmts[i], want) {
			t.Errorf("statement %d: want prefix %q, got %q", i, want, stmts[i])
		}
	}

	// The routine bodies must have survived whole, semicolons included.
	if got := stmts[1]; !strings.Contains(got, "SELECT ROUND(p * 12, 2);") {
		t.Errorf("function body was cut: %q", got)
	}
	if got := stmts[3]; !strings.Contains(got, "RETURN NEW;") {
		t.Errorf("plpgsql body was cut: %q", got)
	}
}

// TestSplitPGStatements_CopyBlock checks that a COPY ... FROM STDIN block stays
// whole: header, rows and the \. terminator are one statement.
//
// Splitting at the header's semicolon would send the server a COPY with no rows
// behind it, and it waits for them indefinitely — the restore hangs rather than
// failing.
func TestSplitPGStatements_CopyBlock(t *testing.T) {
	script := "CREATE TABLE t (id int, name text);\n\n" +
		"COPY \"t\" (id, name) FROM STDIN;\n1	a\n2	b;c\n\\.\n\n" +
		"CREATE INDEX ix ON t(id);\n"

	stmts := splitPGStatements(script)
	if len(stmts) != 3 {
		t.Fatalf("want 3 statements, got %d:\n%q", len(stmts), stmts)
	}

	copyStmt := stmts[1]
	for _, want := range []string{"COPY \"t\" (id, name) FROM STDIN;", "1	a", "2	b;c", "\\."} {
		if !strings.Contains(copyStmt, want) {
			t.Errorf("COPY block lost %q:\n%q", want, copyStmt)
		}
	}
	if !strings.HasPrefix(stmts[2], "CREATE INDEX") {
		t.Errorf("statement after the COPY block: got %q", stmts[2])
	}

	// The restore sends the header as the statement and the rows as the stream.
	header, data := pgSplitCopyBlock(copyStmt)
	if header != "COPY \"t\" (id, name) FROM STDIN;" {
		t.Errorf("header = %q", header)
	}
	if want := "1	a\n2	b;c\n"; data != want {
		t.Errorf("data = %q, want %q", data, want)
	}
}

func TestPGFormatCopyValue_Types(t *testing.T) {
	cases := []struct {
		input interface{}
		want  string
	}{
		{nil, `\N`},
		{int64(42), "42"},
		{float64(3.14), "3.14"},
		{true, "t"},
		{false, "f"},
		{[]byte("hello"), "hello"},
		{"world", "world"},
		{"tab\there", `tab\there`},
		{"new\nline", `new\nline`},
	}
	for _, tc := range cases {
		got := pgFormatCopyValue(tc.input)
		if got != tc.want {
			t.Errorf("pgFormatCopyValue(%T(%v)) = %q, want %q", tc.input, tc.input, got, tc.want)
		}
	}
}

// TestPGFormatCopyValue_PgtypeValues covers the values pgx returns as its own
// types. Formatting them with %v would write the Go struct into the dump — the
// restore then fails with "invalid input syntax for type numeric".
func TestPGFormatCopyValue_PgtypeValues(t *testing.T) {
	num := pgtype.Numeric{}
	if err := num.Scan("1234.56"); err != nil {
		t.Fatalf("scan numeric: %v", err)
	}
	if got := pgFormatCopyValue(num); got != "1234.56" {
		t.Errorf("numeric: got %q, want %q", got, "1234.56")
	}

	// An unset pgtype value is SQL NULL, which COPY spells \\N.
	if got := pgFormatCopyValue(pgtype.Numeric{}); got != `\N` {
		t.Errorf("null numeric: got %q, want %q", got, `\N`)
	}
}

func TestPGBuildCopyQuery_NoExclusion(t *testing.T) {
	drv := NewPostgreSQLDriver()
	// No column exclusion → SELECT * and empty COPY column clause, no DB access.
	sc := newPgDumpScope([]string{"public"}, nil)
	tbl := pgTableRef{Schema: "public", Name: "users"}
	q, clause, err := drv.buildCopyQuery(context.Background(), nil, sc, tbl, nil)
	if err != nil {
		t.Fatalf("buildCopyQuery: %v", err)
	}
	// The SELECT always names the source table by its own schema, whatever form
	// the COPY header around it takes.
	if q != `SELECT * FROM "public"."users"` || clause != "" {
		t.Errorf("unexpected query=%q clause=%q", q, clause)
	}
}

func TestPGQuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"users", `"users"`},
		{"my table", `"my table"`},
		{`has"quote`, `"has""quote"`},
	}
	for _, tc := range cases {
		got := pgQuoteIdent(tc.in)
		if got != tc.want {
			t.Errorf("pgQuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPGExtractCreateTable(t *testing.T) {
	stmt := `CREATE TABLE IF NOT EXISTS "users" (id SERIAL PRIMARY KEY);`
	got := pgExtractCreateTable(stmt)
	if got != "users" {
		t.Errorf("pgExtractCreateTable = %q, want %q", got, "users")
	}
}

func TestPGExtractCopyTable(t *testing.T) {
	stmt := `COPY "orders" FROM STDIN;`
	got := pgExtractCopyTable(stmt)
	if got != "orders" {
		t.Errorf("pgExtractCopyTable = %q, want %q", got, "orders")
	}
}

// =============================================================================
// Integration tests (require POSTGRES_TEST_DSN)
// =============================================================================
//
// Run postgresql_test.sh to execute all integration tests below automatically.
// The script handles container startup, fixture seeding, test execution, and cleanup.
//
// To run:
//
//	./postgresql_test.sh           # start container, run all TestPostgreSQL* tests, remove container on exit
//	./postgresql_test.sh --keep    # keep container running after tests (useful for debugging)
//
// Integration tests covered:
//
//	TestPostgreSQLTestConnection          — verify DB connection is reachable
//	TestPostgreSQLCheckTargetEmpty_EmptyDB — check whether the target database is empty
//	TestPostgreSQLDumpRestore_SchemaOnly   — produce a schema-only dump and validate the output file
//	TestPostgreSQLInspect                 — retrieve server version and database metadata
//
// To run manually without the script:
//
//	export POSTGRES_TEST_DSN="host=127.0.0.1 port=5432 user=postgres password=pass dbname=testdb_src sslmode=disable"
//	go test . -run TestPostgreSQL -v -timeout 120s

type pgTestEnv struct {
	loc    base.DBMSLocation
	srcDB  string
	destDB string
}

func pgTestSetup(t *testing.T) *pgTestEnv {
	t.Helper()
	if os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Skip("POSTGRES_TEST_DSN not set; skipping PostgreSQL integration tests")
	}

	loc := base.DBMSLocation{
		DBMSType:   base.DBMSTypePostgreSQL,
		Database:   "testdb",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: "localhost", Port: 5432, Username: "postgres", Password: "pass"},
	}
	if v := os.Getenv("POSTGRES_TEST_HOST"); v != "" {
		loc.Direct.Host = v
	}
	if v := os.Getenv("POSTGRES_TEST_USER"); v != "" {
		loc.Direct.Username = v
	}
	if v := os.Getenv("POSTGRES_TEST_PASS"); v != "" {
		loc.Direct.Password = v
	}
	if v := os.Getenv("POSTGRES_TEST_PORT"); v != "" {
		p := 0
		fmt.Sscanf(v, "%d", &p)
		if p > 0 {
			loc.Direct.Port = p
		}
	}

	drv := NewPostgreSQLDriver()
	if err := drv.TestConnection(context.Background(), loc); err != nil {
		t.Skipf("PostgreSQL not reachable: %v", err)
	}

	return &pgTestEnv{
		loc:    loc,
		srcDB:  "testdb_src",
		destDB: "testdb_dst",
	}
}

func TestPostgreSQLTestConnection(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()
	if err := drv.TestConnection(context.Background(), env.loc); err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}
}

func TestPostgreSQLCheckTargetEmpty_EmptyDB(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()
	// Use information_schema — it always has tables but no user tables in public.
	// We just verify no panic occurs.
	_ = drv.CheckTargetEmpty(context.Background(), env.loc)
}

func TestPostgreSQLDumpRestore_SchemaOnly(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	dir := t.TempDir()
	outputPath := dir + "/dump.sql"

	src := env.loc
	src.Database = env.srcDB

	if err := drv.Dump(context.Background(), src, base.ScopeSchemaOnly, outputPath, nil, nil); err != nil {
		t.Fatalf("Dump schema-only: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("dump file is empty")
	}
	// schema-only should not contain COPY blocks
	if strings.Contains(string(content), "FROM STDIN") {
		t.Error("schema-only dump should not contain COPY ... FROM STDIN")
	}
}

func TestPostgreSQLInspect(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	info, err := drv.Inspect(context.Background(), env.loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.ServerVersion == "" {
		t.Error("ServerVersion is empty")
	}
	if info.Database != env.loc.Database {
		t.Errorf("Database = %q, want %q", info.Database, env.loc.Database)
	}
}

// sshTunnelPG returns a DBMSLocation with SSH tunnel access type for unit tests.
func sshTunnelPG() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypePostgreSQL,
		Database:   "testdb",
		AccessType: base.AccessTypeSSHTunnel,
		SSHTunnel: &base.SSHTunnelConfig{
			SSH:      &base.SSHConfig{Host: "ssh.example.com", Port: 22, Username: "deploy"},
			DBHost:   "127.0.0.1",
			DBPort:   5432,
			Username: "postgres",
			Password: "pass",
		},
	}
}

// directPG returns a DBMSLocation with direct access type for unit tests.
func directPG() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypePostgreSQL,
		Database:   "testdb",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: "localhost", Port: 5432, Username: "postgres", Password: "pass"},
	}
}

// =============================================================================
// Inspect metric integration tests (require POSTGRES_TEST_DSN + seeded fixtures)
// =============================================================================

func TestPostgreSQLInspect_DefaultOmitsDetail(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	loc := env.loc
	loc.Database = env.srcDB
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(info.Tables) == 0 {
		t.Fatal("expected non-empty table list")
	}
	for _, tbl := range info.Tables {
		if len(tbl.Columns) != 0 || len(tbl.Indexes) != 0 {
			t.Errorf("table %q: default Inspect must omit columns/indexes", tbl.Name)
		}
	}
	if info.Views != nil || info.MaterializedViews != nil || info.Functions != nil ||
		info.Procedures != nil || info.Triggers != nil || info.Sequences != nil ||
		info.Types != nil || info.Extensions != nil || info.Rules != nil || info.ForeignKeys != nil {
		t.Errorf("default Inspect must omit schema-object lists, got %+v", info)
	}
}

func TestPostgreSQLInspect_ColumnsIndexes(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{Columns: bp(true), Indexes: bp(true)}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var users *base.TableInfo
	for i := range info.Tables {
		if info.Tables[i].Name == "users" {
			users = &info.Tables[i]
		}
	}
	if users == nil {
		t.Fatal("users table not found")
	}
	if len(users.Columns) == 0 {
		t.Error("expected columns for users when Columns=true")
	}
	if info.Views != nil || info.Sequences != nil {
		t.Error("columns/indexes metric must not populate schema-object lists")
	}
}

func TestPostgreSQLInspect_SchemaObjects(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{
		Views: bp(true), MaterializedViews: bp(true), Functions: bp(true),
		Procedures: bp(true), Triggers: bp(true), Sequences: bp(true),
		Types: bp(true), Extensions: bp(true), Rules: bp(true), ForeignKeys: bp(true),
	}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !containsAll(info.Views, "active_users") {
		t.Errorf("Views = %v, want active_users", info.Views)
	}
	if !containsAll(info.MaterializedViews, "mv_user_count") {
		t.Errorf("MaterializedViews = %v, want mv_user_count", info.MaterializedViews)
	}
	if !containsAll(info.Functions, "user_count") {
		t.Errorf("Functions = %v, want user_count", info.Functions)
	}
	if !containsAll(info.Procedures, "add_user") {
		t.Errorf("Procedures = %v, want add_user", info.Procedures)
	}
	if !containsAll(info.Triggers, "trg_orders_ai") {
		t.Errorf("Triggers = %v, want trg_orders_ai", info.Triggers)
	}
	if !containsAll(info.Sequences, "seq_demo") {
		t.Errorf("Sequences = %v, want seq_demo", info.Sequences)
	}
	if !containsAll(info.Types, "mood") {
		t.Errorf("Types = %v, want mood", info.Types)
	}
	if !containsAll(info.Extensions, "pgcrypto") {
		t.Errorf("Extensions = %v, want pgcrypto", info.Extensions)
	}
	if !containsAll(info.Rules, "rule_noop") {
		t.Errorf("Rules = %v, want rule_noop", info.Rules)
	}
	if len(info.ForeignKeys) == 0 {
		t.Error("expected at least one foreign key")
	}
}

func TestPostgreSQLInspect_RowCountExact(t *testing.T) {
	env := pgTestSetup(t)
	drv := NewPostgreSQLDriver()

	loc := env.loc
	loc.Database = env.srcDB
	loc.Metric = &base.MetricOption{PostgreSQL: &base.PostgreSQLMetric{RowCountExact: bp(true)}}
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, tbl := range info.Tables {
		if tbl.Name == "users" && tbl.RowCount != 2 {
			t.Errorf("users RowCount = %d, want 2 (exact)", tbl.RowCount)
		}
	}
}

func TestPGRowExcludeClause(t *testing.T) {
	orders := pgTableRef{Schema: "public", Name: "orders"}

	if got := pgRowExcludeClause(nil, orders); got != "" {
		t.Errorf("nil filter = %q, want empty", got)
	}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeRows("orders", "created_at < '2023-01-01'"),
		filterpkg.ExcludeRows("orders", "status = 'cancelled'"),
	}}
	got := pgRowExcludeClause(f, orders)
	want := "((created_at < '2023-01-01') IS NOT TRUE) AND ((status = 'cancelled') IS NOT TRUE)"
	if got != want {
		t.Errorf("multi =\n  %q\nwant\n  %q", got, want)
	}
}

// TestPGFilterRuleForms covers the two spellings a rule may use. A bare rule
// applies in every schema, which is what a rule written before schemas were
// selectable meant; a qualified rule singles out one of two same-named tables.
func TestPGFilterRuleForms(t *testing.T) {
	publicUsers := pgTableRef{Schema: "public", Name: "users"}
	salesUsers := pgTableRef{Schema: "sales", Name: "users"}

	bare := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users"),
	}}
	if !pgTableExcluded(bare, publicUsers) || !pgTableExcluded(bare, salesUsers) {
		t.Error("a bare rule should exclude the table in every schema")
	}

	qualified := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("sales.users"),
	}}
	if pgTableExcluded(qualified, publicUsers) {
		t.Error("a qualified rule must not reach another schema")
	}
	if !pgTableExcluded(qualified, salesUsers) {
		t.Error("a qualified rule should exclude its own table")
	}
}

// TestPGWarnRulesOutOfScope covers the report a rule earns when its schema
// qualifier names a schema the dump does not cover, and the silence every other
// rule keeps: a bare rule applies in each schema, and a qualified one that hits
// is doing its job.
func TestPGWarnRulesOutOfScope(t *testing.T) {
	sc := newPgDumpScope([]string{"sales"}, nil)

	capture := func(f *filterpkg.DBMSFilterOption) string {
		var buf bytes.Buffer
		prev := logger
		SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		defer SetLogger(prev)
		pgWarnRulesOutOfScope(f, sc)
		return buf.String()
	}

	out := capture(&filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("public.orders"),
	}})
	if !strings.Contains(out, "public.orders") {
		t.Errorf("an out-of-scope rule should be reported, got %q", out)
	}

	for name, f := range map[string]*filterpkg.DBMSFilterOption{
		"bare rule":      {Rules: []filterpkg.FilterRule{filterpkg.ExcludeTable("orders")}},
		"in-scope rule":  {Rules: []filterpkg.FilterRule{filterpkg.ExcludeTable("sales.orders")}},
		"in-scope index": {Rules: []filterpkg.FilterRule{filterpkg.ExcludeIndex("sales.orders", "idx")}},
	} {
		if out := capture(f); out != "" {
			t.Errorf("%s should not be reported, got %q", name, out)
		}
	}
	if out := capture(nil); out != "" {
		t.Errorf("nil filter should not be reported, got %q", out)
	}
}

// TestPGQualifiedRuleSingleSchema pins the rule that a qualified rule is matched
// against the table's own schema, not against the form the dump renders names
// in. A one-schema dump writes bare names, but a caller who selected only
// "sales" and wrote "sales.users" has named that very table and must be obeyed —
// and the ssh-tunnel path, which turns the same rule into a pg_dump -T pattern
// with no scope in hand, obeys it either way.
func TestPGQualifiedRuleSingleSchema(t *testing.T) {
	qualified := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("sales.users"),
	}}
	if !pgTableExcluded(qualified, pgTableRef{Schema: "sales", Name: "users"}) {
		t.Error("a qualified rule must apply when its schema is the only one dumped")
	}
	if pgTableExcluded(qualified, pgTableRef{Schema: "public", Name: "users"}) {
		t.Error("a qualified rule must still not reach another schema")
	}
}

// =============================================================================
// FK safety: parent-side filtering, ON_ERROR_STOP, --disable-triggers
// =============================================================================

func TestPgAnyColExcluded(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users", "password_hash", "ssn"),
	}}
	cases := []struct {
		table, cols string
		want        bool
	}{
		{"users", "id", false},
		{"users", "ssn", true},
		{"users", "id,ssn", true},
		{"orders", "ssn", false}, // rule is scoped to users
	}
	for _, c := range cases {
		tbl := pgTableRef{Schema: "public", Name: c.table}
		if got := pgAnyColExcluded(f, tbl, c.cols); got != c.want {
			t.Errorf("pgAnyColExcluded(%q, %q) = %v, want %v", c.table, c.cols, got, c.want)
		}
	}
	if pgAnyColExcluded(nil, pgTableRef{Schema: "public", Name: "users"}, "ssn") {
		t.Error("pgAnyColExcluded with nil filter should be false")
	}
}

func TestBuildPGRestoreCmd_HasOnErrorStop(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildRestoreCmd(sshTunnelPG())
	if !strings.Contains(cmd, "ON_ERROR_STOP=1") {
		t.Errorf("BuildRestoreCmd must set ON_ERROR_STOP=1 so a failed restore exits non-zero: %s", cmd)
	}
}

func TestBuildPGDumpCmd_DataOnly_DisablesTriggers(t *testing.T) {
	drv := NewPostgreSQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelPG(), base.ScopeDataOnly)
	if !strings.Contains(cmd, "--disable-triggers") {
		t.Errorf("data-only dump must pass --disable-triggers to avoid FK violations: %s", cmd)
	}
}

func TestBuildPGDumpCmd_NonDataOnly_NoDisableTriggers(t *testing.T) {
	drv := NewPostgreSQLDriver()
	for _, scope := range []string{base.ScopeSchemaOnly, base.ScopeFull} {
		cmd := drv.BuildDumpCmd(sshTunnelPG(), scope)
		if strings.Contains(cmd, "--disable-triggers") {
			t.Errorf("scope %s must not pass --disable-triggers (pg_dump rejects it outside --data-only): %s", scope, cmd)
		}
	}
}

// =============================================================================
// Unit tests for the SSH inspect transport (no PostgreSQL server required)
// =============================================================================

func TestPGJSONScalarString_Types(t *testing.T) {
	// A decoded JSON null must read as the empty string rather than "<nil>",
	// since callers compare the result directly against "".
	if got := pgJSONScalarString(nil); got != "" {
		t.Errorf("nil = %q, want empty string", got)
	}
	if got := pgJSONScalarString("v_legacy"); got != "v_legacy" {
		t.Errorf("string = %q, want %q", got, "v_legacy")
	}
	if got := pgJSONScalarString(true); got != "true" {
		t.Errorf("true = %q, want %q", got, "true")
	}
	if got := pgJSONScalarString(false); got != "false" {
		t.Errorf("false = %q, want %q", got, "false")
	}
}

func TestPGTruthy(t *testing.T) {
	// "t" is what the tab transport yields, "true" what json_agg yields; both
	// must be accepted so the two helpers stay interchangeable.
	for _, s := range []string{"t", "true", " true "} {
		if !pgTruthy(s) {
			t.Errorf("pgTruthy(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"f", "false", "", "yes"} {
		if pgTruthy(s) {
			t.Errorf("pgTruthy(%q) = true, want false", s)
		}
	}
}

func TestPGSortRows_Numeric(t *testing.T) {
	// ordinal_position must sort numerically: a lexicographic sort would put
	// "10" before "2" and scramble the column order.
	rows := []map[string]string{
		{"ordinal_position": "10", "column_name": "j"},
		{"ordinal_position": "2", "column_name": "b"},
		{"ordinal_position": "1", "column_name": "a"},
	}
	pgSortRows(rows, "ordinal_position")

	want := []string{"a", "b", "j"}
	for i, w := range want {
		if rows[i]["column_name"] != w {
			t.Errorf("row %d = %q, want %q", i, rows[i]["column_name"], w)
		}
	}
}

func TestPGSortRows_Lexicographic(t *testing.T) {
	rows := []map[string]string{
		{"name": "v_orders"},
		{"name": "v_audit"},
		{"name": "v_users"},
	}
	pgSortRows(rows, "name")

	want := []string{"v_audit", "v_orders", "v_users"}
	for i, w := range want {
		if rows[i]["name"] != w {
			t.Errorf("row %d = %q, want %q", i, rows[i]["name"], w)
		}
	}
}

func TestPGColumnQuery_CarriesPrimaryKeyAndComment(t *testing.T) {
	// The SSH and direct paths share this query, so every field either one needs
	// must be present in the single shared definition.
	q := pgColumnQuery("public")
	for _, want := range []string{"is_primary", "comment", "ordinal_position", "pg_description"} {
		if !strings.Contains(q, want) {
			t.Errorf("pgColumnQuery is missing %q", want)
		}
	}
	// Joining key_column_usage at the top level would duplicate a column that
	// belongs to both a primary key and a unique constraint.
	if strings.Contains(q, "LEFT JOIN information_schema.key_column_usage") {
		t.Error("pgColumnQuery must resolve the primary key via EXISTS, not a top-level join")
	}
}

func TestPGIndexQuery_CarriesColumnList(t *testing.T) {
	q := pgIndexQuery("public")
	for _, want := range []string{"index_columns", "array_to_string", "index_type"} {
		if !strings.Contains(q, want) {
			t.Errorf("pgIndexQuery is missing %q", want)
		}
	}
}

// TestPGPerTableQueries_ScopedToOneSchema guards the reason these became
// functions: a predicate on the table name alone would match a same-named table
// in every schema of the database.
func TestPGPerTableQueries_ScopedToOneSchema(t *testing.T) {
	for _, q := range []string{pgColumnQuery("sales"), pgIndexQuery("sales")} {
		if !strings.Contains(q, "='sales'") {
			t.Errorf("query is not scoped to the given schema: %s", q)
		}
	}
	// A schema name carrying a quote must not break out of the literal.
	if !strings.Contains(pgColumnQuery("it's"), "='it''s'") {
		t.Error("schema name is not escaped into the SQL literal")
	}
}

func TestPGObjectQueries_AliasNameColumn(t *testing.T) {
	// json_agg keys rows by output column alias, so every enumeration query must
	// expose its single column as "name".
	schemas := []string{"public"}
	queries := map[string]string{
		"foreignKeys":       pgObjectQueryForeignKeys(schemas),
		"views":             pgObjectQueryViews(schemas),
		"materializedViews": pgObjectQueryMatViews(schemas),
		"functions":         pgObjectQueryRoutines(schemas, "f"),
		"procedures":        pgObjectQueryRoutines(schemas, "p"),
		"triggers":          pgObjectQueryTriggers(schemas),
		"sequences":         pgObjectQuerySequences(schemas),
		"types":             pgObjectQueryTypes(schemas),
		"extensions":        pgObjectQueryExtensions,
		"rules":             pgObjectQueryRules(schemas),
	}
	for metric, q := range queries {
		if !strings.Contains(q, " AS name") {
			t.Errorf("%s query does not alias its column as name: %s", metric, q)
		}
	}
}

// TestPGObjectQueries_QualifyAndScope covers the contract buildTeardownScript
// depends on: every schema-scoped inventory returns a server-quoted,
// schema-qualified name, and is restricted to the schemas being inspected.
func TestPGObjectQueries_QualifyAndScope(t *testing.T) {
	schemas := []string{"public", "sales"}
	queries := map[string]string{
		"foreignKeys":       pgObjectQueryForeignKeys(schemas),
		"views":             pgObjectQueryViews(schemas),
		"materializedViews": pgObjectQueryMatViews(schemas),
		"functions":         pgObjectQueryRoutines(schemas, "f"),
		"procedures":        pgObjectQueryRoutines(schemas, "p"),
		"triggers":          pgObjectQueryTriggers(schemas),
		"sequences":         pgObjectQuerySequences(schemas),
		"types":             pgObjectQueryTypes(schemas),
		"rules":             pgObjectQueryRules(schemas),
	}
	for metric, q := range queries {
		if !strings.Contains(q, "quote_ident(") || !strings.Contains(q, "|| '.' ||") {
			t.Errorf("%s query does not return a quoted, schema-qualified name: %s", metric, q)
		}
		if !strings.Contains(q, "IN ('public', 'sales')") {
			t.Errorf("%s query is not scoped to the inspected schemas: %s", metric, q)
		}
	}

	// An extension belongs to the database, not to a schema: DROP EXTENSION
	// takes a bare name, and the dump needs every extension the selected schemas
	// might depend on — including one installed outside them.
	if strings.Contains(pgObjectQueryExtensions, "|| '.' ||") {
		t.Error("the extension inventory must not be schema-qualified")
	}
	if strings.Contains(pgObjectQueryExtensions, "nspname") {
		t.Error("the extension inventory must not be filtered by schema")
	}
}

func TestPgSchemaLiteralList(t *testing.T) {
	if got := pgSchemaLiteralList([]string{"public", "sales"}); got != "('public', 'sales')" {
		t.Errorf("pgSchemaLiteralList = %q", got)
	}
	if got := pgSchemaLiteralList([]string{"it's"}); got != "('it''s')" {
		t.Errorf("pgSchemaLiteralList does not escape quotes: %q", got)
	}
	// An empty list must still parse as SQL, and must match nothing rather than
	// silently widening the predicate to every schema.
	if got := pgSchemaLiteralList(nil); got != "('')" {
		t.Errorf("pgSchemaLiteralList(nil) = %q, want a list matching nothing", got)
	}
}

func TestPgQualifyIdent(t *testing.T) {
	if got := pgQualifyIdent("sales", "orders"); got != `"sales"."orders"` {
		t.Errorf("pgQualifyIdent = %q", got)
	}
	// A single-schema dump writes bare names so the restore's search_path can
	// place them, which is what makes a schema rename possible.
	if got := pgQualifyIdent("", "orders"); got != `"orders"` {
		t.Errorf("pgQualifyIdent with no schema = %q", got)
	}
	if got := pgQualifyIdent(`a"b`, `c"d`); got != `"a""b"."c""d"` {
		t.Errorf("pgQualifyIdent does not escape embedded quotes: %q", got)
	}
}

// =============================================================================
// Per-schema statistics
// =============================================================================

func TestPgFillSchemaStats_ZeroFillsAndKeepsOrder(t *testing.T) {
	found := map[string]base.PgSchemaInfo{
		"sales": {Name: "sales", TotalSize: 100, DataSize: 60, IndexSize: 40, TableCount: 2},
	}
	// "hr" holds no tables, so GROUP BY produced no row for it — it must still
	// appear, at zero, because the caller selected it.
	got := pgFillSchemaStats([]string{"sales", "hr"}, found)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Name != "sales" || got[1].Name != "hr" {
		t.Errorf("order not preserved: %v", got)
	}
	if got[1] != (base.PgSchemaInfo{Name: "hr"}) {
		t.Errorf("empty schema should be zero-filled, got %+v", got[1])
	}
}

func TestPgSchemaStatsQuery_GroupsBySchema(t *testing.T) {
	q := pgSchemaStatsQuery([]string{"public", "sales"})
	for _, want := range []string{"GROUP BY n.nspname", "IN ('public', 'sales')", "AS table_count"} {
		if !strings.Contains(q, want) {
			t.Errorf("pgSchemaStatsQuery is missing %q: %s", want, q)
		}
	}
}

// =============================================================================
// DescribeTable schema resolution
// =============================================================================

func TestPgResolveTableSchema(t *testing.T) {
	got, err := pgResolveTableSchema("shop", "orders", []string{"sales"})
	if err != nil || got != "sales" {
		t.Errorf("single match: got (%q, %v), want (\"sales\", nil)", got, err)
	}

	if _, err := pgResolveTableSchema("shop", "orders", nil); err == nil {
		t.Error("a table in none of the selected schemas should be an error")
	}

	// Picking one arbitrarily would describe a different table than the caller
	// meant, so an ambiguous name is reported instead.
	_, err = pgResolveTableSchema("shop", "users", []string{"public", "sales"})
	if err == nil {
		t.Fatal("a table present in several schemas should be an error")
	}
	for _, want := range []string{"public, sales", "set pgSchema"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// =============================================================================
// CREATE DATABASE / DROP DATABASE statement building
// =============================================================================

func TestPgBuildCreateDatabase(t *testing.T) {
	cases := []struct {
		label string
		name  string
		opt   base.PostgreSQLCreateOption
		want  string
	}{
		{
			label: "no options",
			name:  "analytics",
			want:  `CREATE DATABASE "analytics"`,
		},
		{
			label: "owner only",
			name:  "analytics",
			opt:   base.PostgreSQLCreateOption{Owner: "ops"},
			want:  `CREATE DATABASE "analytics" WITH OWNER "ops"`,
		},
		{
			label: "template precedes encoding and locale",
			name:  "analytics",
			opt: base.PostgreSQLCreateOption{
				Encoding: "UTF8", LcCollate: "en_US.utf8", LcCtype: "en_US.utf8", Template: "template0",
			},
			want: `CREATE DATABASE "analytics" WITH TEMPLATE "template0" ENCODING 'UTF8' ` +
				`LC_COLLATE 'en_US.utf8' LC_CTYPE 'en_US.utf8'`,
		},
		{
			label: "every clause",
			name:  "analytics",
			opt: base.PostgreSQLCreateOption{
				Owner: "ops", Encoding: "UTF8", Template: "template0",
			},
			want: `CREATE DATABASE "analytics" WITH OWNER "ops" TEMPLATE "template0" ENCODING 'UTF8'`,
		},
		{
			label: "name needing quotes",
			name:  "Reporting Warehouse",
			want:  `CREATE DATABASE "Reporting Warehouse"`,
		},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := pgBuildCreateDatabase(c.name, c.opt); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestPgBuildCreateDatabase_TemplateBeforeEncoding(t *testing.T) {
	// The server reads the clauses in order: TEMPLATE has to appear before the
	// encoding and locale settings it makes acceptable.
	got := pgBuildCreateDatabase("analytics", base.PostgreSQLCreateOption{
		Encoding: "UTF8", Template: "template0",
	})
	if strings.Index(got, "TEMPLATE") > strings.Index(got, "ENCODING") {
		t.Errorf("TEMPLATE must precede ENCODING: %q", got)
	}
}

func TestPgTerminateBackendsQuery_SparesOwnSession(t *testing.T) {
	// Terminating our own backend would kill the session that is about to issue
	// the DROP, so the query excludes pg_backend_pid().
	if !strings.Contains(pgTerminateBackendsQuery, "pg_backend_pid()") {
		t.Errorf("query must exclude our own pid: %q", pgTerminateBackendsQuery)
	}
	// It is used instead of WITH (FORCE), which only exists on PostgreSQL 13+.
	if strings.Contains(pgTerminateBackendsQuery, "FORCE") {
		t.Errorf("query should not rely on the version-gated FORCE clause: %q", pgTerminateBackendsQuery)
	}
}

// =============================================================================
// Shared test helpers
// =============================================================================
// containsAll reports whether every want is present in got.
func containsAll(got []string, want ...string) bool {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// =============================================================================
// pgDumpScope — the two shapes of dump
// =============================================================================

// A single-schema dump writes bare names and no CREATE SCHEMA, which is what
// lets the restore place the whole file through its own search_path.
func TestPgDumpScope_SingleSchema(t *testing.T) {
	sc := newPgDumpScope([]string{"sales"}, nil)

	if sc.qualify {
		t.Error("one schema should not qualify")
	}
	if got := sc.ident("sales", "orders"); got != `"orders"` {
		t.Errorf("ident = %q, want a bare name", got)
	}
	if got := sc.preamble(); got != "" {
		t.Errorf("preamble = %q, want none", got)
	}
	if got := sc.renameTail(); got != "" {
		t.Errorf("renameTail = %q, want none", got)
	}
}

// Renaming one schema produces no statements of its own either: the restore
// redirects the bare names, so the dump is identical whether or not the name
// changes.
func TestPgDumpScope_SingleSchemaRenameIsRestoreSide(t *testing.T) {
	sc := newPgDumpScope([]string{"sales"}, map[string]string{"sales": "sales_prod"})

	if got := sc.ident("sales", "orders"); got != `"orders"` {
		t.Errorf("ident = %q, want a bare name", got)
	}
	if got := sc.renameTail(); got != "" {
		t.Errorf("renameTail = %q, want none - a single schema is redirected by the restore", got)
	}
}

func TestPgDumpScope_MultipleSchemas(t *testing.T) {
	sc := newPgDumpScope([]string{"public", "sales"}, nil)

	if !sc.qualify {
		t.Error("several schemas must qualify")
	}
	if got := sc.ident("sales", "orders"); got != `"sales"."orders"` {
		t.Errorf("ident = %q, want a qualified name", got)
	}
	// The schemas have to exist before the first table is created.
	want := "CREATE SCHEMA IF NOT EXISTS \"public\";\nCREATE SCHEMA IF NOT EXISTS \"sales\";\n\n"
	if got := sc.preamble(); got != want {
		t.Errorf("preamble =\n%q\nwant\n%q", got, want)
	}
	if got := sc.renameTail(); got != "" {
		t.Errorf("renameTail = %q, want none when nothing is renamed", got)
	}
}

// A multi-schema dump names objects by their source schema and renames the
// schemas themselves at the end. PostgreSQL tracks what a view or a constraint
// depends on by object identity, so the rename carries every internal reference
// with it - which is what keeps this driver out of the business of editing SQL
// text it did not write.
func TestPgDumpScope_MultiSchemaRenameIsATail(t *testing.T) {
	sc := newPgDumpScope([]string{"public", "sales"}, map[string]string{"sales": "sales_prod"})

	if got := sc.ident("sales", "orders"); got != `"sales"."orders"` {
		t.Errorf("ident = %q, want the source schema - the rename happens afterwards", got)
	}
	if !strings.Contains(sc.preamble(), `CREATE SCHEMA IF NOT EXISTS "sales";`) {
		t.Errorf("preamble should create the source schema:\n%s", sc.preamble())
	}

	tail := sc.renameTail()
	if !strings.Contains(tail, `ALTER SCHEMA "sales" RENAME TO "sales_prod";`) {
		t.Errorf("renameTail is missing the rename:\n%s", tail)
	}
	// "public" is not renamed, so it must not be touched.
	if strings.Contains(tail, `"public"`) {
		t.Errorf("renameTail should leave unrenamed schemas alone:\n%s", tail)
	}
}

func TestPgDumpScope_Out(t *testing.T) {
	sc := newPgDumpScope([]string{"sales", "hr"}, map[string]string{"sales": "sales_prod"})
	if got := sc.out("sales"); got != "sales_prod" {
		t.Errorf("out(sales) = %q", got)
	}
	if got := sc.out("hr"); got != "hr" {
		t.Errorf("out(hr) = %q, want the name unchanged", got)
	}
}

func TestPgDumpScope_Covers(t *testing.T) {
	sc := newPgDumpScope([]string{"public", "sales"}, nil)
	if !sc.covers("sales") {
		t.Error("covers should find a schema in the dump")
	}
	if sc.covers("hr") {
		t.Error("covers must not claim a schema outside the dump")
	}
}

// =============================================================================
// Reading names back out of a dump
// =============================================================================

func TestPgExtractQualifiedNames(t *testing.T) {
	cases := []struct {
		label string
		stmt  string
		fn    func(string) string
		want  string
	}{
		{"bare table", `CREATE TABLE IF NOT EXISTS "users" (id int);`, pgExtractCreateTable, "users"},
		{"unquoted table", `CREATE TABLE users (id int);`, pgExtractCreateTable, "users"},
		{"qualified table", `CREATE TABLE IF NOT EXISTS "sales"."orders" (id int);`, pgExtractCreateTable, "sales.orders"},
		{"bare copy", `COPY "users" (id) FROM STDIN;`, pgExtractCopyTable, "users"},
		{"qualified copy", `COPY "sales"."orders" FROM STDIN;`, pgExtractCopyTable, "sales.orders"},
		// A doubled quote inside an identifier is its SQL spelling, not part of
		// the name.
		{"embedded quote", `CREATE TABLE "od""d" (id int);`, pgExtractCreateTable, `od"d`},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := c.fn(c.stmt); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The materialized view name is executed rather than displayed, so it comes back
// as separately quoted parts: quoting a joined "sales.mv_daily" whole would name
// one view with a dot in it.
func TestPgExtractMatViewIdent(t *testing.T) {
	cases := []struct{ stmt, want string }{
		{`CREATE MATERIALIZED VIEW "mv_daily" AS SELECT 1;`, `"mv_daily"`},
		{`CREATE MATERIALIZED VIEW "sales"."mv_daily" AS SELECT 1;`, `"sales"."mv_daily"`},
		{`CREATE MATERIALIZED VIEW sales.mv_daily AS SELECT 1;`, `"sales"."mv_daily"`},
	}
	for _, c := range cases {
		if got := pgExtractMatViewIdent(c.stmt); got != c.want {
			t.Errorf("pgExtractMatViewIdent(%q) = %q, want %q", c.stmt, got, c.want)
		}
	}
}

// =============================================================================
// PrepareTarget — role provisioning and schema-level grants
// =============================================================================

// PostgreSQL has no CREATE ROLE ... IF NOT EXISTS. The statement builder must
// emit the plain form; the caller decides whether to run it from a separate
// existence query.
func TestPgCreateRole_HasNoIfNotExistsClause(t *testing.T) {
	stmt := pgCreateRole("migrator", "s3cr3t")
	if strings.Contains(strings.ToUpper(stmt), "IF NOT EXISTS") {
		t.Errorf("PostgreSQL rejects IF NOT EXISTS on CREATE ROLE: %s", stmt)
	}
	if want := `CREATE ROLE "migrator" WITH LOGIN PASSWORD 's3cr3t'`; stmt != want {
		t.Errorf("pgCreateRole =\n  %q\nwant\n  %q", stmt, want)
	}
}

func TestPgCreateRole_EscapesCredentials(t *testing.T) {
	// A quote in the password must stay inside the literal, and one in the role
	// name inside the identifier.
	stmt := pgCreateRole(`ad"min`, "it's")
	if !strings.Contains(stmt, `"ad""min"`) {
		t.Errorf("role name is not quoted safely: %s", stmt)
	}
	if !strings.Contains(stmt, `'it''s'`) {
		t.Errorf("password is not escaped safely: %s", stmt)
	}
}

func TestPgRoleExistsQuery_TakesABoundName(t *testing.T) {
	// The SSH path rewrites $1 into a literal, so the placeholder has to be there
	// and appear exactly once.
	if strings.Count(pgRoleExistsQuery, "$1") != 1 {
		t.Errorf("pgRoleExistsQuery should carry exactly one $1: %s", pgRoleExistsQuery)
	}
}

// From PostgreSQL 15 the public schema no longer grants CREATE to PUBLIC, so a
// role holding ALL PRIVILEGES ON DATABASE still cannot create a table there.
// The schema-level grant is what closes that gap.
func TestPgGrantSchemaTargets(t *testing.T) {
	existing := []string{"public", "sales"}

	// No selection: the dump lands in public by default, so that is what needs
	// granting.
	if got := pgGrantSchemaTargets(base.DBMSLocation{}, existing); len(got) != 1 || got[0] != "public" {
		t.Errorf("unselected location = %v, want [public]", got)
	}

	// A selection grants on the named schemas instead.
	loc := base.DBMSLocation{PgSchema: []string{"sales"}}
	if got := pgGrantSchemaTargets(loc, existing); len(got) != 1 || got[0] != "sales" {
		t.Errorf("selected location = %v, want [sales]", got)
	}

	// A named schema that does not exist yet is left out: the migration creates
	// it and thereby owns it, and granting on an absent schema would fail.
	loc = base.DBMSLocation{PgSchema: []string{"sales", "hr"}}
	got := pgGrantSchemaTargets(loc, existing)
	if len(got) != 1 || got[0] != "sales" {
		t.Errorf("got %v, want only the schema that exists", got)
	}

	// A database without a public schema has nothing to grant on up front.
	if got := pgGrantSchemaTargets(base.DBMSLocation{}, []string{"sales"}); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestPgGrantSchema(t *testing.T) {
	// USAGE plus CREATE is what ALL means for a schema, and both are needed:
	// USAGE to see what is in it, CREATE to add to it.
	if want := `GRANT ALL ON SCHEMA "sales" TO "migrator"`; pgGrantSchema("sales", "migrator") != want {
		t.Errorf("pgGrantSchema = %q, want %q", pgGrantSchema("sales", "migrator"), want)
	}
}

// =============================================================================
// psql meta-commands (pg_dump 13.22 / 14.19 / 15.14 / 16.10 / 17.6 and later)
// =============================================================================

func TestPgIsMetaCommand(t *testing.T) {
	cases := map[string]bool{
		`\restrict xZss3bd7frDbP8qLsa2vt8622yOJWu08`:   true,
		`\unrestrict xZss3bd7frDbP8qLsa2vt8622yOJWu08`: true,
		`\connect app`:              true,
		`\.`:                        false, // COPY terminator, not a meta-command
		`SELECT 1;`:                 false,
		``:                          false,
		"\\restrict tok\nSELECT 1;": false, // more than one line: not a bare meta-command
	}
	for in, want := range cases {
		if got := pgIsMetaCommand(in); got != want {
			t.Errorf("pgIsMetaCommand(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitPGStatements_DropsRestrictLines(t *testing.T) {
	// The shape pg_dump writes since the August 2025 minor releases.
	script := "--\n-- PostgreSQL database dump\n--\n\n" +
		"\\restrict abc123\n\n" +
		"SET statement_timeout = 0;\n" +
		"CREATE TABLE a (id int);\n" +
		"\\unrestrict abc123\n"

	for _, stmt := range splitPGStatements(script) {
		if strings.Contains(stmt, `\restrict`) || strings.Contains(stmt, `\unrestrict`) {
			t.Errorf("meta-command survived the split: %q", stmt)
		}
	}
	var joined string
	for _, stmt := range splitPGStatements(script) {
		joined += stmt + "\n"
	}
	if !strings.Contains(joined, "CREATE TABLE a (id int)") {
		t.Errorf("statements after the meta-command were lost: %q", joined)
	}
}

func TestSplitPGStatements_KeepsCopyTerminator(t *testing.T) {
	script := "COPY a (id) FROM stdin;\n1\n2\n\\.\n"
	stmts := splitPGStatements(script)
	if len(stmts) != 1 || !strings.Contains(stmts[0], `\.`) {
		t.Errorf("COPY block must stay intact, got %q", stmts)
	}
}

func TestRestoreCopyDetection_MatchesSplitter(t *testing.T) {
	// pg_dump writes the clause in lower case, this driver's own dump in upper.
	// Both have to be recognised, or the rows below the header reach the server
	// as SQL and fail with a syntax error on the first value.
	for _, header := range []string{
		"COPY public.customers (id, name) FROM stdin;",
		"COPY public.customers (id, name) FROM STDIN;",
		"copy public.customers (id, name) from stdin;",
	} {
		block := header + "\n1\tAlice\n\\.\n"
		stmts := splitPGStatements(block)
		if len(stmts) != 1 {
			t.Fatalf("%q: expected one statement, got %d", header, len(stmts))
		}
		firstLine, _, ok := strings.Cut(strings.TrimSpace(stmts[0]), "\n")
		if !ok || !pgIsCopyFromStdin(firstLine) {
			t.Errorf("%q: restore loop would not treat this as a COPY block", header)
		}
	}
}

// =============================================================================
// TLS
// =============================================================================

// TestPGBuildDSN_TLSModes checks that a mode reaches the DSN unchanged. This
// vocabulary is libpq's own, which is why PostgreSQL translates nothing while
// the other drivers translate into it.
func TestPGBuildDSN_TLSModes(t *testing.T) {
	d := &PostgreSQLDriver{}
	modes := []string{
		"", // an empty mode is disable
		base.TLSModeDisable,
		base.TLSModePrefer,
		base.TLSModeRequire,
		base.TLSModeVerifyCA,
		base.TLSModeVerifyFull,
	}

	for _, mode := range modes {
		name := mode
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			dsn, cleanup, err := d.buildDSN(base.DBMSLocation{
				DBMSType:   base.DBMSTypePostgreSQL,
				AccessType: base.AccessTypeDirect,
				Database:   "app",
				Direct:     &base.DirectConfig{Host: "db", TLSMode: mode},
			})
			if err != nil {
				t.Fatalf("buildDSN: %v", err)
			}
			defer cleanup()

			want := mode
			if want == "" {
				want = base.TLSModeDisable
			}
			if !strings.Contains(dsn, "sslmode="+want+" ") && !strings.HasSuffix(dsn, "sslmode="+want) {
				t.Errorf("DSN does not carry sslmode=%s:\n%s", want, dsn)
			}
			// No CA was given, so libpq is left to its own default locations.
			if strings.Contains(dsn, "sslrootcert=") {
				t.Errorf("DSN carries sslrootcert without a CA:\n%s", dsn)
			}
		})
	}
}

func TestPGBuildDSN_RejectsAnUnreadableCA(t *testing.T) {
	// Reported here rather than as a connection failure much later.
	d := &PostgreSQLDriver{}
	_, cleanup, err := d.buildDSN(base.DBMSLocation{
		DBMSType:   base.DBMSTypePostgreSQL,
		AccessType: base.AccessTypeDirect,
		Database:   "app",
		Direct: &base.DirectConfig{
			Host: "db", TLSMode: base.TLSModeVerifyFull, TLSCAFile: "/nonexistent/ca.pem",
		},
	})
	cleanup()
	if err == nil {
		t.Fatal("expected an error for a CA file that is not there")
	}
}

// TestPgIsPostDataStmt_HoldsBackOnlyEnforcement pins what the restore defers
// until after the rows. Getting this wrong is not a cosmetic error: holding back
// a CREATE TABLE would break every statement after it, and failing to hold back
// a FK constraint reintroduces the ordering failure the deferral exists to avoid.
func TestPgIsPostDataStmt_HoldsBackOnlyEnforcement(t *testing.T) {
	deferred := []string{
		`ALTER TABLE "orders" ADD CONSTRAINT "fk_cust" FOREIGN KEY ("cust_id") REFERENCES "customers" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION`,
		"alter table sales.orders add constraint fk foreign key (a) references sales.c (b)",
		`CREATE TRIGGER "audit_ins" AFTER INSERT ON "orders" FOR EACH ROW EXECUTE FUNCTION audit()`,
		"CREATE CONSTRAINT TRIGGER t AFTER INSERT ON x DEFERRABLE FOR EACH ROW EXECUTE FUNCTION f()",
		"\n\t  CREATE TRIGGER leading_whitespace AFTER INSERT ON x FOR EACH ROW EXECUTE FUNCTION f()",
	}
	for _, stmt := range deferred {
		if !pgIsPostDataStmt(stmt) {
			t.Errorf("should be deferred but was not:\n%s", stmt)
		}
	}

	immediate := []string{
		`CREATE TABLE "customers" ("id" integer NOT NULL)`,
		// A CHECK or UNIQUE constraint is satisfied row by row, so deferring it
		// would delay an error without preventing one.
		`ALTER TABLE "orders" ADD CONSTRAINT "chk_qty" CHECK ("qty" > 0)`,
		`ALTER TABLE "orders" ADD CONSTRAINT "uq_no" UNIQUE ("order_no")`,
		`ALTER TABLE "orders" ADD CONSTRAINT "orders_pkey" PRIMARY KEY ("id")`,
		`CREATE INDEX "ix_orders_cust" ON "orders" ("cust_id")`,
		`CREATE VIEW "v" AS SELECT 1`,
		`CREATE FUNCTION f() RETURNS integer AS $$ SELECT 1 $$ LANGUAGE sql`,
		`COMMENT ON TABLE "orders" IS 'has a FOREIGN KEY in the text'`,
		`INSERT INTO t VALUES ('CREATE TRIGGER in a string')`,
	}
	for _, stmt := range immediate {
		if pgIsPostDataStmt(stmt) {
			t.Errorf("should NOT be deferred but was:\n%s", stmt)
		}
	}
}
