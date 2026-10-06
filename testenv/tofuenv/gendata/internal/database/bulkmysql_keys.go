package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// mysqlKeys is what one table gives up for the load: its foreign keys and its
// plain secondary indexes, each with the clause that puts it back.
type mysqlKeys struct {
	table   string
	fks     []mysqlKeyDef
	indexes []mysqlKeyDef
}

type mysqlKeyDef struct {
	name string
	def  string // the ADD clause body, e.g. "INDEX `idx` (`col`)"
}

// dropMySQLKeys drops the foreign keys and the plain secondary indexes of the
// tables being filled, and returns a function that puts them back.
//
// The indexes are the point. Maintaining an index on a random column costs a
// random page per row once it outgrows the buffer pool; the change buffer
// softens that, but it does not survive tens of millions of rows. Building the
// index afterwards is one sorted pass.
//
// The foreign keys have to go with them: InnoDB needs an index on a foreign
// key's columns, and refuses to drop the one a constraint relies on - which here
// is most of them. They come back after the indexes, with foreign_key_checks off
// on conn, so adding them is a metadata change rather than a check of every row;
// the generator draws every parent id from a range that exists, so there is
// nothing to check.
//
// Primary keys stay (their ids are generated in order), and so do UNIQUE
// indexes, which unique_checks=0 already makes cheap. An index this cannot
// rebuild exactly - FULLTEXT, SPATIAL, functional - is left in place.
//
// Everything is best effort: a key that cannot be listed or dropped stays, and
// the load is only slower for it.
func dropMySQLKeys(ctx context.Context, db *sql.DB, conn *sql.Conn, engine Engine, schema string, tables []string) func() {
	if len(tables) == 0 {
		return func() {}
	}
	keys, err := listMySQLKeys(ctx, conn, schema, tables)
	if err != nil {
		log.Printf("[warn] could not list indexes and foreign keys (continuing, they stay in place): %v", err)
		return func() {}
	}

	var dropped []mysqlKeys
	var idxNames, fkNames []string
	for _, k := range keys {
		got := mysqlKeys{table: k.table}
		if len(k.fks) > 0 {
			if _, err := conn.ExecContext(ctx, alterMySQL(k.table, "DROP FOREIGN KEY", k.fks)); err != nil {
				// Without the foreign keys gone, their indexes cannot go either.
				log.Printf("[warn] could not drop the foreign keys of %s (continuing, the load will be slower): %v", k.table, err)
				continue
			}
			got.fks = k.fks
			for _, d := range k.fks {
				fkNames = append(fkNames, d.name)
			}
		}
		if len(k.indexes) > 0 {
			if _, err := conn.ExecContext(ctx, alterMySQL(k.table, "DROP INDEX", k.indexes)); err != nil {
				log.Printf("[warn] could not drop the indexes of %s (continuing, the load will be slower): %v", k.table, err)
			} else {
				got.indexes = k.indexes
				for _, d := range k.indexes {
					idxNames = append(idxNames, d.name)
				}
			}
		}
		if len(got.fks) > 0 || len(got.indexes) > 0 {
			dropped = append(dropped, got)
		}
	}
	if len(dropped) == 0 {
		return func() {}
	}
	log.Printf("database: %d %s and %d %s dropped for the bulk load, rebuilt once the test data is in (%s)",
		len(idxNames), plural("index", len(idxNames)), len(fkNames), plural("foreign key", len(fkNames)),
		strings.Join(append(idxNames, fkNames...), ", "))

	return func() {
		recreateMySQLKeys(ctx, conn, dropped, newMySQLDDLWatcher(ctx, db, conn, engine))
	}
}

// recreateMySQLKeys puts each table's keys back: its indexes in one ALTER - one
// pass over the table for all of them - then its foreign keys in another.
func recreateMySQLKeys(ctx context.Context, conn *sql.Conn, dropped []mysqlKeys, watch *ddlWatcher) {
	log.Printf("database: rebuilding indexes and foreign keys on %d %s", len(dropped), plural("table", len(dropped)))
	start := time.Now()
	failed := 0
	for i, k := range dropped {
		label := fmt.Sprintf("  [%d/%d] %s", i+1, len(dropped), k.table)
		log.Printf("%s (%d %s, %d %s) ...", label,
			len(k.indexes), plural("index", len(k.indexes)), len(k.fks), plural("foreign key", len(k.fks)))
		began := time.Now()
		ok := true
		// Indexes first: each foreign key needs an index on its columns, and
		// without one InnoDB would quietly create a second index of its own.
		for _, step := range []struct {
			verb string
			defs []mysqlKeyDef
		}{{"ADD", k.indexes}, {"ADD CONSTRAINT", k.fks}} {
			if len(step.defs) == 0 {
				continue
			}
			stmt := alterMySQLAdd(k.table, step.verb, step.defs)
			stop := watch.start(label)
			_, err := conn.ExecContext(ctx, stmt)
			stop()
			if err != nil {
				ok = false
				// Loud, because the database is now missing keys the fixture
				// created and nothing later in the run would notice.
				log.Printf("[ERROR] %s: keys dropped for the bulk load could NOT be recreated: %v\n"+
					"  recreate them by hand, or reload the fixture:\n  %s;", k.table, err, stmt)
			}
		}
		if !ok {
			failed++
			continue
		}
		log.Printf("%s done in %s", label, time.Since(began).Round(time.Second))
	}
	log.Printf("database: keys rebuilt on %d %s in %s",
		len(dropped)-failed, plural("table", len(dropped)-failed), time.Since(start).Round(time.Second))
}

// alterMySQL is "ALTER TABLE t DROP INDEX a, DROP INDEX b".
func alterMySQL(table, verb string, defs []mysqlKeyDef) string {
	parts := make([]string, len(defs))
	for i, d := range defs {
		parts[i] = verb + " " + quoteMySQL(d.name)
	}
	return "ALTER TABLE " + quoteMySQL(table) + " " + strings.Join(parts, ", ")
}

// alterMySQLAdd is "ALTER TABLE t ADD INDEX `a` (...), ADD INDEX `b` (...)", or
// the same with ADD CONSTRAINT for foreign keys.
func alterMySQLAdd(table, verb string, defs []mysqlKeyDef) string {
	parts := make([]string, len(defs))
	for i, d := range defs {
		parts[i] = verb + " " + d.def
	}
	return "ALTER TABLE " + quoteMySQL(table) + " " + strings.Join(parts, ", ")
}

// listMySQLKeys reads the foreign keys and the rebuildable plain secondary
// indexes of tables, in table order.
func listMySQLKeys(ctx context.Context, conn *sql.Conn, schema string, tables []string) ([]mysqlKeys, error) {
	in := strings.TrimSuffix(strings.Repeat("?,", len(tables)), ",")
	args := []any{schema}
	for _, t := range tables {
		args = append(args, t)
	}
	byTable := map[string]*mysqlKeys{}
	get := func(t string) *mysqlKeys {
		if byTable[t] == nil {
			byTable[t] = &mysqlKeys{table: t}
		}
		return byTable[t]
	}

	// Indexes, one row per column. COLUMN_NAME is NULL for a functional key
	// part, which marks the whole index as one to leave alone.
	rows, err := conn.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME, SUB_PART, COLLATION, INDEX_TYPE
		FROM   information_schema.STATISTICS
		WHERE  TABLE_SCHEMA = ? AND TABLE_NAME IN (`+in+`)
		  AND  NON_UNIQUE = 1 AND INDEX_NAME <> 'PRIMARY'
		ORDER  BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args...)
	if err != nil {
		return nil, fmt.Errorf("list indexes: %w", err)
	}
	type idxCols struct {
		table, name string
		cols        []string
		skip        bool
	}
	var order []*idxCols
	seen := map[string]*idxCols{}
	for rows.Next() {
		var table, name, typ string
		var col, collation sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&table, &name, &col, &sub, &collation, &typ); err != nil {
			rows.Close()
			return nil, err
		}
		key := table + "." + name
		ic := seen[key]
		if ic == nil {
			ic = &idxCols{table: table, name: name}
			seen[key] = ic
			order = append(order, ic)
		}
		if !col.Valid || !strings.EqualFold(typ, "BTREE") {
			ic.skip = true
			continue
		}
		c := quoteMySQL(col.String)
		if sub.Valid {
			c += fmt.Sprintf("(%d)", sub.Int64)
		}
		if collation.String == "D" {
			c += " DESC"
		}
		ic.cols = append(ic.cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, ic := range order {
		if ic.skip {
			continue
		}
		k := get(ic.table)
		k.indexes = append(k.indexes, mysqlKeyDef{
			name: ic.name,
			def:  "INDEX " + quoteMySQL(ic.name) + " (" + strings.Join(ic.cols, ", ") + ")",
		})
	}

	// Foreign keys, one row per column pair.
	rows, err = conn.QueryContext(ctx, `
		SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
		       k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		       r.UPDATE_RULE, r.DELETE_RULE
		FROM   information_schema.KEY_COLUMN_USAGE k
		JOIN   information_schema.REFERENTIAL_CONSTRAINTS r
		       ON  r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		       AND r.CONSTRAINT_NAME   = k.CONSTRAINT_NAME
		       AND r.TABLE_NAME        = k.TABLE_NAME
		WHERE  k.TABLE_SCHEMA = ? AND k.TABLE_NAME IN (`+in+`)
		  AND  k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER  BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, args...)
	if err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	type fkCols struct {
		table, name, refSchema, refTable, onUpdate, onDelete string
		cols, refCols                                        []string
	}
	var fkOrder []*fkCols
	fkSeen := map[string]*fkCols{}
	for rows.Next() {
		var table, name, col, refSchema, refTable, refCol, onUpdate, onDelete string
		if err := rows.Scan(&table, &name, &col, &refSchema, &refTable, &refCol, &onUpdate, &onDelete); err != nil {
			rows.Close()
			return nil, err
		}
		key := table + "." + name
		fc := fkSeen[key]
		if fc == nil {
			fc = &fkCols{table: table, name: name, refSchema: refSchema, refTable: refTable,
				onUpdate: onUpdate, onDelete: onDelete}
			fkSeen[key] = fc
			fkOrder = append(fkOrder, fc)
		}
		fc.cols = append(fc.cols, quoteMySQL(col))
		fc.refCols = append(fc.refCols, quoteMySQL(refCol))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, fc := range fkOrder {
		ref := quoteMySQL(fc.refTable)
		if !strings.EqualFold(fc.refSchema, schema) {
			ref = quoteMySQL(fc.refSchema) + "." + ref
		}
		k := get(fc.table)
		k.fks = append(k.fks, mysqlKeyDef{
			name: fc.name,
			def: fmt.Sprintf("%s FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s ON UPDATE %s",
				quoteMySQL(fc.name), strings.Join(fc.cols, ", "), ref, strings.Join(fc.refCols, ", "),
				fc.onDelete, fc.onUpdate),
		})
	}

	var out []mysqlKeys
	for _, t := range tables {
		if k := byTable[t]; k != nil {
			out = append(out, *k)
		}
	}
	return out, nil
}

// newMySQLDDLWatcher returns a watcher for the ALTERs conn runs, or nil when the
// session cannot be identified.
//
// MariaDB reports an ALTER's progress in information_schema.PROCESSLIST
// (STAGE / MAX_STAGE / PROGRESS) out of the box. MySQL only has it in
// performance_schema, and only when that is on with the stage consumers enabled,
// which a small RDS instance may not have; then the poll finds nothing and the
// rebuild reports per table alone.
func newMySQLDDLWatcher(ctx context.Context, db *sql.DB, conn *sql.Conn, engine Engine) *ddlWatcher {
	var id int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		log.Printf("[warn] could not identify the load session, key rebuilds will not report progress: %v", err)
		return nil
	}
	if engine == MariaDB {
		return &ddlWatcher{ctx: ctx, poll: func(ctx context.Context) (string, bool) {
			var stage, maxStage int
			var pct float64
			err := db.QueryRowContext(ctx, `
				SELECT STAGE, MAX_STAGE, PROGRESS FROM information_schema.PROCESSLIST WHERE ID = ?`, id).
				Scan(&stage, &maxStage, &pct)
			if err != nil || maxStage == 0 {
				return "", false
			}
			return fmt.Sprintf("stage %d/%d %.0f%%", stage, maxStage, pct), true
		}}
	}
	return &ddlWatcher{ctx: ctx, poll: func(ctx context.Context) (string, bool) {
		var event string
		var done, estimated sql.NullInt64
		err := db.QueryRowContext(ctx, `
			SELECT s.EVENT_NAME, s.WORK_COMPLETED, s.WORK_ESTIMATED
			FROM   performance_schema.events_stages_current s
			JOIN   performance_schema.threads t ON t.THREAD_ID = s.THREAD_ID
			WHERE  t.PROCESSLIST_ID = ?`, id).Scan(&event, &done, &estimated)
		if err != nil {
			return "", false
		}
		// "stage/innodb/alter table (read PK and internal sort)" -> the part in
		// parentheses says what it is doing.
		phase := event
		if i := strings.Index(event, "("); i >= 0 {
			phase = strings.TrimSuffix(event[i+1:], ")")
		}
		if estimated.Valid && estimated.Int64 > 0 {
			return fmt.Sprintf("%s %.0f%%", phase, float64(done.Int64)*100/float64(estimated.Int64)), true
		}
		return phase, true
	}}
}
