package base

import (
	"errors"
	"testing"

	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
)

func TestClassifyStatement(t *testing.T) {
	cases := []struct {
		name   string
		stmt   string
		kind   string
		object string
		action string
	}{
		{
			// The statement that failed against RDS MySQL 8.4, as our dump now
			// writes it — the DEFINER already removed.
			name:   "view",
			stmt:   "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v_customer_stats` AS select 1",
			kind:   filterpkg.ObjectKindView,
			object: "v_customer_stats",
			action: ActionWriteDDL,
		},
		{
			// A dump taken before the DEFINER fix still has to classify.
			name:   "view with definer",
			stmt:   "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v_customer_stats` AS select 1",
			kind:   filterpkg.ObjectKindView,
			object: "v_customer_stats",
			action: ActionWriteDDL,
		},
		{
			// The case a 120-character prefix could not reach: the name comes
			// before the column list, the column list is long.
			name:   "create table",
			stmt:   "CREATE TABLE `order_items` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `order_id` int NOT NULL\n) ENGINE=InnoDB",
			kind:   ObjectKindTable,
			object: "order_items",
			action: ActionWriteDDL,
		},
		{
			name:   "create table no space before paren",
			stmt:   "CREATE TABLE `orders`(`id` int)",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionWriteDDL,
		},
		{
			name:   "create table if not exists",
			stmt:   "CREATE TABLE IF NOT EXISTS `orders` (`id` int)",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionWriteDDL,
		},
		{
			name:   "qualified name keeps only the object",
			stmt:   "CREATE TABLE `sales`.`orders` (`id` int)",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionWriteDDL,
		},
		{
			name:   "function",
			stmt:   "CREATE FUNCTION `fn_apply_discount`(p DECIMAL) RETURNS DECIMAL",
			kind:   filterpkg.ObjectKindFunction,
			object: "fn_apply_discount",
			action: ActionWriteDDL,
		},
		{
			name:   "procedure",
			stmt:   "CREATE PROCEDURE `sp_monthly_report`()\nBEGIN\n  SELECT 1;\nEND",
			kind:   filterpkg.ObjectKindProcedure,
			object: "sp_monthly_report",
			action: ActionWriteDDL,
		},
		{
			name:   "trigger",
			stmt:   "CREATE TRIGGER `trg_after_order_item_insert` AFTER INSERT ON `order_items` FOR EACH ROW BEGIN END",
			kind:   filterpkg.ObjectKindTrigger,
			object: "trg_after_order_item_insert",
			action: ActionWriteDDL,
		},
		{
			name:   "event",
			stmt:   "CREATE EVENT `evt_cleanup` ON SCHEDULE EVERY 1 DAY DO BEGIN END",
			kind:   filterpkg.ObjectKindEvent,
			object: "evt_cleanup",
			action: ActionWriteDDL,
		},
		{
			name:   "unique index",
			stmt:   "CREATE UNIQUE INDEX `idx_email` ON `customers` (`email`)",
			kind:   filterpkg.ObjectKindIndex,
			object: "idx_email",
			action: ActionWriteDDL,
		},
		{
			// mysqldump gates statements the target may not understand.
			name:   "version gated",
			stmt:   "/*!50003 CREATE TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW BEGIN END */",
			kind:   filterpkg.ObjectKindTrigger,
			object: "t",
			action: ActionWriteDDL,
		},
		{
			name:   "materialized view",
			stmt:   "CREATE MATERIALIZED VIEW mv_sales AS SELECT 1",
			kind:   filterpkg.ObjectKindMaterializedView,
			object: "mv_sales",
			action: ActionWriteDDL,
		},
		{
			name:   "postgresql sequence, unquoted",
			stmt:   "CREATE SEQUENCE public.orders_id_seq START WITH 1",
			kind:   filterpkg.ObjectKindSequence,
			object: "orders_id_seq",
			action: ActionWriteDDL,
		},
		{
			name:   "postgresql or replace view",
			stmt:   `CREATE OR REPLACE VIEW "sales"."v_daily" AS SELECT 1`,
			kind:   filterpkg.ObjectKindView,
			object: "v_daily",
			action: ActionWriteDDL,
		},
		{
			name:   "insert",
			stmt:   "INSERT INTO `orders` VALUES (1, 'a')",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionWriteRows,
		},
		{
			name:   "insert ignore with column list",
			stmt:   "INSERT IGNORE INTO `orders` (`id`, `total`) VALUES (1, 2)",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionWriteRows,
		},
		{
			// The foreign keys a dump holds back until every table exists; the
			// constraint name is what the server's error refers to.
			name:   "foreign key alter",
			stmt:   "ALTER TABLE `order_items` ADD CONSTRAINT `fk_order_items_order` FOREIGN KEY (`order_id`) REFERENCES `orders` (`id`)",
			kind:   filterpkg.ObjectKindForeignKey,
			object: "fk_order_items_order",
			action: ActionWriteDDL,
		},
		{
			// A non-foreign-key constraint belongs to its table, not to a kind
			// the filter vocabulary has no name for.
			name:   "check constraint alter falls back to the table",
			stmt:   "ALTER TABLE `orders` ADD CONSTRAINT `chk_total` CHECK (`total` >= 0)",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionAlter,
		},
		{
			name:   "drop table",
			stmt:   "DROP TABLE IF EXISTS `orders`",
			kind:   ObjectKindTable,
			object: "orders",
			action: ActionDrop,
		},
		{
			name:   "quoted name containing a space",
			stmt:   "CREATE TABLE `order items` (`id` int)",
			kind:   ObjectKindTable,
			object: "order items",
			action: ActionWriteDDL,
		},
		// Statements that name no object are reported by position instead.
		{name: "set", stmt: "SET FOREIGN_KEY_CHECKS=0"},
		{name: "set names", stmt: "SET NAMES utf8mb4"},
		{name: "use", stmt: "USE `sales`"},
		{name: "empty", stmt: "   \n  "},
		{
			// A keyword inside a view's body must not be mistaken for the
			// subject of a statement that named no object of its own.
			name: "select is not classified",
			stmt: "SELECT * FROM `orders` WHERE 1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyStatement(c.stmt)
			if got.Kind != c.kind || got.Name != c.object || got.Action != c.action {
				t.Errorf("ClassifyStatement()\n got: kind=%q name=%q action=%q\nwant: kind=%q name=%q action=%q",
					got.Kind, got.Name, got.Action, c.kind, c.object, c.action)
			}
			if got.Recognised() != (c.kind != "") {
				t.Errorf("Recognised() = %v for kind %q", got.Recognised(), got.Kind)
			}
		})
	}
}

// A PostgreSQL dump qualifies its names, and the qualifier is the schema that
// encloses the object — the only place the statement says which one it is.
func TestClassifyStatementKeepsQualifier(t *testing.T) {
	cases := []struct {
		name   string
		stmt   string
		kind   string
		owner  string
		object string
		action string
	}{
		{
			name: "copy from stdin", stmt: "COPY sales.orders (id, total) FROM STDIN;",
			kind: ObjectKindTable, owner: "sales", object: "orders", action: ActionWriteRows,
		},
		{
			name: "copy unqualified", stmt: "COPY orders FROM STDIN;",
			kind: ObjectKindTable, owner: "", object: "orders", action: ActionWriteRows,
		},
		{
			name: "qualified create", stmt: `CREATE TABLE "sales"."orders" (id int)`,
			kind: ObjectKindTable, owner: "sales", object: "orders", action: ActionWriteDDL,
		},
		{
			// MySQL dumps write unqualified names; the caller supplies the
			// enclosing database instead.
			name: "unqualified create", stmt: "CREATE TABLE `orders` (id int)",
			kind: ObjectKindTable, owner: "", object: "orders", action: ActionWriteDDL,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyStatement(c.stmt)
			if got.Kind != c.kind || got.Owner != c.owner || got.Name != c.object || got.Action != c.action {
				t.Errorf("ClassifyStatement()\n got: kind=%q owner=%q name=%q action=%q\nwant: kind=%q owner=%q name=%q action=%q",
					got.Kind, got.Owner, got.Name, got.Action, c.kind, c.owner, c.object, c.action)
			}
		})
	}
}

// A statement that carries its own qualifier keeps it; one that does not takes
// the owner the caller supplies.
func TestRestoreStatementError(t *testing.T) {
	cause := errors.New("Error 1227 (42000): Access denied")

	err := RestoreStatementError("mysql", "centipede_test",
		"CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v_customer_stats` AS select 1",
		47, 312, cause)
	want := "create failed on view centipede_test.v_customer_stats (statement 47/312): Error 1227 (42000): Access denied"
	if err.Error() != want {
		t.Errorf("\n got: %q\nwant: %q", err.Error(), want)
	}

	var oe *ObjectError
	if !errors.As(err, &oe) || oe.Name != "v_customer_stats" {
		t.Errorf("errors.As did not yield the failed object: %v", err)
	}

	err = RestoreStatementError("postgresql", "appdb",
		"COPY sales.orders (id) FROM STDIN;", 3, 10, cause)
	want = "insert failed on table sales.orders (statement 3/10): Error 1227 (42000): Access denied"
	if err.Error() != want {
		t.Errorf("\n got: %q\nwant: %q", err.Error(), want)
	}

	// A statement that names no object keeps the older reporting, and its text
	// is short enough to survive whole.
	err = RestoreStatementError("mysql", "centipede_test", "SET FOREIGN_KEY_CHECKS=0", 1, 312, cause)
	want = "statement 1/312 (SET FOREIGN_KEY_CHECKS=0) failed: Error 1227 (42000): Access denied"
	if err.Error() != want {
		t.Errorf("\n got: %q\nwant: %q", err.Error(), want)
	}
	if errors.As(err, &oe) {
		t.Errorf("an unclassified statement must not claim an object: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("original cause lost: %v", err)
	}
}

// The scan must not run past the object's name into a body that happens to
// contain another object keyword.
func TestClassifyStatementStopsBeforeBody(t *testing.T) {
	stmt := "CREATE VIEW `v_order_totals` AS SELECT * FROM `orders` JOIN `table_of_contents` ON 1=1"
	got := ClassifyStatement(stmt)
	if got.Name != "v_order_totals" {
		t.Errorf("got %q, want v_order_totals", got.Name)
	}
}

func TestSplitQualifiedIdent(t *testing.T) {
	cases := []struct {
		in    string
		owner string
		name  string
	}{
		{"`orders`", "", "orders"},
		{"orders", "", "orders"},
		{"`sales`.`orders`", "sales", "orders"},
		{"sales.orders", "sales", "orders"},
		{`"sales"."orders"`, "sales", "orders"},
		{"`orders`,", "", "orders"},
		{"`we``ird`", "", "we`ird"},
		// A dot inside quotes belongs to the name, not to a qualifier.
		{"`db.with.dots`", "", "db.with.dots"},
		{"`db`.`name.dotted`", "db", "name.dotted"},
	}
	for _, c := range cases {
		owner, name := SplitQualifiedIdent(c.in)
		if owner != c.owner || name != c.name {
			t.Errorf("SplitQualifiedIdent(%q) = (%q, %q), want (%q, %q)",
				c.in, owner, name, c.owner, c.name)
		}
	}
}

// =============================================================================
// StripLeadingComments
// =============================================================================

func TestStripLeadingComments_KeepsStatementBelowComments(t *testing.T) {
	// What a mysqldump event section hands the splitter: three comment lines
	// glued to the SET that saves the session time zone.
	stmt := "--\n-- Dumping events for database 'app'\n--\n/*!50106 SET @save_time_zone= @@TIME_ZONE */"
	got := StripLeadingComments(stmt)
	want := "/*!50106 SET @save_time_zone= @@TIME_ZONE */"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripLeadingComments_CommentOnly(t *testing.T) {
	for _, in := range []string{
		"-- Dump completed on 2026-09-02",
		"--",
		"--\n--\n",
		"  \n-- only a comment\n",
	} {
		if got := StripLeadingComments(in); got != "" {
			t.Errorf("StripLeadingComments(%q) = %q, want empty", in, got)
		}
	}
}

func TestStripLeadingComments_LeavesInnerCommentsAlone(t *testing.T) {
	// Only leading line comments go. A comment after the statement begins is
	// part of the statement, and /*! … */ is executable SQL rather than a comment.
	cases := map[string]string{
		"SELECT 1 -- trailing":                      "SELECT 1 -- trailing",
		"/*!40103 SET TIME_ZONE='+00:00' */":        "/*!40103 SET TIME_ZONE='+00:00' */",
		"-- lead\nINSERT INTO t VALUES (1) -- tail": "INSERT INTO t VALUES (1) -- tail",
		"": "",
	}
	for in, want := range cases {
		if got := StripLeadingComments(in); got != want {
			t.Errorf("StripLeadingComments(%q) = %q, want %q", in, got, want)
		}
	}
}
