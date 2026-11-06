package mysql

// inspect_filter.go — the schema-object inventory as a filtered dump leaves it.
//
// Inspect lists every schema object of a database. When loc.Filter is set, the
// lists are narrowed the way dump narrows what it writes, so an inventory of the
// source reads the same as an inventory of a target the filtered dump was
// restored into. A validation can then compare the two lists directly instead of
// re-deriving, rule by rule, which object each rule took with it — a foreign key
// goes with an excluded table without any rule naming it, and the bare inventory
// does not say which table a constraint belongs to.
//
// The narrowing mirrors the dump, decision for decision:
//
//	foreign keys  named by an object_exclude rule; on an excluded table (the
//	              table is not dumped); or referencing an excluded table or an
//	              excluded column of one (mysqlFKRefDropped)
//	views         FilterTables (a table rule naming a view), then FilterObjects
//	triggers      on an excluded table (listTriggers), then FilterObjects
//	routines,
//	events        FilterObjects
//
// The table list is left whole: which tables a filter drops is the filter's
// own statement, and a caller reads it from there.
//
// With no filter Inspect does not come here, and every list is the full one.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	base "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

// mysqlForeignKeyDetailQuery lists each foreign key with the table it is on, the
// table it references and the referenced columns (comma-joined, in key order) —
// what mysqlKeepForeignKey decides on. The caller appends the schema: a ?
// placeholder on the direct path, a quoted literal over SSH.
const mysqlForeignKeyDetailQuery = `SELECT rc.CONSTRAINT_NAME, rc.TABLE_NAME, rc.REFERENCED_TABLE_NAME,
       GROUP_CONCAT(kcu.REFERENCED_COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION SEPARATOR ',')
  FROM information_schema.REFERENTIAL_CONSTRAINTS rc
  JOIN information_schema.KEY_COLUMN_USAGE kcu
    ON kcu.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
   AND kcu.CONSTRAINT_NAME   = rc.CONSTRAINT_NAME
   AND kcu.TABLE_NAME        = rc.TABLE_NAME
 WHERE rc.CONSTRAINT_SCHEMA = `

const mysqlForeignKeyDetailGroup = `
 GROUP BY rc.CONSTRAINT_NAME, rc.TABLE_NAME, rc.REFERENCED_TABLE_NAME
 ORDER BY rc.CONSTRAINT_NAME`

// mysqlKeepForeignKey reports whether a filtered dump still writes the foreign
// key name, on table, referencing refTable(refCols).
func mysqlKeepForeignKey(filter *filterpkg.DBMSFilterOption, name, table, refTable, refCols string) bool {
	if filter.IsObjectExcluded(filterpkg.ObjectKindForeignKey, name) || filter.IsTableExcluded(table) {
		return false
	}
	if filter.IsTableExcluded(refTable) {
		return false
	}
	dropped := filter.ExcludedColumns(refTable)
	for _, c := range strings.Split(refCols, ",") {
		if dropped[strings.TrimSpace(c)] {
			return false
		}
	}
	return true
}

// mysqlKeepForeignKeyRows applies mysqlKeepForeignKey to rows of
// mysqlForeignKeyDetailQuery (name, table, referenced table, referenced
// columns) and returns the names kept.
func mysqlKeepForeignKeyRows(filter *filterpkg.DBMSFilterOption, rows [][]string) []string {
	var names []string
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		name := strings.TrimSpace(r[0])
		if mysqlKeepForeignKey(filter, name, strings.TrimSpace(r[1]), strings.TrimSpace(r[2]), strings.TrimSpace(r[3])) {
			names = append(names, name)
		}
	}
	return names
}

// mysqlKeepTriggerRows keeps the triggers (rows of name, table) a filtered dump
// writes: not on an excluded table, not named by an object_exclude rule.
func mysqlKeepTriggerRows(filter *filterpkg.DBMSFilterOption, rows [][]string) []string {
	var names []string
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		name, table := strings.TrimSpace(r[0]), strings.TrimSpace(r[1])
		if filter.IsTableExcluded(table) || filter.IsObjectExcluded(filterpkg.ObjectKindTrigger, name) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// listForeignKeysFiltered is listForeignKeys narrowed by filter.
func (d *MySQLDriver) listForeignKeysFiltered(ctx context.Context, db *sql.DB, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := db.QueryContext(ctx, mysqlForeignKeyDetailQuery+"?"+mysqlForeignKeyDetailGroup, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var detail [][]string
	for rows.Next() {
		var name, table, refTable string
		var refCols sql.NullString
		if err := rows.Scan(&name, &table, &refTable, &refCols); err != nil {
			return nil, err
		}
		detail = append(detail, []string{name, table, refTable, refCols.String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return mysqlKeepForeignKeyRows(filter, detail), nil
}

// listForeignKeysFilteredViaSSH is listForeignKeysFiltered over SSH.
func (d *MySQLDriver) listForeignKeysFilteredViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := d.mysqlQueryViaSSH(exec, cfg, "",
		mysqlForeignKeyDetailQuery+"'"+mysqlEscape(database)+"'"+mysqlForeignKeyDetailGroup)
	if err != nil {
		return nil, err
	}
	return mysqlKeepForeignKeyRows(filter, rows), nil
}

// listTriggersFilteredViaSSH lists trigger names with their tables over SSH and
// keeps the ones a filtered dump writes.
func (d *MySQLDriver) listTriggersFilteredViaSSH(exec *base.SSHExec, cfg *base.SSHTunnelConfig, database string, filter *filterpkg.DBMSFilterOption) ([]string, error) {
	rows, err := d.mysqlQueryViaSSH(exec, cfg, "", fmt.Sprintf(
		`SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s' ORDER BY TRIGGER_NAME`,
		mysqlEscape(database)))
	if err != nil {
		return nil, err
	}
	return mysqlKeepTriggerRows(filter, rows), nil
}

// mysqlFilterSchemaObjects narrows the name-only lists — views, routines,
// triggers, events — by object_exclude rules, and views by table rules too.
// Foreign keys and the trigger tables are decided before this, where the table
// a name belongs to is still known.
func mysqlFilterSchemaObjects(filter *filterpkg.DBMSFilterOption, info *base.DBMSInfo) {
	info.Views = filter.FilterObjects(filterpkg.ObjectKindView, filter.FilterTables(info.Views))
	info.Functions = filter.FilterObjects(filterpkg.ObjectKindFunction, info.Functions)
	info.Procedures = filter.FilterObjects(filterpkg.ObjectKindProcedure, info.Procedures)
	info.Triggers = filter.FilterObjects(filterpkg.ObjectKindTrigger, info.Triggers)
	info.Events = filter.FilterObjects(filterpkg.ObjectKindEvent, info.Events)
}
