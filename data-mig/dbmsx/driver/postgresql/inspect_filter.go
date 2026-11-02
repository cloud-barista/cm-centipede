package postgresql

// inspect_filter.go — the schema-object inventory as a filtered dump leaves it.
//
// Inspect lists every schema object of the schemas it covers. When loc.Filter is
// set, the lists are narrowed the way dump narrows what it writes, so an
// inventory of the source reads the same as an inventory of a target the
// filtered dump was restored into. A validation can then compare the two lists
// directly, rather than re-deriving which objects each rule took with it — a
// foreign key goes with an excluded table without any rule naming it, and the
// name-only inventory does not say which table a constraint belongs to.
//
// Each kind is enumerated by a query that also returns what the dump decides on
// — the object's schema and bare name, its table, a foreign key's columns and
// referenced side — and kept or dropped by the same predicates the dump uses:
//
//	foreign keys      listFKConstraints: named by a rule; on an excluded table or
//	                  an excluded column of it; referencing a schema outside the
//	                  inspection, an excluded table, or an excluded column of one
//	views             listViews: a table rule naming the view, or a view rule
//	materialized      the same, with the materialized-view kind
//	functions,
//	procedures        listFunctions: a rule naming name or schema.name (every
//	                  overload) or the identity signature the inventory shows (that
//	                  one routine) — see filterpkg.PgRoutineRuleMatches
//	triggers          listTriggers: on an excluded table, or named by a rule, per
//	                  table or per schema
//	rules             listRules: on an excluded table, or named by a rule
//	sequences, types  named by a rule
//	extensions        FilterObjects
//
// A sequence a serial column owns is not dropped with an excluded table: the dump
// creates every sequence in the schema whatever its table, so the target has it.
//
// The "name" column keeps the inventory's form — schema-qualified, quoted by the
// server, a routine with its identity signature — so a filtered list reads like
// an unfiltered one.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	base "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

// pgInventoryRow is one row of a filtered inventory query, keyed by column alias.
type pgInventoryRow = map[string]string

// pgFKColumnsExpr renders the comma-joined column names of a constraint's key
// (conkey or confkey on relation rel), in key order.
func pgFKColumnsExpr(key, rel string) string {
	return `(SELECT string_agg(a.attname, ',' ORDER BY k.ord)
	           FROM unnest(con.` + key + `) WITH ORDINALITY AS k(attnum, ord)
	           JOIN pg_attribute a ON a.attrelid = con.` + rel + ` AND a.attnum = k.attnum)`
}

func pgFilteredQueryForeignKeys(schemas []string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "con.conname") + ` AS name,
		con.conname AS obj, tn.nspname AS tbl_schema, tc.relname AS tbl,
		` + pgFKColumnsExpr("conkey", "conrelid") + ` AS cols,
		rn.nspname AS ref_schema, rc.relname AS ref_tbl,
		` + pgFKColumnsExpr("confkey", "confrelid") + ` AS ref_cols
	FROM pg_constraint con
	JOIN pg_namespace n  ON n.oid  = con.connamespace
	JOIN pg_class tc     ON tc.oid = con.conrelid
	JOIN pg_namespace tn ON tn.oid = tc.relnamespace
	JOIN pg_class rc     ON rc.oid = con.confrelid
	JOIN pg_namespace rn ON rn.oid = rc.relnamespace
	WHERE con.contype = 'f' AND n.nspname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQueryViews(schemas []string) string {
	return `SELECT ` + pgQualifiedName("table_schema", "table_name") + ` AS name,
		table_schema AS schema, table_name AS obj
	FROM information_schema.views
	WHERE table_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQueryMatViews(schemas []string) string {
	return `SELECT ` + pgQualifiedName("schemaname", "matviewname") + ` AS name,
		schemaname AS schema, matviewname AS obj
	FROM pg_matviews
	WHERE schemaname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQueryRoutines(schemas []string, prokind string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "p.proname") +
		` || '(' || pg_get_function_identity_arguments(p.oid) || ')' AS name,
		n.nspname AS schema, p.proname AS obj, pg_get_function_identity_arguments(p.oid) AS args
	FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) +
		` AND p.prokind = '` + prokind + `' ORDER BY 1`
}

// The inventory lists a trigger once, though information_schema has a row per
// event; DISTINCT over every column keeps that.
func pgFilteredQueryTriggers(schemas []string) string {
	return `SELECT DISTINCT ` + pgQualifiedName("trigger_schema", "trigger_name") + ` AS name,
		trigger_schema AS schema, trigger_name AS obj,
		event_object_schema AS tbl_schema, event_object_table AS tbl
	FROM information_schema.triggers
	WHERE trigger_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQueryRules(schemas []string) string {
	return `SELECT ` + pgQualifiedName("schemaname", "rulename") + ` AS name,
		schemaname AS schema, rulename AS obj, schemaname AS tbl_schema, tablename AS tbl
	FROM pg_rules
	WHERE schemaname IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQuerySequences(schemas []string) string {
	return `SELECT ` + pgQualifiedName("sequence_schema", "sequence_name") + ` AS name,
		sequence_schema AS schema, sequence_name AS obj
	FROM information_schema.sequences
	WHERE sequence_schema IN ` + pgSchemaLiteralList(schemas) + ` ORDER BY 1`
}

func pgFilteredQueryTypes(schemas []string) string {
	return `SELECT ` + pgQualifiedName("n.nspname", "t.typname") + ` AS name,
		n.nspname AS schema, t.typname AS obj
	FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
	WHERE n.nspname IN ` + pgSchemaLiteralList(schemas) +
		` AND (t.typtype IN ('e','d') OR (t.typtype = 'c' AND t.typrelid IN ` +
		`(SELECT oid FROM pg_class WHERE relkind = 'c'))) ORDER BY 1`
}

const pgFilteredQueryExtensions = `SELECT quote_ident(extname) AS name, extname AS obj
	FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY 1`

// pgKeepForeignKey mirrors listFKConstraints.
func pgKeepForeignKey(filter *filterpkg.DBMSFilterOption, inScope map[string]bool, r pgInventoryRow) bool {
	tbl := pgTableRef{Schema: r["tbl_schema"], Name: r["tbl"]}
	ref := pgTableRef{Schema: r["ref_schema"], Name: r["ref_tbl"]}
	switch {
	case pgObjectExcluded(filter, filterpkg.ObjectKindForeignKey, tbl.Schema, r["obj"]),
		pgTableExcluded(filter, tbl),
		pgAnyColExcluded(filter, tbl, r["cols"]),
		!inScope[ref.Schema],
		pgTableExcluded(filter, ref),
		pgAnyColExcluded(filter, ref, r["ref_cols"]):
		return false
	}
	return true
}

// pgKeepTableObject mirrors listViews and listMaterializedViews: the object is
// itself matched as a table, then as an object of its kind.
func pgKeepTableObject(filter *filterpkg.DBMSFilterOption, kind string, r pgInventoryRow) bool {
	return !pgTableExcluded(filter, pgTableRef{Schema: r["schema"], Name: r["obj"]}) &&
		!pgObjectExcluded(filter, kind, r["schema"], r["obj"])
}

// pgKeepNamedObject mirrors the kinds a rule removes only by naming them.
func pgKeepNamedObject(filter *filterpkg.DBMSFilterOption, kind string, r pgInventoryRow) bool {
	return !pgObjectExcluded(filter, kind, r["schema"], r["obj"])
}

// pgKeepRoutine mirrors listFunctions, which also takes a rule naming the
// routine by its signature.
func pgKeepRoutine(filter *filterpkg.DBMSFilterOption, kind string, r pgInventoryRow) bool {
	return !pgRoutineExcluded(filter, kind, r["schema"], r["obj"], r["args"])
}

// pgKeepTrigger mirrors listTriggers.
func pgKeepTrigger(filter *filterpkg.DBMSFilterOption, r pgInventoryRow) bool {
	tbl := pgTableRef{Schema: r["tbl_schema"], Name: r["tbl"]}
	return !pgTableExcluded(filter, tbl) &&
		!pgObjectExcludedInTable(filter, filterpkg.ObjectKindTrigger, tbl, r["obj"]) &&
		!pgObjectExcluded(filter, filterpkg.ObjectKindTrigger, tbl.Schema, r["obj"])
}

// pgKeepRule mirrors listRules.
func pgKeepRule(filter *filterpkg.DBMSFilterOption, r pgInventoryRow) bool {
	return !pgTableExcluded(filter, pgTableRef{Schema: r["tbl_schema"], Name: r["tbl"]}) &&
		!pgObjectExcluded(filter, filterpkg.ObjectKindRule, r["schema"], r["obj"])
}

// pgFilteredSchemaObjectFields is pgSchemaObjectFields for an inspection with a
// filter: the same kinds and destinations, each with its detail query and the
// predicate that keeps a row.
func pgFilteredSchemaObjectFields(m pgInspectMetric, info *base.DBMSInfo, schemas []string, filter *filterpkg.DBMSFilterOption) []pgSchemaObjectField {
	inScope := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		inScope[s] = true
	}
	var fields []pgSchemaObjectField
	add := func(want bool, query string, dst *[]string, keep func(pgInventoryRow) bool) {
		if want {
			fields = append(fields, pgSchemaObjectField{query: query, dst: dst, keep: keep})
		}
	}
	named := func(kind string) func(pgInventoryRow) bool {
		return func(r pgInventoryRow) bool { return pgKeepNamedObject(filter, kind, r) }
	}
	routine := func(kind string) func(pgInventoryRow) bool {
		return func(r pgInventoryRow) bool { return pgKeepRoutine(filter, kind, r) }
	}
	add(m.foreignKeys, pgFilteredQueryForeignKeys(schemas), &info.ForeignKeys,
		func(r pgInventoryRow) bool { return pgKeepForeignKey(filter, inScope, r) })
	add(m.views, pgFilteredQueryViews(schemas), &info.Views,
		func(r pgInventoryRow) bool { return pgKeepTableObject(filter, filterpkg.ObjectKindView, r) })
	add(m.materializedViews, pgFilteredQueryMatViews(schemas), &info.MaterializedViews,
		func(r pgInventoryRow) bool { return pgKeepTableObject(filter, filterpkg.ObjectKindMaterializedView, r) })
	add(m.functions, pgFilteredQueryRoutines(schemas, "f"), &info.Functions, routine(filterpkg.ObjectKindFunction))
	add(m.procedures, pgFilteredQueryRoutines(schemas, "p"), &info.Procedures, routine(filterpkg.ObjectKindProcedure))
	add(m.triggers, pgFilteredQueryTriggers(schemas), &info.Triggers,
		func(r pgInventoryRow) bool { return pgKeepTrigger(filter, r) })
	add(m.sequences, pgFilteredQuerySequences(schemas), &info.Sequences, named(filterpkg.ObjectKindSequence))
	add(m.types, pgFilteredQueryTypes(schemas), &info.Types, named(filterpkg.ObjectKindType))
	add(m.extensions, pgFilteredQueryExtensions, &info.Extensions, func(r pgInventoryRow) bool {
		return !filter.IsObjectExcluded(filterpkg.ObjectKindExtension, r["obj"])
	})
	add(m.rules, pgFilteredQueryRules(schemas), &info.Rules,
		func(r pgInventoryRow) bool { return pgKeepRule(filter, r) })
	return fields
}

// pgKeptNames returns the "name" of every row keep accepts, in row order.
func pgKeptNames(rows []pgInventoryRow, keep func(pgInventoryRow) bool) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if keep(r) {
			names = append(names, r["name"])
		}
	}
	return names
}

// queryRowMaps runs a query and returns each row keyed by column name, every
// value as text (NULL as ""). It is the direct path's counterpart of what
// pgQueryJSONViaSSH returns over SSH.
func (d *PostgreSQLDriver) queryRowMaps(ctx context.Context, conn *pgx.Conn, query string) ([]pgInventoryRow, error) {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fieldDescs := rows.FieldDescriptions()
	var out []pgInventoryRow
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(pgInventoryRow, len(fieldDescs))
		for i, fd := range fieldDescs {
			if vals[i] != nil {
				row[fd.Name] = fmt.Sprint(vals[i])
			} else {
				row[fd.Name] = ""
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
