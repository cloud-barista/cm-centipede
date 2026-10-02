package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MiB is the unit config.json speaks in, here and for the dummy files.
const MiB = 1 << 20

// BulkOptions describes one bulk phase: how much data, and how to shape it.
type BulkOptions struct {
	SizeMB    int
	BatchSize int
	IDOffset  int
	Seed      int64
	Weights   map[string]int
	// Workers is how many connections write at once where the engine's load
	// runs in parallel (MySQL, MariaDB, MongoDB); 0 or 1 means one.
	Workers int
}

// BulkPlan is what a BulkOptions works out to, before anything is written.
//
// It exists separately from the load so that --dry-run can print the row counts,
// and so the size arithmetic can be tested without a database.
type BulkPlan struct {
	SizeMB    int            `json:"sizeMB"`
	Rows      map[string]int `json:"rows"`
	TotalRows int            `json:"totalRows"`
}

// BulkResult is what the phase actually did.
type BulkResult struct {
	BulkPlan
	// ActualMB is what the engine reports occupying, which is not what was asked
	// for: row-size estimates are approximations and every engine adds its own
	// per-row, per-page and per-index overhead on top. The number that matters
	// when the next run has to fit on the same disk is this one, not SizeMB.
	ActualMB int    `json:"actualMB"`
	Seconds  int    `json:"seconds"`
	Method   string `json:"method"`
}

// Empty reports whether the plan writes nothing.
func (p BulkPlan) Empty() bool { return p.TotalRows == 0 }

// ---------------------------------------------------------------------------
// the tables
// ---------------------------------------------------------------------------

// bulkTable is one table the bulk phase fills.
//
// The list is deliberately not every table in shop_db. categories, coupons and
// wishlist describe a shop rather than its traffic - a shop with four million
// categories is not a bigger test, just a stranger one - and payments is left
// alone because its own trigger is the thing a migration test wants intact.
//
// Order matters: a table may only reference one above it. That is what keeps the
// generated rows referentially valid without reading anything back.
type bulkTable struct {
	name string
	pk   string
	// bytesPerRow is the estimated stored size of one row including its share of
	// index overhead, used only to turn a megabyte budget into a row count.
	bytesPerRow int
	cols        []string
	gen         func(g *genCtx, id int) []any
}

var bulkTables = []bulkTable{
	{
		name: "customers", pk: "customer_id", bytesPerRow: 300,
		cols: []string{"customer_id", "email", "first_name", "last_name", "phone",
			"address", "city", "country", "grade", "is_active", "created_at"},
		gen: genCustomer,
	},
	{
		name: "products", pk: "product_id", bytesPerRow: 450,
		cols: []string{"product_id", "category_id", "name", "description", "price",
			"stock_quantity", "sku", "weight_kg", "is_active", "created_at"},
		gen: genProduct,
	},
	{
		name: "orders", pk: "order_id", bytesPerRow: 350,
		cols: []string{"order_id", "customer_id", "status", "total_amount",
			"shipping_address", "tracking_number", "notes", "created_at"},
		gen: genOrder,
	},
	{
		name: "order_items", pk: "item_id", bytesPerRow: 90,
		cols: []string{"item_id", "order_id", "product_id", "quantity", "unit_price", "discount_rate"},
		gen:  genOrderItem,
	},
	{
		name: "reviews", pk: "review_id", bytesPerRow: 400,
		cols: []string{"review_id", "product_id", "customer_id", "rating", "title",
			"content", "is_verified", "created_at"},
		gen: genReview,
	},
	{
		name: "inventory_log", pk: "log_id", bytesPerRow: 130,
		cols: []string{"log_id", "product_id", "change_type", "quantity_change",
			"quantity_after", "reference_id", "notes", "created_at"},
		gen: genInventoryLog,
	},
}

// BulkTableNames lists the tables the bulk phase fills, in load order.
func BulkTableNames() []string {
	out := make([]string, 0, len(bulkTables))
	for _, t := range bulkTables {
		out = append(out, t.name)
	}
	return out
}

// DefaultBulkWeights is the split used when config.json names none. The shares
// are roughly how an actual shop's bytes sit: mostly transaction lines, then the
// orders above them, with the catalogue a rounding error next to both.
func DefaultBulkWeights() map[string]int {
	return map[string]int{
		"order_items": 30, "orders": 20, "reviews": 20,
		"customers": 12, "inventory_log": 10, "products": 8,
	}
}

// ---------------------------------------------------------------------------
// planning
// ---------------------------------------------------------------------------

// PlanBulk turns a megabyte budget into per-table row counts.
//
// The weights are normalized by their own sum rather than required to add up to
// 100, so that switching a table off - or asking for one table only - does not
// also mean rebalancing the rest by hand.
func PlanBulk(o BulkOptions) (BulkPlan, error) {
	plan := BulkPlan{SizeMB: o.SizeMB, Rows: map[string]int{}}
	if o.SizeMB <= 0 {
		return plan, nil
	}

	weights := o.Weights
	if len(weights) == 0 {
		weights = DefaultBulkWeights()
	}
	known := map[string]bool{}
	for _, t := range bulkTables {
		known[t.name] = true
	}
	var sum int
	for name, w := range weights {
		if !known[name] {
			return plan, fmt.Errorf("database weights: unknown table %q (%s)",
				name, strings.Join(BulkTableNames(), ", "))
		}
		if w < 0 {
			return plan, fmt.Errorf("database weights: %s=%d is negative", name, w)
		}
		sum += w
	}
	if sum == 0 {
		return plan, fmt.Errorf("database weights: every weight is 0, so no table would be filled")
	}

	budget := int64(o.SizeMB) * MiB
	for _, t := range bulkTables {
		w := weights[t.name]
		if w == 0 {
			continue
		}
		rows := int(budget * int64(w) / int64(sum) / int64(t.bytesPerRow))
		// A table with a weight is a table that was asked for. Rounding it away
		// would silently drop a foreign key target the tables below it need.
		if rows < 1 {
			rows = 1
		}
		plan.Rows[t.name] = rows
		plan.TotalRows += rows
	}
	return plan, nil
}

// String renders the plan as one line for the run log.
func (p BulkPlan) String() string {
	if p.Empty() {
		return "no bulk rows"
	}
	names := make([]string, 0, len(p.Rows))
	for n := range p.Rows {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%s", n, humanCount(p.Rows[n])))
	}
	return fmt.Sprintf("%s rows (%s)", humanCount(p.TotalRows), strings.Join(parts, " "))
}

// ---------------------------------------------------------------------------
// loading
// ---------------------------------------------------------------------------

// LoadBulk writes the planned rows into a database that already holds the fixture.
//
// It runs after LoadShopDB, never instead of it: the fixture creates the tables
// and the DDL objects a migration is actually being tested on, and these rows are
// only volume on top. Running it the other way round would not work either - the
// fixture's seed inserts reference their parents by hard-coded id, and bulk rows
// inserted first would push the AUTO_INCREMENT counter past them.
func LoadBulk(ctx context.Context, cfg Config, o BulkOptions, plan BulkPlan) (BulkResult, error) {
	res := BulkResult{BulkPlan: plan}
	if plan.Empty() {
		return res, nil
	}
	start := time.Now()
	var err error
	switch cfg.Engine {
	case MySQL, MariaDB:
		res.Method = "batched INSERT"
		err = loadBulkMySQL(ctx, cfg, o, plan)
	case Postgres:
		res.Method = "COPY"
		err = loadBulkPostgres(ctx, cfg, o, plan)
	case MongoDB:
		res.Method = "InsertMany"
		err = loadBulkMongo(ctx, cfg, o, plan)
	default:
		return res, fmt.Errorf("unsupported engine %q", cfg.Engine)
	}
	if err != nil {
		return res, err
	}
	res.Seconds = int(time.Since(start).Seconds())
	if mb, serr := measureSize(ctx, cfg); serr != nil {
		log.Printf("[warn] could not measure the loaded size: %v", serr)
	} else {
		res.ActualMB = mb
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// MySQL / MariaDB
// ---------------------------------------------------------------------------

// loadBulkMySQL fills the tables with multi-row INSERT batches.
//
// Not LOAD DATA LOCAL INFILE, which would be several times faster: it needs
// local_infile=ON on the server, and both RDS and NCP ship it off. Carrying a
// fast path that is refused on every managed instance this environment actually
// provisions - and a fallback underneath it for when it is - would be two code
// paths where the slower one does all the work.
func loadBulkMySQL(ctx context.Context, cfg Config, o BulkOptions, plan BulkPlan) error {
	dbName := targetDB(cfg)
	db, err := sql.Open("mysql", mysqlBulkDSN(cfg, dbName))
	if err != nil {
		return err
	}
	defer db.Close()
	// The control connection: the triggers, keys and the session settings the
	// DDL below needs all go through it. The rows themselves are written by
	// connections of their own, opened per table.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("connect %s/%s: %w", cfg.Host, dbName, err)
	}
	setMySQLBulkSession(ctx, conn, true)

	restore, err := suspendMySQLTriggers(ctx, conn, dbName)
	if err != nil {
		return err
	}
	defer restore()

	// Deferred after the triggers, so it runs first: keys back, then triggers.
	restoreKeys := dropMySQLKeys(ctx, db, conn, cfg.Engine, dbName, plannedTables(plan))
	defer restoreKeys()

	g, err := newGenCtx(ctx, o, plan, &sqlRefs{db: db, engine: cfg.Engine})
	if err != nil {
		return err
	}

	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		if err := insertParallelMySQL(ctx, db, t, rows, g, o); err != nil {
			return fmt.Errorf("bulk %s: %w", t.name, err)
		}
	}
	return nil
}

// setMySQLBulkSession turns off the per-row checks on one connection. Every
// connection that writes rows needs it, since session settings stay with the
// session.
//
// The parents are generated before the children and referenced by id, so the
// constraints are satisfied either way; checking them per row only costs an
// index probe each. unique_checks is the same bargain for the unique email and
// sku, which are generated from the row number and cannot collide. A refusal is
// reported once, by the control connection (loud), and not again per writer.
func setMySQLBulkSession(ctx context.Context, conn *sql.Conn, loud bool) {
	for _, s := range []string{"SET SESSION foreign_key_checks = 0", "SET SESSION unique_checks = 0"} {
		if _, err := conn.ExecContext(ctx, s); err != nil && loud {
			log.Printf("[warn] %s failed (continuing): %v", s, err)
		}
	}
}

// mysqlBulkDSN is the fixture loader's DSN with parameter interpolation added.
//
// Without it the driver prepares, executes and closes a statement for every
// batch - three round trips instead of one, which against a managed instance
// reached over its public endpoint is most of the load's wall clock. The values
// are ints, floats, strings, bools and timestamps, all of which the driver's own
// interpolation escapes.
func mysqlBulkDSN(cfg Config, dbName string) string {
	mc := mysqlConfig(cfg, dbName)
	mc.InterpolateParams = true
	return mc.FormatDSN()
}

// insertParallelMySQL writes one table's rows over o.Workers connections at
// once (pumpBatches), each a mysqlWriter.
func insertParallelMySQL(ctx context.Context, db *sql.DB, t bulkTable, rows int, g *genCtx, o BulkOptions) error {
	prefix := "INSERT INTO " + quoteMySQL(t.name) + " (" + joinQuoted(t.cols, quoteMySQL) + ") VALUES "
	n := workerCount(o)
	writers := make([]bulkWriter, 0, n)
	var conns []*sql.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < n; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		conns = append(conns, c)
		setMySQLBulkSession(ctx, c, false)
		writers = append(writers, &mysqlWriter{conn: c, prefix: prefix, cols: len(t.cols)})
	}
	return pumpBatches(ctx, t, rows, mysqlRowsPerStatement(t, o.BatchSize), g, o.IDOffset, writers)
}

// mysqlCommitRows is how many rows a writer puts in one transaction. Committing
// every statement means a redo log flush - and a binlog one where it is on -
// every few thousand rows; RDS keeps innodb_flush_log_at_trx_commit=1 and a
// master account cannot change it, so fewer commits is the lever that is left.
// Large enough to make the flushes rare, small enough that a transaction does
// not hold an undo log of millions of rows.
const mysqlCommitRows = 50_000

// mysqlWriter is one connection of a parallel MySQL/MariaDB load: multi-row
// INSERTs inside a transaction it commits every mysqlCommitRows rows.
type mysqlWriter struct {
	conn   *sql.Conn
	prefix string
	cols   int
	tx     *sql.Tx
	inTx   int
}

func (w *mysqlWriter) write(ctx context.Context, rows [][]any) error {
	if w.tx == nil {
		tx, err := w.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		w.tx = tx
	}
	args := make([]any, 0, len(rows)*w.cols)
	for _, r := range rows {
		args = append(args, r...)
	}
	if _, err := w.tx.ExecContext(ctx, w.prefix+placeholders(len(rows), w.cols), args...); err != nil {
		return err
	}
	w.inTx += len(rows)
	if w.inTx < mysqlCommitRows {
		return nil
	}
	err := w.tx.Commit()
	w.tx, w.inTx = nil, 0
	return err
}

func (w *mysqlWriter) finish(_ context.Context, ok bool) error {
	if w.tx == nil {
		return nil
	}
	tx := w.tx
	w.tx = nil
	if ok {
		return tx.Commit()
	}
	_ = tx.Rollback()
	return nil
}

// mysqlRowsPerStatement is how many rows one INSERT carries: batch, raised to
// mysqlStatementRows, and then held under two ceilings - 60000 values, inside
// the 65535 placeholders a prepared statement may have, and about 4 MiB of
// statement text, well inside the 16 MiB max_allowed_packet MariaDB ships with
// (MySQL's is 64). A bigger statement is fewer round trips and fewer parses.
const mysqlStatementRows = 10_000

func mysqlRowsPerStatement(t bulkTable, batch int) int {
	n := batch
	if n < mysqlStatementRows {
		n = mysqlStatementRows
	}
	if max := 60000 / len(t.cols); n > max {
		n = max
	}
	if max := (4 << 20) / t.bytesPerRow; n > max {
		n = max
	}
	if n < 1 {
		n = 1
	}
	return n
}

func placeholders(rows, cols int) string {
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", cols), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(one+",", rows), ",")
}

// mysqlTrigger is a trigger taken out of the way, and how to put it back.
type mysqlTrigger struct {
	name    string
	stmt    string
	sqlMode string
}

// suspendMySQLTriggers drops the INSERT triggers on the bulk tables and returns a
// function that recreates them.
//
// They have to go. trg_after_order_item_insert decrements product stock and
// writes an inventory_log row for every order item - correct for a shop, but
// against millions of generated rows it triples the write volume and drives the
// stock of a few thousand products deep into the negative, at which point
// trg_before_product_update starts rejecting rows outright.
//
// MySQL has no way to disable a trigger, so the only way to do this is to drop
// and recreate. SHOW CREATE TRIGGER hands back the exact statement, DEFINER and
// all, and it is replayed under the sql_mode it was created with, since a trigger
// body is parsed under the session's mode rather than a stored one.
func suspendMySQLTriggers(ctx context.Context, conn *sql.Conn, schema string) (func(), error) {
	names, err := insertTriggerNames(ctx, conn, schema)
	if err != nil {
		return nil, err
	}
	var saved []mysqlTrigger
	for _, n := range names {
		tr, err := showCreateTrigger(ctx, conn, n)
		if err != nil {
			return nil, fmt.Errorf("back up trigger %s: %w", n, err)
		}
		if _, err := conn.ExecContext(ctx, "DROP TRIGGER "+quoteMySQL(n)); err != nil {
			return nil, fmt.Errorf("drop trigger %s: %w", n, err)
		}
		saved = append(saved, tr)
	}
	if len(saved) > 0 {
		log.Printf("database: %d insert trigger(s) suspended for the bulk load", len(saved))
	}

	return func() {
		var orig string
		if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&orig); err != nil {
			log.Printf("[warn] could not read sql_mode before restoring triggers: %v", err)
		}
		for _, tr := range saved {
			if tr.sqlMode != "" {
				if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ?", tr.sqlMode); err != nil {
					log.Printf("[warn] could not set sql_mode for %s: %v", tr.name, err)
				}
			}
			if _, err := conn.ExecContext(ctx, tr.stmt); err != nil {
				// Loud, because the database is now missing a trigger the fixture
				// created and nothing later in the run would notice.
				log.Printf("[ERROR] trigger %s was dropped for the bulk load and could NOT be recreated: %v\n"+
					"  recreate it by hand, or reload the fixture:\n%s", tr.name, err, tr.stmt)
			}
		}
		if orig != "" {
			if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ?", orig); err != nil {
				log.Printf("[warn] could not restore sql_mode: %v", err)
			}
		}
		if len(saved) > 0 {
			log.Printf("database: %d insert trigger(s) restored", len(saved))
		}
	}, nil
}

func insertTriggerNames(ctx context.Context, conn *sql.Conn, schema string) ([]string, error) {
	q := `SELECT TRIGGER_NAME FROM information_schema.TRIGGERS
	      WHERE TRIGGER_SCHEMA = ? AND EVENT_MANIPULATION = 'INSERT'
	        AND EVENT_OBJECT_TABLE IN (` + strings.TrimSuffix(strings.Repeat("?,", len(bulkTables)), ",") + `)`
	args := []any{schema}
	for _, t := range bulkTables {
		args = append(args, t.name)
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list triggers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// showCreateTrigger reads one trigger's definition. The result set has gained
// columns across MySQL and MariaDB versions, so the wanted ones are picked by
// name rather than by position.
func showCreateTrigger(ctx context.Context, conn *sql.Conn, name string) (mysqlTrigger, error) {
	tr := mysqlTrigger{name: name}
	rows, err := conn.QueryContext(ctx, "SHOW CREATE TRIGGER "+quoteMySQL(name))
	if err != nil {
		return tr, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return tr, err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return tr, err
		}
		return tr, fmt.Errorf("no definition returned")
	}
	cells := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return tr, err
	}
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "sql original statement":
			tr.stmt = string(cells[i])
		case "sql_mode":
			tr.sqlMode = string(cells[i])
		}
	}
	if tr.stmt == "" {
		return tr, fmt.Errorf("SHOW CREATE TRIGGER returned no statement")
	}
	return tr, nil
}

// ---------------------------------------------------------------------------
// PostgreSQL
// ---------------------------------------------------------------------------

// loadBulkPostgres fills the tables with COPY FROM STDIN, which is the fastest
// way into PostgreSQL and needs no server-side setting to be enabled.
func loadBulkPostgres(ctx context.Context, cfg Config, o BulkOptions, plan BulkPlan) error {
	dbName := targetDB(cfg)
	db, err := sql.Open("postgres", pgDSN(cfg, dbName))
	if err != nil {
		return err
	}
	defer db.Close()
	// One connection throughout: session_replication_role below only applies to
	// the session that set it, so every COPY has to run on that same session.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("connect postgres(%s) %s: %w", dbName, cfg.Host, err)
	}

	schema := pgSchema(cfg)
	tables := plannedTables(plan)

	// Deferred in reverse: the indexes are rebuilt first, then the checks come
	// back, and the tables are analyzed last, once both are in place.
	defer analyzePG(ctx, conn, tables)
	restoreChecks := suspendPGChecks(ctx, conn, schema, tables)
	defer restoreChecks()
	restoreIndexes := dropPGIndexes(ctx, db, conn, schema, tables)
	defer restoreIndexes()

	g, err := newGenCtx(ctx, o, plan, &sqlRefs{db: db, engine: cfg.Engine})
	if err != nil {
		return err
	}

	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		if err := copyPostgres(ctx, conn, t, rows, g, o); err != nil {
			return fmt.Errorf("bulk %s: %w", t.name, err)
		}
	}
	return nil
}

// plannedTables lists the bulk tables the plan writes rows into, in load order.
func plannedTables(plan BulkPlan) []string {
	var out []string
	for _, t := range bulkTables {
		if plan.Rows[t.name] > 0 {
			out = append(out, t.name)
		}
	}
	return out
}

// pgCopyChunk is how many rows go into one COPY transaction. One transaction for
// the whole table would hold a single snapshot and its WAL open for millions of
// rows; chunking bounds that without costing much.
const pgCopyChunk = 100_000

func copyPostgres(ctx context.Context, conn *sql.Conn, t bulkTable, rows int, g *genCtx, o BulkOptions) error {
	prog := newProgress(t.name, rows)
	for done := 0; done < rows; {
		n := pgCopyChunk
		if rem := rows - done; rem < n {
			n = rem
		}
		if err := copyChunkPostgres(ctx, conn, t, o.IDOffset+done, n, g); err != nil {
			return err
		}
		done += n
		prog.add(n)
	}
	prog.done()
	return nil
}

func copyChunkPostgres(ctx context.Context, conn *sql.Conn, t bulkTable, firstID, n int, g *genCtx) (err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// Unqualified: the DSN pins search_path to the fixture schema, the same way
	// the fixture SQL itself relies on.
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(t.name, t.cols...))
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if _, err = stmt.ExecContext(ctx, t.gen(g, firstID+i)...); err != nil {
			return err
		}
	}
	// The empty Exec is what flushes the buffered rows; without it COPY sends
	// nothing and the commit succeeds having written no rows at all.
	if _, err = stmt.ExecContext(ctx); err != nil {
		return err
	}
	if err = stmt.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

// suspendPGChecks turns off the foreign key checks and the user triggers on the
// tables being filled, and returns a function that turns them back on.
//
// The user triggers go for the same reason MySQL's are dropped. The foreign keys
// go because each check is a lookup plus a FOR KEY SHARE lock on the parent row:
// with children spread at random over millions of orders, that is a random read
// and a dirtied page per row, which on a small instance is most of the load's
// wall clock. The generator only ever draws parent ids from ranges it created or
// read back, so the rows are referentially valid without being checked.
//
// session_replication_role = replica does both at once and touches nothing but
// this session, so there is nothing to put back if the process dies. It needs a
// privilege a managed instance's master account may lack; when it is refused,
// the triggers are disabled in place and the foreign keys dropped, to be added
// back - and so validated - once the load is done.
func suspendPGChecks(ctx context.Context, conn *sql.Conn, schema string, tables []string) func() {
	err := setReplicaRole(ctx, conn)
	if err == nil {
		log.Printf("database: session_replication_role = replica, foreign key checks and user triggers are off for the bulk load")
		return func() {
			if _, err := conn.ExecContext(ctx, "RESET session_replication_role"); err != nil {
				log.Printf("[warn] could not reset session_replication_role: %v", err)
			}
		}
	}
	log.Printf("[warn] session_replication_role = replica refused (%v), "+
		"disabling triggers and dropping foreign keys instead", err)

	restoreTriggers := suspendPGTriggers(ctx, conn, tables)
	restoreFKs := dropPGForeignKeys(ctx, conn, schema, tables)
	return func() {
		restoreFKs()
		restoreTriggers()
	}
}

// setReplicaRole switches the session to replica mode and confirms it took.
func setReplicaRole(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "SET session_replication_role = replica"); err != nil {
		return err
	}
	var role string
	if err := conn.QueryRowContext(ctx, "SHOW session_replication_role").Scan(&role); err != nil {
		return err
	}
	if role != "replica" {
		return fmt.Errorf("session_replication_role is %q after SET", role)
	}
	return nil
}

// suspendPGTriggers turns off the user triggers on the tables being filled.
// PostgreSQL can do it in place, and only USER triggers are touched, so the
// foreign keys - enforced by system triggers - keep checking.
func suspendPGTriggers(ctx context.Context, conn *sql.Conn, tables []string) func() {
	var disabled []string
	for _, name := range tables {
		q := "ALTER TABLE " + pq.QuoteIdentifier(name) + " DISABLE TRIGGER USER"
		if _, err := conn.ExecContext(ctx, q); err != nil {
			log.Printf("[warn] could not disable triggers on %s (continuing, the load will be slower): %v", name, err)
			continue
		}
		disabled = append(disabled, name)
	}
	if len(disabled) > 0 {
		log.Printf("database: triggers disabled on %s for the bulk load", strings.Join(disabled, ", "))
	}
	return func() {
		for _, name := range disabled {
			q := "ALTER TABLE " + pq.QuoteIdentifier(name) + " ENABLE TRIGGER USER"
			if _, err := conn.ExecContext(ctx, q); err != nil {
				log.Printf("[ERROR] triggers on %s were disabled for the bulk load and could NOT be re-enabled: %v\n"+
					"  run it by hand: %s", name, err, q)
			}
		}
		if len(disabled) > 0 {
			log.Printf("database: triggers re-enabled on %s", strings.Join(disabled, ", "))
		}
	}
}

// pgDDL is a schema object taken out of the way for the load, and the
// statements that remove and recreate it.
type pgDDL struct {
	name, table    string
	drop, recreate string
}

// dropPGForeignKeys drops the foreign keys declared on the tables being filled
// and returns a function that adds them back.
//
// Only the child side matters: a foreign key is checked when a row is inserted
// into the table that declares it, never when one is inserted into the table it
// points at. pg_get_constraintdef hands back the clause exactly, ON DELETE and
// ON UPDATE actions included, and adding it back validates every row.
func dropPGForeignKeys(ctx context.Context, conn *sql.Conn, schema string, tables []string) func() {
	defs, err := queryPGDDL(ctx, conn, `
		SELECT c.conname, t.relname,
		       format('ALTER TABLE %I.%I DROP CONSTRAINT %I', n.nspname, t.relname, c.conname),
		       format('ALTER TABLE %I.%I ADD CONSTRAINT %I ', n.nspname, t.relname, c.conname)
		         || pg_get_constraintdef(c.oid)
		FROM   pg_constraint c
		JOIN   pg_class t     ON t.oid = c.conrelid
		JOIN   pg_namespace n ON n.oid = t.relnamespace
		WHERE  c.contype = 'f' AND n.nspname = $1 AND t.relname = ANY($2)
		ORDER  BY t.relname, c.conname`, schema, tables)
	if err != nil {
		log.Printf("[warn] could not list foreign keys (continuing, they keep checking): %v", err)
		return func() {}
	}
	dropped := dropPGDDL(ctx, conn, "foreign key", defs)
	return func() {
		// No progress view covers validating a foreign key, so each one is only
		// timed.
		recreatePGDDL(ctx, conn, "foreign key", dropped, nil)
	}
}

// pgMaintenanceWorkMem is what each index rebuild may sort in. Large enough that
// the biggest index here sorts in a few passes, small enough to leave a 4 GB
// instance its shared buffers.
const pgMaintenanceWorkMem = "512MB"

// dropPGIndexes drops the plain secondary indexes on the tables being filled and
// returns a function that rebuilds them.
//
// Maintaining an index on a random column costs a random page per row once the
// index outgrows memory; building it afterwards is one sort. Primary keys stay,
// since their ids are generated in order and cost nothing to maintain, and so do
// the indexes behind UNIQUE constraints, which can only go with the constraint.
//
// The rebuild is the slow end of a large load, so it reports as it goes: each
// index as it starts and finishes, and in between what pg_stat_progress_create_index
// says the build is doing, read from a second connection of db.
func dropPGIndexes(ctx context.Context, db *sql.DB, conn *sql.Conn, schema string, tables []string) func() {
	defs, err := queryPGDDL(ctx, conn, `
		SELECT ic.relname, t.relname,
		       format('DROP INDEX %I.%I', n.nspname, ic.relname),
		       pg_get_indexdef(i.indexrelid)
		FROM   pg_index i
		JOIN   pg_class ic    ON ic.oid = i.indexrelid
		JOIN   pg_class t     ON t.oid = i.indrelid
		JOIN   pg_namespace n ON n.oid = t.relnamespace
		WHERE  n.nspname = $1 AND t.relname = ANY($2)
		  AND  NOT i.indisprimary
		  AND  NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid)
		ORDER  BY t.relname, ic.relname`, schema, tables)
	if err != nil {
		log.Printf("[warn] could not list secondary indexes (continuing, they stay in place): %v", err)
		return func() {}
	}
	dropped := dropPGDDL(ctx, conn, "index", defs)
	return func() {
		if len(dropped) == 0 {
			return
		}
		if _, err := conn.ExecContext(ctx, "SET maintenance_work_mem = '"+pgMaintenanceWorkMem+"'"); err != nil {
			log.Printf("[warn] could not raise maintenance_work_mem (the rebuild will be slower): %v", err)
		}
		recreatePGDDL(ctx, conn, "index", dropped, newPGIndexWatcher(ctx, db, conn))
		if _, err := conn.ExecContext(ctx, "RESET maintenance_work_mem"); err != nil {
			log.Printf("[warn] could not reset maintenance_work_mem: %v", err)
		}
	}
}

func queryPGDDL(ctx context.Context, conn *sql.Conn, q string, schema string, tables []string) ([]pgDDL, error) {
	rows, err := conn.QueryContext(ctx, q, schema, pq.Array(tables))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgDDL
	for rows.Next() {
		var d pgDDL
		if err := rows.Scan(&d.name, &d.table, &d.drop, &d.recreate); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// dropPGDDL drops each object and returns the ones that are now gone, reported
// as one line. The definitions are held in memory only; if the process dies
// before the rebuild, the names logged here are what to look up in the fixture
// SQL, which holds every one of them.
func dropPGDDL(ctx context.Context, conn *sql.Conn, kind string, defs []pgDDL) []pgDDL {
	var dropped []pgDDL
	var names []string
	for _, d := range defs {
		if _, err := conn.ExecContext(ctx, d.drop); err != nil {
			log.Printf("[warn] could not drop %s %s (continuing, the load will be slower): %v", kind, d.name, err)
			continue
		}
		dropped = append(dropped, d)
		names = append(names, d.name)
	}
	if len(dropped) > 0 {
		log.Printf("database: %d %s dropped for the bulk load, rebuilt once the test data is in (%s)",
			len(dropped), plural(kind, len(dropped)), strings.Join(names, ", "))
	}
	return dropped
}

// recreatePGDDL puts the dropped objects back one at a time, logging each as it
// starts and finishes. watch, when not nil, reports on a build while it runs.
func recreatePGDDL(ctx context.Context, conn *sql.Conn, kind string, dropped []pgDDL, watch *ddlWatcher) {
	if len(dropped) == 0 {
		return
	}
	log.Printf("database: rebuilding %d %s", len(dropped), plural(kind, len(dropped)))
	start := time.Now()
	failed := 0
	for i, d := range dropped {
		label := fmt.Sprintf("  [%d/%d] %s", i+1, len(dropped), d.name)
		log.Printf("%s (%s) ...", label, d.table)
		began := time.Now()
		stop := watch.start(label)
		_, err := conn.ExecContext(ctx, d.recreate)
		stop()
		if err != nil {
			failed++
			// Loud, because the database is now missing an object the fixture
			// created and nothing later in the run would notice.
			log.Printf("[ERROR] %s %s was dropped for the bulk load and could NOT be recreated: %v\n"+
				"  recreate it by hand, or reload the fixture:\n  %s;", kind, d.name, err, d.recreate)
			continue
		}
		log.Printf("%s done in %s", label, time.Since(began).Round(time.Second))
	}
	log.Printf("database: %d %s rebuilt in %s",
		len(dropped)-failed, plural(kind, len(dropped)-failed), time.Since(start).Round(time.Second))
}

// newPGIndexWatcher returns a watcher that reads pg_stat_progress_create_index
// for conn's session, or nil - which reports nothing - if the session cannot be
// identified.
//
// It reads from a connection of db's own: conn is busy running the CREATE INDEX
// it is watching. The view shows a session of the same role without any extra
// privilege, which is what the load connects as.
func newPGIndexWatcher(ctx context.Context, db *sql.DB, conn *sql.Conn) *ddlWatcher {
	var pid int
	if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		log.Printf("[warn] could not identify the load session, index rebuilds will not report progress: %v", err)
		return nil
	}
	return &ddlWatcher{ctx: ctx, poll: func(ctx context.Context) (string, bool) {
		var (
			phase                   string
			blocksDone, blocksTotal int64
			tuplesDone, tuplesTotal int64
		)
		err := db.QueryRowContext(ctx, `
			SELECT phase, blocks_done, blocks_total, tuples_done, tuples_total
			FROM   pg_stat_progress_create_index WHERE pid = $1`, pid).
			Scan(&phase, &blocksDone, &blocksTotal, &tuplesDone, &tuplesTotal)
		if err != nil {
			return "", false
		}
		return formatIndexProgress(phase, blocksDone, blocksTotal, tuplesDone, tuplesTotal), true
	}}
}

// formatIndexProgress renders one sample. A B-tree build scans the table (counted
// in blocks), sorts (not counted at all), then loads the sorted tuples (counted
// in tuples); whichever counter the current phase fills is the one shown.
func formatIndexProgress(phase string, blocksDone, blocksTotal, tuplesDone, tuplesTotal int64) string {
	phase = strings.TrimPrefix(phase, "building index: ")
	switch {
	case tuplesTotal > 0:
		return fmt.Sprintf("%s %.0f%% (%s/%s tuples)", phase,
			float64(tuplesDone)*100/float64(tuplesTotal), humanCount(int(tuplesDone)), humanCount(int(tuplesTotal)))
	case blocksTotal > 0:
		return fmt.Sprintf("%s %.0f%% (%s/%s blocks)", phase,
			float64(blocksDone)*100/float64(blocksTotal), humanCount(int(blocksDone)), humanCount(int(blocksTotal)))
	default:
		return phase
	}
}

// plural spells kind for n of them: index/indexes, foreign key/foreign keys.
func plural(kind string, n int) string {
	if n == 1 {
		return kind
	}
	if strings.HasSuffix(kind, "x") {
		return kind + "es"
	}
	return kind + "s"
}

// analyzePG refreshes the planner statistics of the loaded tables, which a bulk
// load leaves describing the tables as they were before it.
//
// One table at a time, so a long run shows which table it is on.
func analyzePG(ctx context.Context, conn *sql.Conn, tables []string) {
	if len(tables) == 0 {
		return
	}
	log.Printf("database: analyzing %d tables", len(tables))
	start := time.Now()
	for i, t := range tables {
		began := time.Now()
		if _, err := conn.ExecContext(ctx, "ANALYZE "+pq.QuoteIdentifier(t)); err != nil {
			log.Printf("[warn] could not analyze %s: %v", t, err)
			continue
		}
		log.Printf("  [%d/%d] %s analyzed in %s", i+1, len(tables), t, time.Since(began).Round(time.Second))
	}
	log.Printf("database: tables analyzed in %s", time.Since(start).Round(time.Second))
}

// ---------------------------------------------------------------------------
// MongoDB
// ---------------------------------------------------------------------------

// loadBulkMongo fills the collections with InsertMany batches, o.Workers of
// them in flight at once (pumpBatches), each a mongoWriter.
//
// The batches are mongoBatchSize documents or more: a batch is a round trip,
// and an order_items document is ~150 bytes, so a thousand of them leave the
// connection waiting on the reply far longer than it spends sending.
func loadBulkMongo(ctx context.Context, cfg Config, o BulkOptions, plan BulkPlan) error {
	client, err := mongoConnect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)
	db := client.Database(targetDB(cfg))

	restoreIndexes := dropMongoIndexes(ctx, client, db, plannedTables(plan))
	defer restoreIndexes()

	g, err := newGenCtx(ctx, o, plan, &mongoRefs{db: db})
	if err != nil {
		return err
	}

	batch := o.BatchSize
	if batch < mongoBatchSize {
		batch = mongoBatchSize
	}
	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		coll := db.Collection(t.name)
		writers := make([]bulkWriter, workerCount(o))
		for i := range writers {
			writers[i] = &mongoWriter{coll: coll, t: t}
		}
		if err := pumpBatches(ctx, t, rows, batch, g, o.IDOffset, writers); err != nil {
			return fmt.Errorf("bulk %s: %w", t.name, err)
		}
	}
	return nil
}

// mongoBatchSize is the fewest documents one InsertMany carries. The driver
// splits a batch past the server's 48 MB message size on its own.
const mongoBatchSize = 10_000

// mongoWriter is one of a parallel MongoDB load's writers. The client's
// connection pool hands each concurrent InsertMany a connection of its own.
type mongoWriter struct {
	coll *mongo.Collection
	t    bulkTable
}

// write inserts one batch, unordered: the documents are independent, so the
// server may apply them in any order instead of stopping at the first rejected
// one.
func (w *mongoWriter) write(ctx context.Context, rows [][]any) error {
	docs := make([]any, len(rows))
	for i, r := range rows {
		docs[i] = rowToDoc(w.t, r)
	}
	_, err := w.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	return err
}

// finish has nothing to do: every InsertMany is acknowledged on its own.
func (w *mongoWriter) finish(context.Context, bool) error { return nil }

// rowToDoc turns a generated row into a document shaped like the fixture's own:
// the primary key becomes _id rather than staying a named field, which is what
// the shop_db_mongo.json documents do.
func rowToDoc(t bulkTable, vals []any) bson.D {
	doc := make(bson.D, 0, len(vals))
	for i, c := range t.cols {
		name := c
		if c == t.pk {
			name = "_id"
		}
		doc = append(doc, bson.E{Key: name, Value: vals[i]})
	}
	return doc
}

// ---------------------------------------------------------------------------
// existing ids
// ---------------------------------------------------------------------------

// refSource reads ids that are already in the database.
//
// Needed for two things: categories, which the bulk phase never generates and
// products must point at, and any parent table whose weight is 0 - asking for
// order_items alone is reasonable, and they then have to hang off the orders the
// fixture already put there.
type refSource interface {
	existingIDs(ctx context.Context, table, pk string) ([]int, error)
}

// refSampleLimit caps how many ids are read back. A few thousand is plenty to
// spread children over, and it keeps this from pulling a previous bulk run's
// millions of ids into memory.
const refSampleLimit = 2000

type sqlRefs struct {
	db     *sql.DB
	engine Engine
}

func (s *sqlRefs) existingIDs(ctx context.Context, table, pk string) ([]int, error) {
	quote := quoteMySQL
	if s.engine == Postgres {
		quote = pq.QuoteIdentifier
	}
	q := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s LIMIT %d",
		quote(pk), quote(table), quote(pk), refSampleLimit)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type mongoRefs struct {
	db *mongo.Database
}

func (m *mongoRefs) existingIDs(ctx context.Context, table, _ string) ([]int, error) {
	cur, err := m.db.Collection(table).Find(ctx, bson.D{},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetLimit(refSampleLimit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []int
	for cur.Next(ctx) {
		var d struct {
			ID int `bson:"_id"`
		}
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d.ID)
	}
	return out, cur.Err()
}

// ---------------------------------------------------------------------------
// measuring
// ---------------------------------------------------------------------------

// measureSize asks the engine how much the fixture database now occupies, in MB.
func measureSize(ctx context.Context, cfg Config) (int, error) {
	dbName := targetDB(cfg)
	switch cfg.Engine {
	case MySQL, MariaDB:
		db, err := sql.Open("mysql", mysqlDSN(cfg, dbName))
		if err != nil {
			return 0, err
		}
		defer db.Close()
		if err := refreshInnoDBStats(ctx, db); err != nil {
			return 0, err
		}
		var bytes sql.NullInt64
		err = db.QueryRowContext(ctx,
			"SELECT SUM(data_length + index_length) FROM information_schema.TABLES WHERE table_schema = ?",
			dbName).Scan(&bytes)
		return int(bytes.Int64 / MiB), err
	case Postgres:
		db, err := sql.Open("postgres", pgDSN(cfg, dbName))
		if err != nil {
			return 0, err
		}
		defer db.Close()
		var bytes sql.NullInt64
		err = db.QueryRowContext(ctx, `
			SELECT SUM(pg_total_relation_size(c.oid))
			FROM   pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE  n.nspname = $1 AND c.relkind = 'r'`, pgSchema(cfg)).Scan(&bytes)
		return int(bytes.Int64 / MiB), err
	case MongoDB:
		client, err := mongoConnect(ctx, cfg)
		if err != nil {
			return 0, err
		}
		defer client.Disconnect(ctx)
		var stats struct {
			StorageSize float64 `bson:"storageSize"`
		}
		if err := client.Database(dbName).RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&stats); err != nil {
			return 0, err
		}
		return int(stats.StorageSize) / MiB, nil
	}
	return 0, fmt.Errorf("unsupported engine %q", cfg.Engine)
}

// refreshInnoDBStats recomputes the table statistics the size query reads.
//
// data_length and index_length in information_schema.TABLES are not measured
// when queried: InnoDB serves them from its cached persistent statistics, which
// a bulk load leaves badly out of date. Recalculation is automatic but
// asynchronous, and RDS ships innodb_stats_on_metadata=OFF, so reading
// information_schema does not trigger it either - which made a freshly loaded
// database report the size it had before the load, off by an order of magnitude.
//
// ANALYZE TABLE forces the recalculation. On InnoDB it samples a small number of
// index pages rather than scanning, so it costs little even on the largest table
// here. The result is still an estimate; it is simply an estimate of the table
// that now exists.
func refreshInnoDBStats(ctx context.Context, db *sql.DB) error {
	names := make([]string, 0, len(bulkTables))
	for _, t := range bulkTables {
		names = append(names, quoteMySQL(t.name))
	}
	// ANALYZE returns a status row per table, so it is a query rather than an exec.
	rows, err := db.QueryContext(ctx, "ANALYZE TABLE "+strings.Join(names, ", "))
	if err != nil {
		return fmt.Errorf("analyze tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() { // drained, not read: the status rows say "OK" per table
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// quoteMySQL wraps an identifier in backticks. Every identifier passed to it is
// a constant from bulkTables, but quoting keeps a name that happens to be a
// reserved word from turning into a syntax error.
func quoteMySQL(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func joinQuoted(names []string, quote func(string) string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quote(n)
	}
	return strings.Join(out, ", ")
}

// progress reports a long load at a readable pace: a line every few seconds
// rather than one per batch, and a rate so the remaining time is obvious.
//
// Two rates: the average since the start, and "now" - the rows of the last
// interval alone. The average is what the time left is judged by; "now" is what
// shows a load slowing down, which an average spread over half an hour hides.
// Safe for concurrent use: parallel writers report into one progress.
type progress struct {
	label string
	total int
	start time.Time

	mu       sync.Mutex
	seen     int
	lastAt   time.Time
	lastSeen int
}

const progressEvery = 5 * time.Second

func newProgress(label string, total int) *progress {
	now := time.Now()
	return &progress{label: label, total: total, start: now, lastAt: now}
}

func (p *progress) add(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen += n
	now := time.Now()
	since := now.Sub(p.lastAt)
	if since < progressEvery {
		return
	}
	recent := int(float64(p.seen-p.lastSeen) / since.Seconds())
	p.lastAt, p.lastSeen = now, p.seen
	log.Printf("  %s: %s/%s rows (%.0f%%, %s rows/s, now %s)",
		p.label, humanCount(p.seen), humanCount(p.total),
		float64(p.seen)*100/float64(p.total), humanCount(p.rate()), humanCount(recent))
}

func (p *progress) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	log.Printf("  %s: %s rows in %s (%s rows/s)",
		p.label, humanCount(p.total), time.Since(p.start).Round(time.Second), humanCount(p.rate()))
}

// rate is the average since the start; the caller holds mu.
func (p *progress) rate() int {
	sec := time.Since(p.start).Seconds()
	if sec <= 0 {
		return p.seen
	}
	return int(float64(p.seen) / sec)
}

// humanCount renders a row count as 1.2M / 340k / 512.
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
