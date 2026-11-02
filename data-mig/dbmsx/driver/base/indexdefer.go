package base

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexdefer.go builds the secondary indexes of a large table after its rows
// rather than row by row as they load, for a MySQL-family restore.
//
// Maintaining an index on a column whose values arrive in random order costs a
// random page per row once the index outgrows the buffer pool; building it
// afterwards is one sorted pass. Below that size, keeping the index as the rows
// arrive is cheap, and taking it off and putting it back is the dearer of the
// two. Which side of that line a table is on is not known when the restore
// creates it - the rows come later in the dump - so the restore decides as the
// rows arrive: a table whose rows pass a threshold has its secondary indexes
// dropped there and then, and rebuilt once the dump has run. Dropping an index
// is a metadata change in InnoDB, so the rows loaded before it cost no more
// than they did.
//
// FULLTEXT indexes are the exception, deferred from a table's first row: their
// per-row upkeep is dear at any size, which is why MySQL Shell defers them by
// default.
//
// Only what can be put back exactly, and safely, is deferred: the plain (KEY)
// and FULLTEXT indexes of an InnoDB table, taken as SHOW CREATE TABLE prints
// them so that a COMMENT, INVISIBLE or a prefix length comes back as it was.
// PRIMARY and UNIQUE keys stay, and so does any index a foreign key may need -
// on its own columns or on the columns another table's key refers to - since
// InnoDB refuses to drop those.

const (
	// DeferIndexAuto sets the threshold from the target's buffer pool.
	DeferIndexAuto int64 = -1
	// DeferIndexOff keeps every index in place while the rows load.
	DeferIndexOff int64 = -2
)

// RestoreIndexDeferBytes is how many bytes of a table's INSERTs a MySQL-family
// restore lets through before it defers the table's secondary indexes: 0 defers
// them from the first row, DeferIndexOff never, and DeferIndexAuto - the
// default - a quarter of the target's innodb_buffer_pool_size, but no less than
// deferIndexMinBytes.
var RestoreIndexDeferBytes = DeferIndexAuto

const (
	// deferIndexMinBytes keeps a small buffer pool from deferring the indexes of
	// tables that would load as quickly with them.
	deferIndexMinBytes = 64 << 20
	// deferIndexUnknownBytes is the threshold when the buffer pool cannot be read.
	deferIndexUnknownBytes = 1 << 30
)

// IndexDeferrer follows the rows of one restore table by table and defers, and
// later rebuilds, the indexes of the tables that pass the threshold.
type IndexDeferrer struct {
	db        *sql.DB
	conn      *sql.Conn
	schema    string
	threshold int64    // < 0: defer nothing
	session   []string // what a rebuild connection runs, after rebuildSession
	progress  *RestoreProgress
	deferred  int // tables whose indexes have been held back, ever
	// alterProgress is how this server reports an ALTER's progress, found on
	// the first ask: see alterProgressPercent.
	alterProgress alterProgressSource
	tables        map[string]*deferTable
	pending       []*deferTable // tables holding indexes to rebuild, in the order deferred
	connections   int           // tables rebuilt at once; 0: RestoreWriters
}

// SetConnections sets how many tables Rebuild works on at once — the restore's
// writer count, so a rebuild asks no more of the target than the load did.
func (d *IndexDeferrer) SetConnections(n int) { d.connections = n }

type deferTable struct {
	name            string
	bytes           int64
	fulltextChecked bool
	btreeChecked    bool
	btree           []string // definitions to add back, as SHOW CREATE TABLE printed them
	fulltext        []string
}

// NewIndexDeferrer starts following a restore into schema. conn is the restore's
// own connection, the one indexes are dropped on; the rebuild takes connections
// of its own from db, and runs session on each first - whatever the engine's
// restore session sets that an ALTER of the table depends on. progress hears
// of both: the tables held back, as the restore's phase, and the rebuild, table
// by table, since an operator watching a restore otherwise sees it stop on its
// last table for as long as the indexes take.
func NewIndexDeferrer(ctx context.Context, db *sql.DB, conn *sql.Conn, schema string, session []string, progress *RestoreProgress) *IndexDeferrer {
	d := &IndexDeferrer{db: db, conn: conn, schema: schema, session: session, progress: progress, tables: map[string]*deferTable{}}
	switch v := RestoreIndexDeferBytes; {
	case v == DeferIndexOff:
		d.threshold = -1
	case v >= 0:
		d.threshold = v
	default:
		var pool int64
		if err := conn.QueryRowContext(ctx, "SELECT @@innodb_buffer_pool_size").Scan(&pool); err != nil {
			logger.Info("restore could not read innodb_buffer_pool_size; deferring indexes past a fixed size",
				"bytes", deferIndexUnknownBytes, "err", err.Error())
			d.threshold = deferIndexUnknownBytes
		} else {
			d.threshold = max(pool/4, deferIndexMinBytes)
		}
	}
	return d
}

// Insert counts an INSERT of n bytes into table, about to be written. When it
// takes the table past the threshold - or it is the table's first, and the
// table has FULLTEXT indexes - the indexes are dropped, after barrier: no row
// may be in flight and no transaction may hold the table. A table with nothing
// to drop is not held up for a barrier.
//
// A failure to read or drop an index leaves it in place: the restore is only
// slower for it.
func (d *IndexDeferrer) Insert(ctx context.Context, table string, n int, barrier func() error) error {
	if d.threshold < 0 || table == "" {
		return nil
	}
	t := d.tables[table]
	if t == nil {
		t = &deferTable{name: table}
		d.tables[table] = t
	}
	t.bytes += int64(n)
	fulltext := !t.fulltextChecked
	btree := !t.btreeChecked && t.bytes >= d.threshold
	if !fulltext && !btree {
		return nil
	}
	return d.drop(ctx, t, fulltext, btree, barrier)
}

func (d *IndexDeferrer) drop(ctx context.Context, t *deferTable, fulltext, btree bool, barrier func() error) error {
	t.fulltextChecked = t.fulltextChecked || fulltext
	t.btreeChecked = t.btreeChecked || btree

	// Reading the definition needs no barrier: it takes only a shared lock,
	// which the writers' inserts do not conflict with.
	create, err := mysqlShowCreateTable(ctx, d.conn, t.name)
	if err != nil {
		logger.Info("restore keeps the indexes of a table it cannot read", "table", t.name, "err", err.Error())
		return nil
	}
	if !strings.Contains(create, "ENGINE=InnoDB") {
		return nil
	}
	fks, err := d.foreignKeyColumns(ctx, t.name)
	if err != nil {
		logger.Info("restore keeps the indexes of a table whose foreign keys it cannot read", "table", t.name, "err", err.Error())
		return nil
	}

	var drops, names []string
	var addBtree, addFulltext []string
	for _, line := range strings.Split(create, "\n") {
		ix, ok := parseDeferrableIndex(line)
		if !ok || (ix.fulltext && !fulltext) || (!ix.fulltext && !btree) || ix.servesForeignKey(fks) {
			continue
		}
		drops = append(drops, "DROP INDEX "+QuoteMySQLIdent(ix.name))
		names = append(names, ix.name)
		if ix.fulltext {
			addFulltext = append(addFulltext, ix.def)
		} else {
			addBtree = append(addBtree, ix.def)
		}
	}
	if len(drops) == 0 {
		return nil
	}
	if err := barrier(); err != nil {
		return err
	}
	stmt := "ALTER TABLE " + QuoteMySQLIdent(t.name) + " " + strings.Join(drops, ", ")
	if _, err := d.conn.ExecContext(ctx, stmt); err != nil {
		logger.Info("restore keeps the indexes it could not drop", "table", t.name, "indexes", names, "err", err.Error())
		return nil
	}
	if len(t.btree) == 0 && len(t.fulltext) == 0 {
		d.pending = append(d.pending, t)
		d.deferred++
		d.progress.Phase(PhaseIndexesDeferred(d.deferred))
	}
	t.btree = append(t.btree, addBtree...)
	t.fulltext = append(t.fulltext, addFulltext...)
	logger.Info("restore defers indexes until the rows are in", "table", t.name, "indexes", names, "bytesSoFar", t.bytes)
	return nil
}

// Pending reports whether indexes are waiting to be rebuilt.
func (d *IndexDeferrer) Pending() bool { return len(d.pending) > 0 }

// Rebuild puts back every deferred index, the tables side by side over up to
// SetConnections (else RestoreWriters) connections of db, each table in one ALTER for its plain
// indexes and one for each FULLTEXT index - InnoDB builds those one at a time.
// working is told of each table as its rebuild starts.
//
// A table whose indexes have been rebuilt is not deferred again, so a restore
// that has to rebuild early - see Pending - loads the rest of its rows with
// them in place. An index that cannot be rebuilt fails the restore, with the
// statement that would put it back logged: a restore that reports success with
// an index missing would be worse than one that reports the failure.
func (d *IndexDeferrer) Rebuild(ctx context.Context) error {
	tables := d.pending
	d.pending = nil
	if len(tables) == 0 {
		return nil
	}
	for _, t := range tables {
		t.fulltextChecked, t.btreeChecked = true, true
	}
	logger.Info("restore rebuilding deferred indexes", "tables", len(tables))
	start := time.Now()

	st := &rebuildStatus{total: len(tables), running: map[string]*rebuildRun{}}
	report := func() {
		item, phase := st.describe()
		d.progress.Rebuilding(item, phase)
	}
	stopWatch := d.watchRebuild(ctx, st, report)

	var (
		mu    sync.Mutex
		errs  []error
		wg    sync.WaitGroup
		slots = make(chan struct{}, cmp.Or(d.connections, RestoreWriters))
	)
	report()
	for _, group := range d.relatedGroups(ctx, tables) {
		wg.Add(1)
		slots <- struct{}{}
		go func(group []*deferTable) {
			defer wg.Done()
			defer func() { <-slots }()
			for _, t := range group {
				st.started(t.name)
				report()
				err := d.rebuildTable(ctx, t, func(id int64) { st.connected(t.name, id) })
				st.finished(t.name)
				report()
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}(group)
	}
	wg.Wait()
	stopWatch()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	// Rebuilt early, the restore goes back to writing rows, every index in
	// place; rebuilt at the end, it is done.
	d.progress.Phase("")
	logger.Info("restore rebuilt deferred indexes", "tables", len(tables), "elapsed", time.Since(start).Round(time.Millisecond).String())
	return nil
}

// relatedGroups splits tables into groups that may be rebuilt side by side:
// two tables joined by a foreign key go in one group, rebuilt one after the
// other. An ALTER on a table takes a metadata lock on the tables its foreign
// keys join it to, so the ALTERs of a parent and its child, run at once, each
// wait for the other and one is killed as a deadlock (1213). If the keys
// cannot be read, every table goes in one group.
func (d *IndexDeferrer) relatedGroups(ctx context.Context, tables []*deferTable) [][]*deferTable {
	parent := map[string]string{}
	for _, t := range tables {
		parent[t.name] = t.name
	}
	var find func(string) string
	find = func(s string) string {
		if parent[s] != s {
			parent[s] = find(parent[s])
		}
		return parent[s]
	}

	rows, err := d.conn.QueryContext(ctx, `
		SELECT DISTINCT TABLE_NAME, REFERENCED_TABLE_NAME
		FROM   information_schema.KEY_COLUMN_USAGE
		WHERE  TABLE_SCHEMA = ? AND REFERENCED_TABLE_SCHEMA = ?`, d.schema, d.schema)
	if err != nil {
		logger.Info("restore rebuilds deferred indexes one table at a time; foreign keys unreadable", "err", err.Error())
		return [][]*deferTable{tables}
	}
	for rows.Next() {
		var child, ref string
		if err := rows.Scan(&child, &ref); err != nil {
			rows.Close()
			return [][]*deferTable{tables}
		}
		if _, ok := parent[child]; !ok {
			continue
		}
		if _, ok := parent[ref]; !ok {
			continue
		}
		parent[find(child)] = find(ref)
	}
	rows.Close()
	if rows.Err() != nil {
		return [][]*deferTable{tables}
	}

	var groups [][]*deferTable
	index := map[string]int{}
	for _, t := range tables {
		root := find(t.name)
		i, ok := index[root]
		if !ok {
			i = len(groups)
			index[root] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], t)
	}
	return groups
}

// rebuildSession is what a rebuild connection runs first. NO_AUTO_VALUE_ON_ZERO
// because an ALTER that copies the table - the first FULLTEXT index on one
// without its FTS_DOC_ID column does - renumbers a 0 in the AUTO_INCREMENT
// column without it, as an INSERT would.
var rebuildSession = []string{
	"SET FOREIGN_KEY_CHECKS=0",
	"SET SESSION sql_mode = IF(@@SESSION.sql_mode = '', 'NO_AUTO_VALUE_ON_ZERO', " +
		"CONCAT(@@SESSION.sql_mode, ',NO_AUTO_VALUE_ON_ZERO'))",
}

func (d *IndexDeferrer) rebuildTable(ctx context.Context, t *deferTable, connected func(id int64)) error {
	var stmts []string
	table := "ALTER TABLE " + QuoteMySQLIdent(t.name) + " "
	if len(t.btree) > 0 {
		stmts = append(stmts, table+"ADD "+strings.Join(t.btree, ", ADD "))
	}
	for _, def := range t.fulltext {
		stmts = append(stmts, table+"ADD "+def)
	}
	fail := func(err error, from int) error {
		logger.Error("restore could not rebuild the indexes it deferred; run the statements by hand",
			"table", t.name, "statements", strings.Join(stmts[from:], ";\n"), "err", err.Error())
		return fmt.Errorf("rebuild deferred indexes of %s: %w", t.name, err)
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fail(err, 0)
	}
	defer conn.Close()
	if err := execAll(ctx, conn, append(append([]string{}, rebuildSession...), d.session...)); err != nil {
		return fail(err, 0)
	}
	// The connection's id is what the server's progress of the ALTER is
	// found by; without it the table is reported by name alone.
	var id int64
	if conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id) == nil {
		connected(id)
	}
	began := time.Now()
	for i, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fail(err, i)
		}
	}
	logger.Info("restore rebuilt the indexes of a table", "table", t.name,
		"indexes", len(t.btree)+len(t.fulltext), "elapsed", time.Since(began).Round(time.Millisecond).String())
	t.btree, t.fulltext = nil, nil
	return nil
}

// foreignKeyColumns lists the column lists of table that a foreign key needs
// an index on: its own foreign keys' columns, and the columns other tables'
// foreign keys refer to.
func (d *IndexDeferrer) foreignKeyColumns(ctx context.Context, table string) ([][]string, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_NAME, 'child', COLUMN_NAME
		FROM   information_schema.KEY_COLUMN_USAGE
		WHERE  TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		UNION ALL
		SELECT CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_NAME, 'parent', REFERENCED_COLUMN_NAME
		FROM   information_schema.KEY_COLUMN_USAGE
		WHERE  REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?
		ORDER  BY 1, 2, 3, 4`, d.schema, table, d.schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// ORDER BY above keeps each key's columns together; within a key they come
	// in ORDINAL_POSITION order, which information_schema returns them in.
	var out [][]string
	var last string
	for rows.Next() {
		var schema, name, tbl, side, col string
		if err := rows.Scan(&schema, &name, &tbl, &side, &col); err != nil {
			return nil, err
		}
		key := schema + "." + name + "." + tbl + "." + side
		if key != last || len(out) == 0 {
			out = append(out, nil)
			last = key
		}
		out[len(out)-1] = append(out[len(out)-1], col)
	}
	return out, rows.Err()
}

// deferrableIndex is one index line of SHOW CREATE TABLE that the restore may
// drop and add back.
type deferrableIndex struct {
	name     string
	def      string // the line as printed, less its trailing comma: "KEY `n` (`a`) COMMENT 'x'"
	fulltext bool
	cols     []string
}

// parseDeferrableIndex reads one line of SHOW CREATE TABLE output, which prints
// every index on a line of its own. Only plain and FULLTEXT keys on columns
// qualify; a functional key part - "KEY `f` ((lower(`v`)))" - does not.
func parseDeferrableIndex(line string) (deferrableIndex, bool) {
	s := strings.TrimSuffix(strings.TrimSpace(line), ",")
	ix := deferrableIndex{def: s}
	switch {
	case strings.HasPrefix(s, "KEY `"):
		s = s[len("KEY "):]
	case strings.HasPrefix(s, "FULLTEXT KEY `"):
		s = s[len("FULLTEXT KEY "):]
		ix.fulltext = true
	default:
		return ix, false
	}
	name, rest, ok := cutBacktickIdent(s)
	if !ok {
		return ix, false
	}
	ix.name = name
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "(") {
		return ix, false
	}
	// The key parts: `a`, `b`(10), `c` DESC - each a quoted column with an
	// optional prefix length and direction, never a parenthesised expression.
	rest = rest[1:]
	for {
		col, after, ok := cutBacktickIdent(strings.TrimSpace(rest))
		if !ok {
			return ix, false
		}
		ix.cols = append(ix.cols, col)
		after = strings.TrimSpace(after)
		if strings.HasPrefix(after, "(") {
			end := strings.IndexByte(after, ')')
			if end < 0 {
				return ix, false
			}
			after = strings.TrimSpace(after[end+1:])
		}
		after = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(after, "DESC"), "ASC"))
		switch {
		case strings.HasPrefix(after, ","):
			rest = after[1:]
		case strings.HasPrefix(after, ")"):
			return ix, true
		default:
			return ix, false
		}
	}
}

// servesForeignKey reports whether a foreign key may need this index: its
// leading columns are the key's, in order.
func (ix deferrableIndex) servesForeignKey(fks [][]string) bool {
	for _, fk := range fks {
		if len(fk) == 0 || len(fk) > len(ix.cols) {
			continue
		}
		match := true
		for i, c := range fk {
			if !strings.EqualFold(ix.cols[i], c) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// cutBacktickIdent reads a `quoted` identifier off the front of s, a doubled
// backtick standing for one, and returns it with what follows.
func cutBacktickIdent(s string) (ident, rest string, ok bool) {
	if !strings.HasPrefix(s, "`") {
		return "", s, false
	}
	var sb strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '`' {
			sb.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '`' {
			sb.WriteByte('`')
			i++
			continue
		}
		return sb.String(), s[i+1:], true
	}
	return "", s, false
}

// QuoteMySQLIdent quotes an identifier for MySQL and MariaDB.
func QuoteMySQLIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func mysqlShowCreateTable(ctx context.Context, conn *sql.Conn, table string) (string, error) {
	var name, create string
	err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+QuoteMySQLIdent(table)).Scan(&name, &create)
	return create, err
}

// =============================================================================
// Progress of the rebuild
// =============================================================================

// RebuildProgressEvery is how often a restore asks the server how far the
// index builds it is running have got. A table's ALTER can take minutes, in
// which a count of tables finished does not move.
var RebuildProgressEvery = 5 * time.Second

// rebuildStatus is what Rebuild reports: the tables finished out of how many,
// and the ones being built, each with how far its ALTER has got when the
// server says.
type rebuildStatus struct {
	mu      sync.Mutex
	total   int
	done    int
	running map[string]*rebuildRun
	last    string // the table started last, reported as the item in flight
}

type rebuildRun struct {
	connID  int64
	percent float64 // < 0 while the server has not said
}

func (s *rebuildStatus) started(table string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[table] = &rebuildRun{percent: -1}
	s.last = table
}

func (s *rebuildStatus) connected(table string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.running[table]; r != nil {
		r.connID = id
	}
}

func (s *rebuildStatus) finished(table string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, table)
	s.done++
}

// connections returns the connection id each running table's ALTER is on.
func (s *rebuildStatus) connections() map[int64]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]string{}
	for table, r := range s.running {
		if r.connID > 0 {
			out[r.connID] = table
		}
	}
	return out
}

func (s *rebuildStatus) setPercent(table string, percent float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.running[table]; r != nil {
		r.percent = percent
	}
}

// describe renders the status as the item in flight and the restore's phase:
// "rebuilding indexes: 1/2 tables, orders 42%". A table whose progress the
// server has not reported is named without one.
func (s *rebuildStatus) describe() (item, phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.running))
	for table := range s.running {
		names = append(names, table)
	}
	sort.Strings(names)
	parts := []string{fmt.Sprintf("%s: %d/%d tables", PhaseRebuildingIndexes, s.done, s.total)}
	for _, table := range names {
		if p := s.running[table].percent; p >= 0 {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", table, p))
		} else {
			parts = append(parts, table)
		}
	}
	return s.last, strings.Join(parts, ", ")
}

// watchRebuild asks the server every RebuildProgressEvery how far the running
// ALTERs have got, and reports when it has asked. The returned stop waits for
// the watcher to exit, so no report lands after the rebuild's own last one.
func (d *IndexDeferrer) watchRebuild(ctx context.Context, st *rebuildStatus, report func()) (stop func()) {
	if RebuildProgressEvery <= 0 || d.alterProgress == alterProgressNone {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		tick := time.NewTicker(RebuildProgressEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			conns := st.connections()
			if len(conns) == 0 {
				continue
			}
			percents, ok := d.alterProgressPercent(ctx, conns)
			if !ok {
				return // this server does not say; the tables are reported by name
			}
			for id, p := range percents {
				st.setPercent(conns[id], p)
			}
			report()
		}
	}()
	return func() {
		cancel()
		<-exited
	}
}

// alterProgressSource is where a server reports an ALTER's progress.
type alterProgressSource int

const (
	alterProgressUnknown alterProgressSource = iota
	// alterProgressProcesslist: MariaDB's PROGRESS column, always there.
	alterProgressProcesslist
	// alterProgressStages: MySQL's performance_schema stage events, there
	// only when performance_schema is on and its stage events are collected.
	alterProgressStages
	alterProgressNone
)

// alterProgressPercent returns how far, in percent, the ALTER on each of the
// connections has got, for those the server reports on. ok is false once the
// server has shown it reports on none - the question is then not asked again.
//
// Progress is a courtesy: a query that fails here costs the operator a figure,
// never the restore.
func (d *IndexDeferrer) alterProgressPercent(ctx context.Context, conns map[int64]string) (map[int64]float64, bool) {
	ids := make([]any, 0, len(conns))
	for id := range conns {
		ids = append(ids, id)
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	if d.alterProgress == alterProgressUnknown || d.alterProgress == alterProgressProcesslist {
		out, err := d.queryPercents(ctx,
			"SELECT ID, PROGRESS FROM information_schema.PROCESSLIST WHERE ID IN ("+in+")", ids, false)
		if err == nil {
			d.alterProgress = alterProgressProcesslist
			return out, true
		}
		if d.alterProgress == alterProgressProcesslist {
			return nil, true // asked once already; a passing failure
		}
	}
	if d.alterProgress == alterProgressUnknown || d.alterProgress == alterProgressStages {
		out, err := d.queryPercents(ctx, `
			SELECT t.PROCESSLIST_ID, s.WORK_COMPLETED, s.WORK_ESTIMATED
			FROM   performance_schema.events_stages_current s
			JOIN   performance_schema.threads t ON t.THREAD_ID = s.THREAD_ID
			WHERE  t.PROCESSLIST_ID IN (`+in+`)`, ids, true)
		if err == nil {
			d.alterProgress = alterProgressStages
			return out, true
		}
		if d.alterProgress == alterProgressStages {
			return nil, true
		}
	}
	d.alterProgress = alterProgressNone
	return nil, false
}

// queryPercents runs a progress query keyed by connection id: one percentage
// column, or - stages - work done and work estimated.
func (d *IndexDeferrer) queryPercents(ctx context.Context, q string, args []any, stages bool) (map[int64]float64, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		if !stages {
			var p sql.NullFloat64
			if err := rows.Scan(&id, &p); err != nil {
				return nil, err
			}
			// MariaDB reports 0 for a statement that keeps no progress; a
			// figure of 0 would say the build has not begun.
			if p.Valid && p.Float64 > 0 {
				out[id] = p.Float64
			}
			continue
		}
		var done, estimated sql.NullInt64
		if err := rows.Scan(&id, &done, &estimated); err != nil {
			return nil, err
		}
		if done.Valid && estimated.Valid && estimated.Int64 > 0 {
			out[id] = float64(done.Int64) * 100 / float64(estimated.Int64)
		}
	}
	return out, rows.Err()
}
