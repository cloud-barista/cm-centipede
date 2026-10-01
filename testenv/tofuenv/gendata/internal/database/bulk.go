package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
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
	// One connection throughout: the session settings below, and the dropped
	// triggers, have to apply to every statement of the load.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("connect %s/%s: %w", cfg.Host, dbName, err)
	}

	// The parents are generated before the children and referenced by id, so the
	// constraints are satisfied either way; checking them per row only costs an
	// index probe each. unique_checks is the same bargain for the unique email
	// and sku, which are generated from the row number and cannot collide.
	for _, s := range []string{"SET SESSION foreign_key_checks = 0", "SET SESSION unique_checks = 0"} {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			log.Printf("[warn] %s failed (continuing): %v", s, err)
		}
	}

	restore, err := suspendMySQLTriggers(ctx, conn, dbName)
	if err != nil {
		return err
	}
	defer restore()

	g, err := newGenCtx(ctx, o, plan, &sqlRefs{db: db, engine: cfg.Engine})
	if err != nil {
		return err
	}

	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		if err := insertBatchedMySQL(ctx, conn, t, rows, g, o); err != nil {
			return fmt.Errorf("bulk %s: %w", t.name, err)
		}
	}
	return nil
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

func insertBatchedMySQL(ctx context.Context, conn *sql.Conn, t bulkTable, rows int, g *genCtx, o BulkOptions) error {
	perStmt := rowsPerStatement(o.BatchSize, len(t.cols))
	prefix := "INSERT INTO " + quoteMySQL(t.name) + " (" + joinQuoted(t.cols, quoteMySQL) + ") VALUES "
	prog := newProgress(t.name, rows)

	args := make([]any, 0, perStmt*len(t.cols))
	for done := 0; done < rows; {
		n := perStmt
		if rem := rows - done; rem < n {
			n = rem
		}
		args = args[:0]
		for i := 0; i < n; i++ {
			args = append(args, t.gen(g, o.IDOffset+done+i)...)
		}
		if _, err := conn.ExecContext(ctx, prefix+placeholders(n, len(t.cols)), args...); err != nil {
			return err
		}
		done += n
		prog.add(n)
	}
	prog.done()
	return nil
}

// rowsPerStatement caps a batch so the placeholder count stays well inside
// MySQL's 65535 limit per prepared statement.
func rowsPerStatement(batch, cols int) int {
	if batch < 1 {
		batch = 1000
	}
	if max := 60000 / cols; batch > max {
		batch = max
	}
	return batch
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
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect postgres(%s) %s: %w", dbName, cfg.Host, err)
	}

	restore := suspendPGTriggers(ctx, db, plan)
	defer restore()

	g, err := newGenCtx(ctx, o, plan, &sqlRefs{db: db, engine: cfg.Engine})
	if err != nil {
		return err
	}

	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		if err := copyPostgres(ctx, db, t, rows, g, o); err != nil {
			return fmt.Errorf("bulk %s: %w", t.name, err)
		}
	}
	return nil
}

// pgCopyChunk is how many rows go into one COPY transaction. One transaction for
// the whole table would hold a single snapshot and its WAL open for millions of
// rows; chunking bounds that without costing much.
const pgCopyChunk = 100_000

func copyPostgres(ctx context.Context, db *sql.DB, t bulkTable, rows int, g *genCtx, o BulkOptions) error {
	prog := newProgress(t.name, rows)
	for done := 0; done < rows; {
		n := pgCopyChunk
		if rem := rows - done; rem < n {
			n = rem
		}
		if err := copyChunkPostgres(ctx, db, t, o.IDOffset+done, n, g); err != nil {
			return err
		}
		done += n
		prog.add(n)
	}
	prog.done()
	return nil
}

func copyChunkPostgres(ctx context.Context, db *sql.DB, t bulkTable, firstID, n int, g *genCtx) (err error) {
	tx, err := db.BeginTx(ctx, nil)
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

// suspendPGTriggers turns off the user triggers on the tables being filled, for
// the same reason MySQL's are dropped. PostgreSQL can do it in place, and only
// USER triggers are touched, so the foreign keys - enforced by system triggers -
// keep checking.
func suspendPGTriggers(ctx context.Context, db *sql.DB, plan BulkPlan) func() {
	var disabled []string
	for _, t := range bulkTables {
		if plan.Rows[t.name] == 0 {
			continue
		}
		q := "ALTER TABLE " + pq.QuoteIdentifier(t.name) + " DISABLE TRIGGER USER"
		if _, err := db.ExecContext(ctx, q); err != nil {
			log.Printf("[warn] could not disable triggers on %s (continuing, the load will be slower): %v", t.name, err)
			continue
		}
		disabled = append(disabled, t.name)
	}
	if len(disabled) > 0 {
		log.Printf("database: triggers disabled on %s for the bulk load", strings.Join(disabled, ", "))
	}
	return func() {
		for _, name := range disabled {
			q := "ALTER TABLE " + pq.QuoteIdentifier(name) + " ENABLE TRIGGER USER"
			if _, err := db.ExecContext(ctx, q); err != nil {
				log.Printf("[ERROR] triggers on %s were disabled for the bulk load and could NOT be re-enabled: %v\n"+
					"  run it by hand: %s", name, err, q)
			}
		}
		if len(disabled) > 0 {
			log.Printf("database: triggers re-enabled on %s", strings.Join(disabled, ", "))
		}
	}
}

// ---------------------------------------------------------------------------
// MongoDB
// ---------------------------------------------------------------------------

func loadBulkMongo(ctx context.Context, cfg Config, o BulkOptions, plan BulkPlan) error {
	client, err := mongoConnect(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect(ctx)
	db := client.Database(targetDB(cfg))

	g, err := newGenCtx(ctx, o, plan, &mongoRefs{db: db})
	if err != nil {
		return err
	}

	batch := o.BatchSize
	if batch < 1 {
		batch = 1000
	}
	// Unordered: the inserts are independent, so the server may run them in
	// parallel instead of stopping the batch at the first rejected document.
	opts := options.InsertMany().SetOrdered(false)

	for _, t := range bulkTables {
		rows := plan.Rows[t.name]
		if rows == 0 {
			continue
		}
		coll := db.Collection(t.name)
		prog := newProgress(t.name, rows)
		docs := make([]any, 0, batch)
		for done := 0; done < rows; {
			n := batch
			if rem := rows - done; rem < n {
				n = rem
			}
			docs = docs[:0]
			for i := 0; i < n; i++ {
				docs = append(docs, rowToDoc(t, t.gen(g, o.IDOffset+done+i)))
			}
			if _, err := coll.InsertMany(ctx, docs, opts); err != nil {
				return fmt.Errorf("bulk %s: %w", t.name, err)
			}
			done += n
			prog.add(n)
		}
		prog.done()
	}
	return nil
}

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
type progress struct {
	label  string
	total  int
	seen   int
	start  time.Time
	lastAt time.Time
}

const progressEvery = 5 * time.Second

func newProgress(label string, total int) *progress {
	now := time.Now()
	return &progress{label: label, total: total, start: now, lastAt: now}
}

func (p *progress) add(n int) {
	p.seen += n
	if time.Since(p.lastAt) < progressEvery {
		return
	}
	p.lastAt = time.Now()
	log.Printf("  %s: %s/%s rows (%.0f%%, %s rows/s)",
		p.label, humanCount(p.seen), humanCount(p.total),
		float64(p.seen)*100/float64(p.total), humanCount(p.rate()))
}

func (p *progress) done() {
	log.Printf("  %s: %s rows in %s (%s rows/s)",
		p.label, humanCount(p.total), time.Since(p.start).Round(time.Second), humanCount(p.rate()))
}

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
