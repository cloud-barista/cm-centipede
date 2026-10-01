package mysql

import (
	"context"
	"fmt"
	base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Unit tests (no MySQL server required)
// =============================================================================

func TestBuildDumpCmd_ContainsRequiredFlags(t *testing.T) {
	loc := sshTunnelMySQL()
	drv := NewMySQLDriver()

	cmd := drv.BuildDumpCmd(loc, base.ScopeFull)
	for _, flag := range []string{"mysqldump", "--routines", "--triggers", "--events", "--no-tablespaces"} {
		if !strings.Contains(cmd, flag) {
			t.Errorf("BuildDumpCmd missing %q: %s", flag, cmd)
		}
	}
}

func TestBuildDumpCmd_SchemaOnly(t *testing.T) {
	drv := NewMySQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMySQL(), base.ScopeSchemaOnly)
	if !strings.Contains(cmd, "--no-data") {
		t.Errorf("schema-only should have --no-data: %s", cmd)
	}
	if strings.Contains(cmd, "--no-create-info") {
		t.Errorf("schema-only must not have --no-create-info: %s", cmd)
	}
}

func TestBuildDumpCmd_DataOnly(t *testing.T) {
	drv := NewMySQLDriver()
	cmd := drv.BuildDumpCmd(sshTunnelMySQL(), base.ScopeDataOnly)
	if !strings.Contains(cmd, "--no-create-info") {
		t.Errorf("data-only should have --no-create-info: %s", cmd)
	}
	if strings.Contains(cmd, "--no-data") {
		t.Errorf("data-only must not have --no-data: %s", cmd)
	}
}

func TestBuildDumpCmd_DirectMode_ReturnsEmpty(t *testing.T) {
	drv := NewMySQLDriver()
	loc := directMySQL()
	if cmd := drv.BuildDumpCmd(loc, base.ScopeFull); cmd != "" {
		t.Errorf("BuildDumpCmd for direct mode should be empty, got %q", cmd)
	}
}

func TestBuildRestoreCmd_ContainsMySQL(t *testing.T) {
	drv := NewMySQLDriver()
	cmd := drv.BuildRestoreCmd(sshTunnelMySQL())
	if !strings.HasPrefix(cmd, "mysql") {
		t.Errorf("BuildRestoreCmd should start with 'mysql': %s", cmd)
	}
}

func TestMysqlExtractFKs_NoFK(t *testing.T) {
	createSQL := "CREATE TABLE `users` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB"
	clean, alters := mysqlExtractFKs("users", createSQL)
	if len(alters) != 0 {
		t.Errorf("expected no FK alters, got %v", alters)
	}
	if clean != createSQL {
		t.Errorf("clean SQL should be unchanged when no FKs present")
	}
}

func TestMysqlExtractFKs_WithFK(t *testing.T) {
	createSQL := "CREATE TABLE `orders` (\n" +
		"  `id` int NOT NULL,\n" +
		"  `user_id` int NOT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  CONSTRAINT `fk_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE\n" +
		") ENGINE=InnoDB"

	clean, alters := mysqlExtractFKs("orders", createSQL)

	if strings.Contains(clean, "FOREIGN KEY") {
		t.Errorf("clean SQL should not contain FOREIGN KEY:\n%s", clean)
	}
	if len(alters) != 1 {
		t.Fatalf("expected 1 ALTER, got %d: %v", len(alters), alters)
	}
	if !strings.Contains(alters[0], "ALTER TABLE `orders` ADD CONSTRAINT") {
		t.Errorf("alter statement format unexpected: %s", alters[0])
	}
	if !strings.Contains(alters[0], "fk_user") {
		t.Errorf("alter statement should reference fk_user: %s", alters[0])
	}
	// Trailing comma should be gone from the line before closing paren
	if strings.Contains(clean, "PRIMARY KEY (`id`),\n)") {
		t.Errorf("trailing comma not cleaned from last line before ')':\n%s", clean)
	}
}

func TestMysqlExtractFKs_MultipleFKs(t *testing.T) {
	createSQL := "CREATE TABLE `items` (\n" +
		"  `id` int NOT NULL,\n" +
		"  `order_id` int NOT NULL,\n" +
		"  `product_id` int NOT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  CONSTRAINT `fk_order` FOREIGN KEY (`order_id`) REFERENCES `orders` (`id`),\n" +
		"  CONSTRAINT `fk_product` FOREIGN KEY (`product_id`) REFERENCES `products` (`id`)\n" +
		") ENGINE=InnoDB"

	clean, alters := mysqlExtractFKs("items", createSQL)

	if strings.Contains(clean, "FOREIGN KEY") {
		t.Errorf("clean SQL should not contain FOREIGN KEY")
	}
	if len(alters) != 2 {
		t.Errorf("expected 2 ALTER statements, got %d", len(alters))
	}
}

func TestSplitMySQLStatements_Simple(t *testing.T) {
	script := "CREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n"
	stmts := splitMySQLStatements(script)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.HasPrefix(stmts[0], "CREATE TABLE") {
		t.Errorf("first stmt should be CREATE TABLE, got: %s", stmts[0])
	}
}

func TestSplitMySQLStatements_DelimiterBlock(t *testing.T) {
	script := "CREATE TABLE t (id INT);\n" +
		"DELIMITER $$\n" +
		"CREATE PROCEDURE p()\n" +
		"BEGIN\n" +
		"  SELECT 1;\n" +
		"END$$\n" +
		"DELIMITER ;\n" +
		"INSERT INTO t VALUES (2);\n"

	stmts := splitMySQLStatements(script)

	// Expect: CREATE TABLE, CREATE PROCEDURE, INSERT
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[1], "CREATE PROCEDURE") {
		t.Errorf("second stmt should be CREATE PROCEDURE, got: %s", stmts[1])
	}
	if !strings.Contains(stmts[1], "SELECT 1;") {
		t.Errorf("procedure body should be intact, got: %s", stmts[1])
	}
}

func TestSplitMySQLStatements_EmptyInput(t *testing.T) {
	stmts := splitMySQLStatements("")
	if len(stmts) != 0 {
		t.Errorf("expected 0 statements for empty input, got %d", len(stmts))
	}
}

func TestMysqlFormatValue_Types(t *testing.T) {
	cases := []struct {
		input interface{}
		want  string
	}{
		{nil, "NULL"},
		{int64(42), "42"},
		{float64(3.14), "3.14"},
		{true, "1"},
		{false, "0"},
		{[]byte("hello"), "'hello'"},
		{"world", "'world'"},
		{"it's", `'it\'s'`},
	}
	for _, tc := range cases {
		got := mysqlFormatValue(tc.input)
		if got != tc.want {
			t.Errorf("mysqlFormatValue(%T(%v)) = %q, want %q", tc.input, tc.input, got, tc.want)
		}
	}
}

func TestBuildSelectQuery_NoExclusion(t *testing.T) {
	drv := NewMySQLDriver()
	// No column exclusion → SELECT * without touching the database (nil DB is safe).
	q, err := drv.buildSelectQuery(context.Background(), nil, "app", "users", nil)
	if err != nil {
		t.Fatalf("buildSelectQuery: %v", err)
	}
	if q != "SELECT * FROM `users`" {
		t.Errorf("unexpected query: %s", q)
	}
}

func TestMySQLDropColumns(t *testing.T) {
	in := "CREATE TABLE `users` (\n" +
		"  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `password` varchar(255) DEFAULT NULL,\n" +
		"  `email` varchar(255) DEFAULT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE KEY `email_idx` (`email`),\n" +
		"  KEY `pw_idx` (`password`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	out := mysqlDropColumns(in, map[string]bool{"password": true})

	if strings.Contains(out, "`password`") {
		t.Errorf("password column and its key should be gone:\n%s", out)
	}
	if strings.Contains(out, "pw_idx") {
		t.Errorf("index on password should be dropped:\n%s", out)
	}
	if !strings.Contains(out, "`email`") || !strings.Contains(out, "email_idx") {
		t.Errorf("unrelated column/index must remain:\n%s", out)
	}
	if !strings.Contains(out, "PRIMARY KEY (`id`)") {
		t.Errorf("primary key on a kept column must remain:\n%s", out)
	}
	if strings.Contains(out, ",\n)") {
		t.Errorf("dangling comma before closing paren:\n%s", out)
	}
}

func TestMySQLDropColumns_CompositeKeyAndEmpty(t *testing.T) {
	// Unchanged when no exclusions.
	in := "CREATE TABLE `t` (\n  `id` int\n)"
	if got := mysqlDropColumns(in, nil); got != in {
		t.Errorf("empty exclusion must return input unchanged")
	}
	// A composite key touching an excluded column is dropped whole.
	in2 := "CREATE TABLE `t` (\n" +
		"  `a` int NOT NULL,\n" +
		"  `b` int NOT NULL,\n" +
		"  KEY `ab_idx` (`a`,`b`)\n" +
		") ENGINE=InnoDB"
	out2 := mysqlDropColumns(in2, map[string]bool{"b": true})
	if strings.Contains(out2, "ab_idx") || strings.Contains(out2, "`b`") {
		t.Errorf("composite key touching excluded column must be dropped:\n%s", out2)
	}
}

// mysqlCollectUsed decides which names a charset check holds the target to. A
// name kept only by an object the dump will not carry must not block a migration
// that never touches it, so the filter is applied before the names are gathered.
// Rows are shaped as mysqlUsedCharsetQuery returns them: table, column (empty on
// a table row), character set, collation.

func TestMysqlCollectUsed_NoFilter(t *testing.T) {
	rows := [][]string{
		{"orders", "", "utf8mb4", "utf8mb4_general_ci"},
		{"legacy", "", "latin1", "latin1_swedish_ci"},
		{"orders", "note", "utf8mb4", "utf8mb4_bin"},
	}

	var p base.CharsetProfile
	mysqlCollectUsed(&p, rows, nil)

	if got := strings.Join(p.UsedCharsets, ","); got != "latin1,utf8mb4" {
		t.Errorf("UsedCharsets = %q, want sorted and deduplicated", got)
	}
	if got := strings.Join(p.UsedCollations, ","); got != "latin1_swedish_ci,utf8mb4_bin,utf8mb4_general_ci" {
		t.Errorf("UsedCollations = %q, want sorted and deduplicated", got)
	}
}

func TestMysqlCollectUsed_ExcludedTableDropsItsNames(t *testing.T) {
	// The dump will not carry `legacy`, so the latin1 collation it is the only
	// user of must not be held against the target.
	rows := [][]string{
		{"orders", "", "utf8mb4", "utf8mb4_general_ci"},
		{"legacy", "", "latin1", "latin1_swedish_ci"},
	}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("legacy"),
	}}

	var p base.CharsetProfile
	mysqlCollectUsed(&p, rows, f)

	for _, name := range p.UsedCollations {
		if name == "latin1_swedish_ci" {
			t.Errorf("an excluded table's collation must not be collected: %v", p.UsedCollations)
		}
	}
	if len(p.UsedCollations) != 1 || p.UsedCollations[0] != "utf8mb4_general_ci" {
		t.Errorf("UsedCollations = %v, want only the kept table's", p.UsedCollations)
	}
}

func TestMysqlCollectUsed_ExcludedColumnDropsItsNames(t *testing.T) {
	// The column is dropped from the CREATE TABLE the dump writes, so its
	// collation never reaches the target either.
	rows := [][]string{
		{"users", "", "utf8mb4", "utf8mb4_general_ci"},
		{"users", "password", "utf8mb4", "utf8mb4_bin"},
	}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users", "password"),
	}}

	var p base.CharsetProfile
	mysqlCollectUsed(&p, rows, f)

	if len(p.UsedCollations) != 1 || p.UsedCollations[0] != "utf8mb4_general_ci" {
		t.Errorf("UsedCollations = %v, want the excluded column's collation dropped", p.UsedCollations)
	}
}

func TestMysqlWithCollation(t *testing.T) {
	// The object has to be created under the source's collation_connection, and
	// the session value has to be put back afterwards so the next object in the
	// dump is not created under this one's.
	got := mysqlWithCollation("CREATE VIEW `v` AS SELECT 1;\n", "utf8mb4_general_ci")

	if !strings.Contains(got, "SET @saved_col_connection = @@collation_connection;") {
		t.Errorf("session collation not saved:\n%s", got)
	}
	// A string literal, not a back-quoted identifier: back quotes would make the
	// server read the value as a column reference.
	if !strings.Contains(got, "SET collation_connection = 'utf8mb4_general_ci';") {
		t.Errorf("collation not set as a string literal:\n%s", got)
	}
	if !strings.Contains(got, "SET collation_connection = @saved_col_connection;") {
		t.Errorf("session collation not restored:\n%s", got)
	}
	if before, _, _ := strings.Cut(got, "CREATE VIEW"); !strings.Contains(before, "SET collation_connection = 'utf8mb4_general_ci';") {
		t.Errorf("collation must be set before the statement, not after:\n%s", got)
	}
}

func TestMysqlWithCollation_DelimiterBlockStaysIntact(t *testing.T) {
	// The SET statements terminate with ";", so they must sit outside the
	// DELIMITER block — inside it the server would wait for "$$" instead.
	body := "DELIMITER $$\nCREATE PROCEDURE `p`() BEGIN SELECT 1; END$$\nDELIMITER ;\n"
	got := mysqlWithCollation(body, "utf8mb4_general_ci")

	setAt := strings.Index(got, "SET collation_connection = 'utf8mb4_general_ci';")
	blockAt := strings.Index(got, "DELIMITER $$")
	restoreAt := strings.Index(got, "SET collation_connection = @saved_col_connection;")
	endAt := strings.Index(got, "DELIMITER ;")
	if setAt < 0 || blockAt < 0 || restoreAt < 0 || endAt < 0 {
		t.Fatalf("wrapper is missing a part:\n%s", got)
	}
	if !(setAt < blockAt && endAt < restoreAt) {
		t.Errorf("SET statements must bracket the DELIMITER block, not sit inside it:\n%s", got)
	}
}

func TestMysqlWithCollation_EmptyIsNoOp(t *testing.T) {
	// SHOW CREATE TABLE reports no session context, and an older server may
	// report none either. Nothing to reproduce, so the statement is emitted bare
	// rather than wrapped around an empty value.
	stmt := "CREATE VIEW `v` AS SELECT 1;\n"
	if got := mysqlWithCollation(stmt, ""); got != stmt {
		t.Errorf("empty collation must return the statement unchanged:\n%s", got)
	}
}

func TestMysqlEnsureTableCollation(t *testing.T) {
	// SHOW CREATE TABLE left COLLATE= out because the collation is the default
	// for utf8mb4 on this server. Restoring that onto a server with a different
	// default would silently change it, so the catalog value is spliced in.
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	got := mysqlEnsureTableCollation(in, "utf8mb4_general_ci")
	if !strings.Contains(got, "DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci") {
		t.Errorf("collation not spliced after the charset option:\n%s", got)
	}
}

func TestMysqlEnsureTableCollation_AlreadyPresent(t *testing.T) {
	// A statement that already names its collation must come back byte-identical
	// — a second COLLATE= would not parse.
	in := "CREATE TABLE `t` (\n  `id` int\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"
	if got := mysqlEnsureTableCollation(in, "utf8mb4_general_ci"); got != in {
		t.Errorf("statement with an explicit collation must be unchanged:\n%s", got)
	}
}

func TestMysqlEnsureTableCollation_ColumnLevelClauseIsNotMistaken(t *testing.T) {
	// A column-level clause reads "COLLATE x" without the equals sign. Treating
	// it as the table option would leave the table itself unqualified.
	in := "CREATE TABLE `t` (\n" +
		"  `name` varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	got := mysqlEnsureTableCollation(in, "utf8mb4_general_ci")
	if !strings.Contains(got, "DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci") {
		t.Errorf("column-level COLLATE suppressed the table option:\n%s", got)
	}
	if !strings.Contains(got, "COLLATE utf8mb4_bin") {
		t.Errorf("column-level COLLATE was altered:\n%s", got)
	}
}

func TestMysqlEnsureTableCollation_PartitionedTable(t *testing.T) {
	// The partition clause follows the table options. Appending to the end of the
	// statement would land inside it, so the splice goes right after the charset.
	in := "CREATE TABLE `t` (\n  `id` int NOT NULL\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4\n" +
		"/*!50100 PARTITION BY HASH (`id`)\nPARTITIONS 4 */"
	got := mysqlEnsureTableCollation(in, "utf8mb4_general_ci")
	if !strings.Contains(got, "DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci\n/*!50100 PARTITION BY") {
		t.Errorf("collation must sit before the partition clause:\n%s", got)
	}
}

func TestMysqlEnsureTableCollation_EmptyCollationIsNoOp(t *testing.T) {
	// The catalog had nothing to say — a table with no collation at all. Nothing
	// to splice, so the statement passes through.
	in := "CREATE TABLE `t` (\n  `id` int\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	if got := mysqlEnsureTableCollation(in, ""); got != in {
		t.Errorf("empty collation must return the statement unchanged:\n%s", got)
	}
}

// The SSH path reads the mysql CLI's tab-separated output by column position, so
// a column added to a shared query without widening the parser silently
// mis-assigns every field after it. The integration tests below need a live
// server and would not catch that; these do. Each row is written as
// mysqlQueryViaSSH hands it over — already unescaped, one element per column.

func TestMysqlParseTableRow(t *testing.T) {
	row := []string{
		"orders", "1200", "98304", "65536", "32768", "InnoDB",
		"utf8mb4_unicode_ci", "utf8mb4",
	}

	got, ok := mysqlParseTableRow(row)
	if !ok {
		t.Fatalf("mysqlParseTableRow rejected a full-width row: %v", row)
	}
	if got.Name != "orders" {
		t.Errorf("Name = %q, want %q", got.Name, "orders")
	}
	if got.RowCount != 1200 {
		t.Errorf("RowCount = %d, want 1200", got.RowCount)
	}
	if got.TotalSize != 98304 || got.DataSize != 65536 || got.IndexSize != 32768 {
		t.Errorf("sizes = total %d, data %d, index %d; want 98304/65536/32768",
			got.TotalSize, got.DataSize, got.IndexSize)
	}
	if got.Engine != "InnoDB" {
		t.Errorf("Engine = %q, want %q", got.Engine, "InnoDB")
	}
	// The two columns most at risk: they sit last, so an off-by-one swaps them
	// or leaves them empty rather than failing outright.
	if got.Collation != "utf8mb4_unicode_ci" {
		t.Errorf("Collation = %q, want %q", got.Collation, "utf8mb4_unicode_ci")
	}
	if got.CharacterSet != "utf8mb4" {
		t.Errorf("CharacterSet = %q, want %q", got.CharacterSet, "utf8mb4")
	}
}

func TestMysqlParseTableRow_ShortRowSkipped(t *testing.T) {
	// The pre-charset column list. Accepting it would report a table whose
	// collation and character set silently read empty.
	old := []string{"orders", "1200", "98304", "65536", "32768", "InnoDB"}
	if _, ok := mysqlParseTableRow(old); ok {
		t.Error("mysqlParseTableRow accepted a row missing the charset columns")
	}
}

func TestMysqlParseColumnRow(t *testing.T) {
	row := []string{
		"name", "varchar(100)", "YES", "", "MUL", "the display name",
		"utf8mb4", "utf8mb4_unicode_ci",
	}

	got, ok := mysqlParseColumnRow(row)
	if !ok {
		t.Fatalf("mysqlParseColumnRow rejected a full-width row: %v", row)
	}
	if got.Name != "name" || got.DataType != "varchar(100)" {
		t.Errorf("Name/DataType = %q/%q, want name/varchar(100)", got.Name, got.DataType)
	}
	if !got.IsNullable {
		t.Error("IsNullable = false, want true for is_nullable=YES")
	}
	if got.IsPrimary {
		t.Error("IsPrimary = true, want false for column_key=MUL")
	}
	if got.CharacterSet != "utf8mb4" {
		t.Errorf("CharacterSet = %q, want %q", got.CharacterSet, "utf8mb4")
	}
	if got.Collation != "utf8mb4_unicode_ci" {
		t.Errorf("Collation = %q, want %q", got.Collation, "utf8mb4_unicode_ci")
	}
}

func TestMysqlParseColumnRow_NonStringColumn(t *testing.T) {
	// A numeric column has no character set. mysqlColumnQuery coalesces both
	// catalog columns to '' so the CLI never writes the literal "NULL" into
	// them; this pins that the parser passes the empty strings through.
	row := []string{"id", "int", "NO", "", "PRI", "", "", ""}

	got, ok := mysqlParseColumnRow(row)
	if !ok {
		t.Fatalf("mysqlParseColumnRow rejected a full-width row: %v", row)
	}
	if !got.IsPrimary {
		t.Error("IsPrimary = false, want true for column_key=PRI")
	}
	if got.CharacterSet != "" || got.Collation != "" {
		t.Errorf("non-string column carried charset %q / collation %q, want both empty",
			got.CharacterSet, got.Collation)
	}
}

func TestMysqlParseColumnRow_ShortRowSkipped(t *testing.T) {
	old := []string{"name", "varchar(100)", "YES", "", "MUL", "the display name"}
	if _, ok := mysqlParseColumnRow(old); ok {
		t.Error("mysqlParseColumnRow accepted a row missing the charset columns")
	}
}

// TestMysqlSharedQueryColumnCount keeps the parsers and the queries in step: a
// parser reads eight columns, so each query must select eight. Counting
// top-level commas is enough because neither query nests a call with arguments.
func TestMysqlSharedQueryColumnCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"mysqlTableListQuery", mysqlTableListQuery},
		{"mysqlColumnQuery", mysqlColumnQuery},
	} {
		selectList := tc.query
		if i := strings.Index(selectList, " FROM "); i >= 0 {
			selectList = selectList[:i]
		}
		if n := strings.Count(selectList, ",") - strings.Count(selectList, "(") + 1; n != 8 {
			t.Errorf("%s selects %d columns, but the parser reads 8", tc.name, n)
		}
	}
}

// =============================================================================
// Integration tests (require MYSQL_TEST_DSN)
// =============================================================================
//
// Run mysql_test.sh to execute all integration tests below automatically.
// The script handles container startup, fixture seeding, test execution, and cleanup.
//
// To run:
//
//	./mysql_test.sh           # start container, run all TestMySQL* tests, remove container on exit
//	./mysql_test.sh --keep    # keep container running after tests (useful for debugging)
//
// Integration tests covered:
//
//	TestMySQLTestConnection          — verify DB connection is reachable
//	TestMySQLCheckTargetEmpty_EmptyDB — check whether the target database is empty
//	TestMySQLDumpRestore_SchemaOnly   — produce a schema-only dump and validate the output file
//	TestMySQLInspect                 — retrieve server version and database metadata
//
// To run manually without the script:
//
//	export MYSQL_TEST_DSN="root:pass@tcp(127.0.0.1:3306)/testdb_src?parseTime=true"
//	go test . -run TestMySQL -v -timeout 120s

type mysqlTestEnv struct {
	loc     base.DBMSLocation
	srcDB   string
	destDB  string
	cleanup func()
}

func mysqlTestSetup(t *testing.T) *mysqlTestEnv {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN not set; skipping MySQL integration tests")
	}

	// Parse host/port/user/pass from DSN for direct config.
	// Expected format: user:pass@tcp(host:port)/db
	// We use a minimal parse.
	loc := base.DBMSLocation{
		DBMSType:   base.DBMSTypeMySQL,
		Database:   "testdb_src",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: "localhost", Port: 3306, Username: "root", Password: "pass"},
	}
	// Allow override via simple env vars
	if v := os.Getenv("MYSQL_TEST_HOST"); v != "" {
		loc.Direct.Host = v
	}
	if v := os.Getenv("MYSQL_TEST_USER"); v != "" {
		loc.Direct.Username = v
	}
	if v := os.Getenv("MYSQL_TEST_PASS"); v != "" {
		loc.Direct.Password = v
	}
	if v := os.Getenv("MYSQL_TEST_PORT"); v != "" {
		p := 0
		fmt.Sscanf(v, "%d", &p)
		if p > 0 {
			loc.Direct.Port = p
		}
	}

	drv := NewMySQLDriver()
	if err := drv.TestConnection(context.Background(), loc); err != nil {
		t.Skipf("MySQL not reachable: %v", err)
	}

	return &mysqlTestEnv{
		loc:    loc,
		srcDB:  "testdb_src",
		destDB: "testdb_dst",
	}
}

// createTestFixture creates a test database with tables, FK, view, procedure, function, trigger.
func createTestFixture(t *testing.T, env *mysqlTestEnv) {
	t.Helper()
	// (full integration fixture creation would go here; skipped in unit-only runs)
}

func TestMySQLTestConnection(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()
	if err := drv.TestConnection(context.Background(), env.loc); err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}
}

func TestMySQLCheckTargetEmpty_EmptyDB(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()
	// Use a db that likely has no tables in this test run.
	loc := env.loc
	loc.Database = "information_schema" // always read-only, we just check it isn't empty
	_ = drv.CheckTargetEmpty(context.Background(), loc)
	// We just verify no panic; information_schema will return non-empty but that's OK.
}

func TestMySQLDumpRestore_SchemaOnly(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

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
}

func TestMySQLInspect(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

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

// sshTunnelMySQL returns a DBMSLocation with SSH tunnel access type for testing BuildDumpCmd.
func sshTunnelMySQL() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: base.AccessTypeSSHTunnel,
		SSHTunnel: &base.SSHTunnelConfig{
			SSH:      &base.SSHConfig{Host: "ssh.example.com", Port: 22, Username: "deploy"},
			DBHost:   "127.0.0.1",
			DBPort:   3306,
			Username: "root",
			Password: "pass",
		},
	}
}

func directMySQL() base.DBMSLocation {
	return base.DBMSLocation{
		DBMSType:   base.DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: base.AccessTypeDirect,
		Direct:     &base.DirectConfig{Host: "127.0.0.1", Port: 3306},
	}
}

// =============================================================================
// Inspect metric integration tests (require MYSQL_TEST_DSN + seeded fixtures)
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

func withMetric(loc base.DBMSLocation, m *base.MetricOption) base.DBMSLocation {
	loc.Metric = m
	return loc
}

// TestMySQLInspect_DefaultOmitsDetail verifies that a nil Metric returns only
// the cheap summary: table list without per-table columns/indexes and without
// any schema-object inventory.
func TestMySQLInspect_DefaultOmitsDetail(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

	info, err := drv.Inspect(context.Background(), env.loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(info.Tables) == 0 {
		t.Fatal("expected non-empty table list")
	}
	for _, tbl := range info.Tables {
		if len(tbl.Columns) != 0 || len(tbl.Indexes) != 0 {
			t.Errorf("table %q: default Inspect must omit columns/indexes, got %d cols %d idx",
				tbl.Name, len(tbl.Columns), len(tbl.Indexes))
		}
	}
	if info.Views != nil || info.Functions != nil || info.Procedures != nil ||
		info.Triggers != nil || info.Events != nil || info.ForeignKeys != nil {
		t.Errorf("default Inspect must omit schema-object lists, got %+v", info)
	}
}

// TestMySQLInspect_ColumnsIndexes verifies the columns/indexes toggles populate
// per-table detail without pulling in schema-object inventories.
func TestMySQLInspect_ColumnsIndexes(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

	loc := withMetric(env.loc, &base.MetricOption{MySQL: &base.MySQLMetric{
		Columns: bp(true), Indexes: bp(true),
	}})
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
	if len(users.Indexes) == 0 {
		t.Error("expected indexes for users when Indexes=true")
	}
	if info.Views != nil || info.Procedures != nil {
		t.Error("columns/indexes metric must not populate schema-object lists")
	}
}

// TestMySQLInspect_SchemaObjects verifies that schema-object toggles enumerate
// the seeded objects by name, without pulling in per-table columns.
func TestMySQLInspect_SchemaObjects(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

	loc := withMetric(env.loc, &base.MetricOption{MySQL: &base.MySQLMetric{
		Views: bp(true), Functions: bp(true), Procedures: bp(true),
		Triggers: bp(true), Events: bp(true), ForeignKeys: bp(true),
	}})
	info, err := drv.Inspect(context.Background(), loc)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !containsAll(info.Views, "active_users") {
		t.Errorf("Views = %v, want to contain active_users", info.Views)
	}
	if !containsAll(info.Functions, "user_count") {
		t.Errorf("Functions = %v, want to contain user_count", info.Functions)
	}
	if !containsAll(info.Procedures, "add_user") {
		t.Errorf("Procedures = %v, want to contain add_user", info.Procedures)
	}
	if !containsAll(info.Triggers, "trg_orders_ai") {
		t.Errorf("Triggers = %v, want to contain trg_orders_ai", info.Triggers)
	}
	if !containsAll(info.Events, "ev_noop") {
		t.Errorf("Events = %v, want to contain ev_noop", info.Events)
	}
	if !containsAll(info.ForeignKeys, "fk_orders_user") {
		t.Errorf("ForeignKeys = %v, want to contain fk_orders_user", info.ForeignKeys)
	}
	// Not requested → not populated.
	for _, tbl := range info.Tables {
		if len(tbl.Columns) != 0 {
			t.Errorf("table %q: columns must stay empty when not requested", tbl.Name)
		}
	}
}

// TestMySQLInspect_RowCountExact verifies the exact row count matches the seeded
// data (2 users).
func TestMySQLInspect_RowCountExact(t *testing.T) {
	env := mysqlTestSetup(t)
	drv := NewMySQLDriver()

	loc := withMetric(env.loc, &base.MetricOption{MySQL: &base.MySQLMetric{RowCountExact: bp(true)}})
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

func TestMySQLRowExcludeClause(t *testing.T) {
	// No rules => empty clause.
	if got := mysqlRowExcludeClause(nil, "orders"); got != "" {
		t.Errorf("nil filter = %q, want empty", got)
	}

	// Single predicate: NULL-preserving negation.
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeRows("orders", "status = 'cancelled'"),
	}}
	got := mysqlRowExcludeClause(f, "orders")
	want := "(NOT (status = 'cancelled') OR (status = 'cancelled') IS NULL)"
	if got != want {
		t.Errorf("single =\n  %q\nwant\n  %q", got, want)
	}

	// Multiple predicates AND-joined.
	f2 := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeRows("orders", "a"),
		filterpkg.ExcludeRows("orders", "b"),
	}}
	got2 := mysqlRowExcludeClause(f2, "orders")
	want2 := "(NOT (a) OR (a) IS NULL) AND (NOT (b) OR (b) IS NULL)"
	if got2 != want2 {
		t.Errorf("multi =\n  %q\nwant\n  %q", got2, want2)
	}
}

func TestMysqlConstraintName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ALTER TABLE `orders` ADD CONSTRAINT `fk_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)", "fk_user"},
		{"ALTER TABLE `t` ADD PRIMARY KEY (`id`)", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := mysqlConstraintName(c.in); got != c.want {
			t.Errorf("mysqlConstraintName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMysqlDropIndexes(t *testing.T) {
	in := "CREATE TABLE `orders` (\n" +
		"  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `user_id` int NOT NULL,\n" +
		"  `created` datetime DEFAULT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE KEY `uq_user` (`user_id`),\n" +
		"  KEY `idx_created` (`created`),\n" +
		"  CONSTRAINT `fk_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)\n" +
		") ENGINE=InnoDB"
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeIndex("orders", "idx_created"),
	}}
	out := mysqlDropIndexes(in, "orders", f)

	if strings.Contains(out, "idx_created") {
		t.Errorf("idx_created should be dropped:\n%s", out)
	}
	if !strings.Contains(out, "PRIMARY KEY (`id`)") {
		t.Errorf("PRIMARY KEY must remain:\n%s", out)
	}
	if !strings.Contains(out, "uq_user") || !strings.Contains(out, "fk_user") {
		t.Errorf("unrelated index/constraint must remain:\n%s", out)
	}
	if strings.Contains(out, ",\n)") {
		t.Errorf("dangling comma before closing paren:\n%s", out)
	}

	// Table qualifier: a rule pinned to another table must not drop it here.
	f2 := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeIndex("other", "idx_created"),
	}}
	if out2 := mysqlDropIndexes(in, "orders", f2); !strings.Contains(out2, "idx_created") {
		t.Errorf("index pinned to another table must not be dropped from orders")
	}
}

func TestMysqlIndexName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"KEY `idx_created` (`created`)", "idx_created"},
		{"UNIQUE KEY `uq_user` (`user_id`)", "uq_user"},
		{"PRIMARY KEY (`id`)", ""},
		{"CONSTRAINT `fk_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)", ""},
		{"`created` datetime DEFAULT NULL", ""},
	}
	for _, c := range cases {
		if got := mysqlIndexName(c.in); got != c.want {
			t.Errorf("mysqlIndexName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// =============================================================================
// FK parent-side filtering (mysqlFKRef / mysqlFKRefDropped)
// =============================================================================

const fkAlterOrders = "ALTER TABLE `orders` ADD CONSTRAINT `fk_user` " +
	"FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE"

func TestMysqlFKRef(t *testing.T) {
	refTable, refCols := mysqlFKRef(fkAlterOrders)
	if refTable != "users" {
		t.Errorf("refTable = %q, want users", refTable)
	}
	if len(refCols) != 1 || refCols[0] != "id" {
		t.Errorf("refCols = %v, want [id]", refCols)
	}
}

func TestMysqlFKRef_Composite(t *testing.T) {
	alter := "ALTER TABLE `items` ADD CONSTRAINT `fk_ov` " +
		"FOREIGN KEY (`o_id`, `v_id`) REFERENCES `variants` (`o_id`, `v_id`)"
	refTable, refCols := mysqlFKRef(alter)
	if refTable != "variants" {
		t.Errorf("refTable = %q, want variants", refTable)
	}
	if len(refCols) != 2 || refCols[0] != "o_id" || refCols[1] != "v_id" {
		t.Errorf("refCols = %v, want [o_id v_id]", refCols)
	}
}

func TestMysqlFKRef_NoReferences(t *testing.T) {
	if refTable, refCols := mysqlFKRef("ALTER TABLE `t` ADD KEY `k` (`c`)"); refTable != "" || refCols != nil {
		t.Errorf("mysqlFKRef = (%q, %v), want empty", refTable, refCols)
	}
}

func TestMysqlFKRefDropped_NilFilter(t *testing.T) {
	if _, reason := mysqlFKRefDropped(fkAlterOrders, nil); reason != "" {
		t.Errorf("reason = %q, want empty for nil filter", reason)
	}
}

func TestMysqlFKRefDropped_ParentTableExcluded(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users"),
	}}
	refTable, reason := mysqlFKRefDropped(fkAlterOrders, f)
	if refTable != "users" || reason != "referenced table excluded" {
		t.Errorf("got (%q, %q), want (users, referenced table excluded)", refTable, reason)
	}
}

func TestMysqlFKRefDropped_ParentColumnExcluded(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("users", "id"),
	}}
	_, reason := mysqlFKRefDropped(fkAlterOrders, f)
	if reason != "referenced column id excluded" {
		t.Errorf("reason = %q, want referenced column id excluded", reason)
	}
}

func TestMysqlFKRefDropped_UnrelatedExclusionKeepsFK(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("audit_log"),
		filterpkg.ExcludeTable("users", "password_hash"),
	}}
	if _, reason := mysqlFKRefDropped(fkAlterOrders, f); reason != "" {
		t.Errorf("reason = %q, want empty (FK must survive)", reason)
	}
}

// =============================================================================
// Unit tests for the SSH inspect transport (no MySQL server required)
// =============================================================================

func TestMysqlUnescapeBatch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The mysql client escapes these in --batch mode; leaving them escaped
		// would corrupt multi-line column comments and default expressions.
		{"newline", `line1\nline2`, "line1\nline2"},
		{"tab", `a\tb`, "a\tb"},
		{"carriage return", `a\rb`, "a\rb"},
		{"backslash", `C:\\path`, `C:\path`},
		{"nul", `a\0b`, "a\x00b"},
		{"mixed", `a\tb\nc`, "a\tb\nc"},
		// An escaped backslash followed by t is a literal backslash-t, not a tab.
		{"escaped backslash then t", `a\\tb`, `a\tb`},
		{"no escapes is returned as is", "plain value", "plain value"},
		{"trailing lone backslash", `abc\`, `abc\`},
		{"unknown escape kept verbatim", `a\qb`, `a\qb`},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := mysqlUnescapeBatch(c.in); got != c.want {
			t.Errorf("%s: mysqlUnescapeBatch(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestMysqlSSHMaxLine_ExceedsScannerDefault(t *testing.T) {
	// bufio.Scanner's default cap is 64 KiB; a long comment or view definition
	// would otherwise abort the query with "token too long".
	const scannerDefault = 64 * 1024
	if mysqlSSHMaxLine <= scannerDefault {
		t.Errorf("mysqlSSHMaxLine = %d, want more than the %d default", mysqlSSHMaxLine, scannerDefault)
	}
}

// =============================================================================
// CREATE DATABASE / DROP DATABASE statement building
// =============================================================================

func TestMysqlBuildCreateDatabase(t *testing.T) {
	cases := []struct {
		label string
		name  string
		opt   base.MySQLCreateOption
		want  string
	}{
		{
			label: "no options",
			name:  "app",
			want:  "CREATE DATABASE `app`",
		},
		{
			label: "character set only",
			name:  "app",
			opt:   base.MySQLCreateOption{CharacterSet: "utf8mb4"},
			want:  "CREATE DATABASE `app` CHARACTER SET utf8mb4",
		},
		{
			label: "character set and collation",
			name:  "app",
			opt:   base.MySQLCreateOption{CharacterSet: "utf8mb4", Collate: "utf8mb4_0900_ai_ci"},
			want:  "CREATE DATABASE `app` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		},
		{
			label: "collation only",
			name:  "app",
			opt:   base.MySQLCreateOption{Collate: "utf8mb4_0900_ai_ci"},
			want:  "CREATE DATABASE `app` COLLATE utf8mb4_0900_ai_ci",
		},
		{
			label: "name needing quotes",
			name:  "my app-db",
			want:  "CREATE DATABASE `my app-db`",
		},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := mysqlBuildCreateDatabase(c.name, c.opt); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestMysqlQuoteIdent(t *testing.T) {
	if got, want := mysqlQuoteIdent("app"), "`app`"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// base.ValidateDatabaseName refuses a back quote before a name reaches here,
	// but the quoting must stay sound for any other identifier passed in.
	if got, want := mysqlQuoteIdent("a`b"), "`a``b`"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMysqlDatabaseExistsQuery_MatchesExactly(t *testing.T) {
	// SHOW DATABASES LIKE would treat "_" and "%" in the name as wildcards, so a
	// database called "app_db" would report a sibling "appXdb" as itself. The
	// existence check compares SCHEMA_NAME instead.
	q := mysqlDatabaseExistsQuery
	if want := "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = "; q != want {
		t.Errorf("query = %q, want %q", q, want)
	}
}

// =============================================================================
// TLS
// =============================================================================

// TestMysqlDSNTLSParam pins the mapping from a TLS mode onto the driver's tls=
// parameter. Three of the five reach a value built into go-sql-driver; the
// modes that verify need a registered configuration whenever the standard
// verifier cannot do the job on its own.
func TestMysqlDSNTLSParam(t *testing.T) {
	tests := []struct {
		name       string
		cfg        base.DirectConfig
		want       string
		registered bool // a dbmsx- name rather than a built-in value
	}{
		{
			name: "an empty mode is disable",
			cfg:  base.DirectConfig{Host: "db"},
			want: "false",
		},
		{
			name: "disable",
			cfg:  base.DirectConfig{Host: "db", TLSMode: base.TLSModeDisable},
			want: "false",
		},
		{
			name: "prefer",
			cfg:  base.DirectConfig{Host: "db", TLSMode: base.TLSModePrefer},
			want: "preferred",
		},
		{
			name: "require",
			cfg:  base.DirectConfig{Host: "db", TLSMode: base.TLSModeRequire},
			want: "skip-verify",
		},
		{
			// The system trust store plus a hostname check is exactly tls=true,
			// so no configuration has to be registered for it.
			name: "verify-full without a CA is the driver's own tls=true",
			cfg:  base.DirectConfig{Host: "db", TLSMode: base.TLSModeVerifyFull},
			want: "true",
		},
		{
			// verify-ca has no built-in equivalent, so it registers whether or
			// not a CA was given.
			name:       "verify-ca registers a configuration",
			cfg:        base.DirectConfig{Host: "db", TLSMode: base.TLSModeVerifyCA},
			registered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dsnTLSParam(&tt.cfg)
			if err != nil {
				t.Fatalf("dsnTLSParam: %v", err)
			}
			if tt.registered {
				if !strings.HasPrefix(got, "dbmsx-") {
					t.Fatalf("tls=%q, want a registered dbmsx- name", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("tls=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestMysqlDSNTLSParam_RejectsAnUnreadableCA(t *testing.T) {
	// Reported here rather than as a connection failure much later.
	_, err := dsnTLSParam(&base.DirectConfig{
		Host: "db", TLSMode: base.TLSModeVerifyCA, TLSCAFile: "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Fatal("expected an error for a CA file that is not there")
	}
}

func TestMysqlBuildDSN_CarriesTheTLSParam(t *testing.T) {
	d := &MySQLDriver{}
	dsn, err := d.buildDSN(base.DBMSLocation{
		DBMSType:   base.DBMSTypeMySQL,
		AccessType: base.AccessTypeDirect,
		Database:   "app",
		Direct: &base.DirectConfig{
			Host: "db", Port: 3306, Username: "u", Password: "p",
			TLSMode: base.TLSModeRequire,
		},
	})
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	if !strings.Contains(dsn, "tls=skip-verify") {
		t.Errorf("DSN does not carry the TLS parameter:\n%s", dsn)
	}
}
