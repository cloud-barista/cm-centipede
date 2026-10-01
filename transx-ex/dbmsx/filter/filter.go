// Package filter provides an extensible, exclude-only filter pipeline for dbmsx.
//
// A migration transfers the source database in full; the pipeline only removes
// things. DBMSFilterOption carries an ordered list of Rules, each wrapping a
// typed Matcher that decides what to exclude. Built-in matchers register
// themselves from init (see table_col_exclude.go, row_exclude.go and
// object_exclude.go); further matchers can be added with RegisterFilter
// without touching this file.
//
// Matchers declare which DBMS engines they apply to via AppliesTo, so the same
// pipeline definition can be validated against the source engine.
package filter

import (
	"encoding/json"
	"fmt"
)

// =============================================================================
// Matcher + registry
// =============================================================================

// FilterMatcher is a pluggable exclusion predicate identified by a "type" key.
// Every matcher expresses an EXCLUSION: the source database is migrated in full
// except for what the matcher removes.
type FilterMatcher interface {
	// Type returns the registry key used to (de)serialise the matcher.
	Type() string
	// AppliesTo reports whether the matcher is valid for the given DBMS engine
	// (e.g. "mysql", "postgresql"). Engine-agnostic matchers return true for all.
	AppliesTo(dbmsType string) bool
}

// factory builds a Matcher from the raw JSON of a rule object. The raw message
// contains the whole rule (type + matcher-specific fields).
type factory func(raw json.RawMessage) (FilterMatcher, error)

// registration is a registry entry: the factory plus the set of engines the
// matcher type is valid for (nil => all engines).
type registration struct {
	engines map[string]bool
	factory factory
}

// registry maps a matcher "type" to its registration. Built-in matchers
// register in their own init(); callers add custom types with RegisterFilter.
var registry = map[string]registration{}

// RegisterFilter makes a matcher type available to the JSON decoder, scoped to
// the given engines (empty/nil => valid for all engines). It is intended to be
// called from init() and is not safe for concurrent use with decoding.
func RegisterFilter(typ string, engines []string, f factory) {
	reg := registration{factory: f}
	if len(engines) > 0 {
		reg.engines = make(map[string]bool, len(engines))
		for _, e := range engines {
			reg.engines[e] = true
		}
	}
	registry[typ] = reg
}

// =============================================================================
// Rule
// =============================================================================

// FilterRule is one entry of the exclusion pipeline: a typed matcher. The
// original JSON is retained so the rule round-trips through json.Marshal
// unchanged: FilterMatcher is an interface, so the "type" discriminator lives
// only in the JSON and would be lost by a plain marshal.
type FilterRule struct {
	Matcher FilterMatcher

	raw json.RawMessage
}

// UnmarshalJSON decodes {"type",...} by dispatching on "type" to the registered
// factory.
func (r *FilterRule) UnmarshalJSON(data []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if head.Type == "" {
		return fmt.Errorf("filter: rule is missing \"type\"")
	}
	reg, ok := registry[head.Type]
	if !ok {
		return fmt.Errorf("filter: unknown filter type %q", head.Type)
	}
	m, err := reg.factory(data)
	if err != nil {
		return fmt.Errorf("filter: %q: %w", head.Type, err)
	}
	r.Matcher = m
	r.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON re-emits the original JSON when available (config-loaded rules),
// otherwise reconstructs {"type",...matcher fields} for rules built in code.
func (r FilterRule) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return r.raw, nil
	}
	if r.Matcher == nil {
		return nil, fmt.Errorf("filter: rule has no matcher")
	}
	mb, err := json.Marshal(r.Matcher)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(mb, &fields); err != nil {
		return nil, err
	}
	fields["type"], _ = json.Marshal(r.Matcher.Type())
	return json.Marshal(fields)
}

// =============================================================================
// Option
// =============================================================================

// DBMSFilterOption is an ordered exclusion pipeline. The source database is
// migrated in full except for the tables/columns/rows removed by Rules.
type DBMSFilterOption struct {
	Rules []FilterRule `json:"rules,omitempty"`
}

// tableColumnRule is the internal contract implemented by matchers that exclude
// whole tables (empty columns) or specific columns within a table. Resolution
// helpers below aggregate across all rules implementing it.
type tableColumnRule interface {
	table() string
	columns() []string // empty => whole-table exclusion
}

// rowExcludeRule is the internal contract implemented by matchers that drop rows
// matching a predicate. Exactly one predicate form is populated per rule:
// match() for the relational engines, whose driver negates and combines it into
// a keep-clause using its own SQL dialect (NULL handling differs across
// engines), and query() for MongoDB, whose driver combines the documents under
// $nor.
type rowExcludeRule interface {
	table() string
	match() string
	query() json.RawMessage
}

// objectExcludeRule is the internal contract implemented by matchers that drop a
// single named schema object (view, function, trigger, index, …) of a given
// kind. table() is an optional qualifier used only by index rules (empty for
// schema-global kinds).
type objectExcludeRule interface {
	kind() string
	name() string
	table() string
}

// Validate checks every rule against the target engine and access mode.
// directAccess must be true only for a direct TCP connection: column exclusion
// is impossible over ssh-tunnel because native CLI dumps cannot drop columns.
func (o *DBMSFilterOption) Validate(dbmsType string, directAccess bool) error {
	if o == nil {
		return nil
	}
	for i, r := range o.Rules {
		if r.Matcher == nil {
			return fmt.Errorf("filter: rule %d has no matcher", i)
		}
		if !r.Matcher.AppliesTo(dbmsType) {
			return fmt.Errorf("filter: %q is not supported for engine %q", r.Matcher.Type(), dbmsType)
		}
		if tc, ok := r.Matcher.(tableColumnRule); ok {
			if tc.table() == "" {
				return fmt.Errorf("filter: rule %d: table is required", i)
			}
			if len(tc.columns()) > 0 && !directAccess {
				return fmt.Errorf("filter: column exclusion (table %q) requires direct access, not ssh-tunnel", tc.table())
			}
		}
		if rf, ok := r.Matcher.(rowExcludeRule); ok {
			if rf.table() == "" {
				return fmt.Errorf("filter: rule %d: table is required", i)
			}
			// A rule built in code bypasses the parser, so the query shape is
			// re-checked here rather than assumed well-formed.
			hasQuery := isJSONObject(rf.query())
			if !hasQuery && jsonPresent(rf.query()) {
				return fmt.Errorf("filter: row exclusion (table %q): query must be a JSON object", rf.table())
			}
			// The predicate form has to match the engine: a raw SQL string
			// cannot be handed to MongoDB, and a query document cannot be
			// spliced into a SELECT.
			switch {
			case rowExcludeSQLEngine(dbmsType) && hasQuery:
				return fmt.Errorf("filter: row exclusion (table %q): query is for mongodb; engine %q takes a SQL match", rf.table(), dbmsType)
			case rowExcludeDocEngine(dbmsType) && rf.match() != "":
				return fmt.Errorf("filter: row exclusion (collection %q): match is a SQL predicate; engine %q takes a query document", rf.table(), dbmsType)
			case rf.match() == "" && !hasQuery:
				return fmt.Errorf("filter: rule %d: match or query is required", i)
			}
			// Row filtering rewrites the per-table dump read, which only the
			// direct-mode data dump performs. Native CLI dumps cannot express
			// per-table row predicates in a single invocation — mysqldump's
			// --where applies to every table, pg_dump has no row option, and
			// mongodump's --query needs a single --collection — so row exclusion
			// is rejected over ssh-tunnel, mirroring column exclusion above.
			if !directAccess {
				return fmt.Errorf("filter: row exclusion (table %q) requires direct access, not ssh-tunnel", rf.table())
			}
		}
		if oe, ok := r.Matcher.(objectExcludeRule); ok {
			if oe.kind() == "" {
				return fmt.Errorf("filter: rule %d: kind is required", i)
			}
			if oe.name() == "" {
				return fmt.Errorf("filter: rule %d: name is required", i)
			}
			// Per-name object exclusion works only in direct mode: native CLI
			// dumps emit routines/triggers/events per category as all-or-nothing
			// (mysqldump --routines/--triggers/--events), so a single named object
			// cannot be dropped over ssh-tunnel — mirroring column/row exclusion.
			if !directAccess {
				return fmt.Errorf("filter: object exclusion (%s %q) requires direct access, not ssh-tunnel", oe.kind(), oe.name())
			}
		}
	}
	return nil
}

// =============================================================================
// Resolution helpers — consulted by the drivers
// =============================================================================

// IsTableExcluded reports whether table is excluded in full (a table_column
// rule naming it with no columns).
func (o *DBMSFilterOption) IsTableExcluded(table string) bool {
	if o == nil {
		return false
	}
	for _, r := range o.Rules {
		if tc, ok := r.Matcher.(tableColumnRule); ok {
			if tc.table() == table && len(tc.columns()) == 0 {
				return true
			}
		}
	}
	return false
}

// FilterTables returns names with wholly-excluded tables removed, preserving
// order. A nil option passes everything.
func (o *DBMSFilterOption) FilterTables(names []string) []string {
	if o == nil {
		return names
	}
	result := names[:0:0]
	for _, name := range names {
		if o.IsTableExcluded(name) {
			continue
		}
		result = append(result, name)
	}
	return result
}

// ExcludedColumns returns the set of columns to drop for table — the union of
// all column-level rules naming it. A whole-table exclusion contributes nothing
// here (the table itself is dropped via FilterTables/IsTableExcluded).
func (o *DBMSFilterOption) ExcludedColumns(table string) map[string]bool {
	res := map[string]bool{}
	if o == nil {
		return res
	}
	for _, r := range o.Rules {
		if tc, ok := r.Matcher.(tableColumnRule); ok && tc.table() == table {
			for _, c := range tc.columns() {
				res[c] = true
			}
		}
	}
	return res
}

// HasColumnExclusion reports whether table has any column-level exclusion rule.
func (o *DBMSFilterOption) HasColumnExclusion(table string) bool {
	return len(o.ExcludedColumns(table)) > 0
}

// KeepColumns returns cols with excluded columns removed, preserving order.
func (o *DBMSFilterOption) KeepColumns(table string, cols []string) []string {
	if o == nil {
		return cols
	}
	excluded := o.ExcludedColumns(table)
	if len(excluded) == 0 {
		return cols
	}
	kept := cols[:0:0]
	for _, c := range cols {
		if excluded[c] {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// ExcludedTableNames returns the names of all wholly-excluded tables, in rule
// order (deduplicated). Used to build native CLI --ignore/-T flags.
func (o *DBMSFilterOption) ExcludedTableNames() []string {
	if o == nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, r := range o.Rules {
		if tc, ok := r.Matcher.(tableColumnRule); ok && len(tc.columns()) == 0 {
			if t := tc.table(); t != "" && !seen[t] {
				seen[t] = true
				names = append(names, t)
			}
		}
	}
	return names
}

// RowExcludeMatches returns the SQL predicates of all row_exclude rules naming
// table, in rule order. Each predicate describes rows to REMOVE; the caller (an
// engine driver) negates and AND-combines them into a keep-clause using its own
// SQL dialect. Returns nil when there are no row_exclude rules for table.
func (o *DBMSFilterOption) RowExcludeMatches(table string) []string {
	if o == nil {
		return nil
	}
	var matches []string
	for _, r := range o.Rules {
		if rf, ok := r.Matcher.(rowExcludeRule); ok && rf.table() == table {
			if m := rf.match(); m != "" {
				matches = append(matches, m)
			}
		}
	}
	return matches
}

// RowExcludeQueries returns the query documents of all row_exclude rules naming
// collection, in rule order, each as raw JSON. Every document describes
// documents to REMOVE; the MongoDB driver combines them under $nor so that only
// documents matching none of them are kept. Returns nil when there are no
// document-form row_exclude rules for collection.
func (o *DBMSFilterOption) RowExcludeQueries(collection string) []json.RawMessage {
	if o == nil {
		return nil
	}
	var queries []json.RawMessage
	for _, r := range o.Rules {
		if rf, ok := r.Matcher.(rowExcludeRule); ok && rf.table() == collection {
			if q := rf.query(); isJSONObject(q) {
				queries = append(queries, q)
			}
		}
	}
	return queries
}

// IsObjectExcluded reports whether a schema-global object (view, function,
// trigger, …) of the given kind and name is excluded. Only unqualified rules
// (no table) match; table-qualified rules — used for indexes — are ignored here.
func (o *DBMSFilterOption) IsObjectExcluded(kind, name string) bool {
	if o == nil {
		return false
	}
	for _, r := range o.Rules {
		if oe, ok := r.Matcher.(objectExcludeRule); ok &&
			oe.kind() == kind && oe.name() == name && oe.table() == "" {
			return true
		}
	}
	return false
}

// IsObjectExcludedInTable reports whether an object of the given kind and name
// belonging to table is excluded. A rule matches when its kind and name match
// and its table qualifier is either empty (any table) or equal to table. Used
// for per-table objects such as indexes.
func (o *DBMSFilterOption) IsObjectExcludedInTable(kind, table, name string) bool {
	if o == nil {
		return false
	}
	for _, r := range o.Rules {
		if oe, ok := r.Matcher.(objectExcludeRule); ok &&
			oe.kind() == kind && oe.name() == name &&
			(oe.table() == "" || oe.table() == table) {
			return true
		}
	}
	return false
}

// RuleTargets returns the name each rule addresses its target by, in rule order,
// deduplicated. Which field that is depends on the matcher: the table of a
// table_column or row_exclude rule, the table qualifier of an index
// object_exclude rule, and the object name of any other object_exclude rule.
//
// The names come back exactly as the rules spell them, and nothing here reads
// any structure into them. It exists for the engines whose rules may carry a
// schema qualifier ("sales.orders"), so a driver can report a rule pointing at a
// schema its dump does not cover — a judgement only that driver can make, since
// a dot means a qualifier on PostgreSQL but is an ordinary character in a
// MongoDB collection name.
func (o *DBMSFilterOption) RuleTargets() []string {
	if o == nil {
		return nil
	}
	var targets []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		targets = append(targets, name)
	}
	for _, r := range o.Rules {
		switch m := r.Matcher.(type) {
		case tableColumnRule:
			add(m.table())
		case rowExcludeRule:
			add(m.table())
		case objectExcludeRule:
			// An index rule qualifies its table and leaves the index name bare,
			// index names being unique per table rather than per schema; every
			// other kind carries the qualifier on the object's own name.
			if m.table() != "" {
				add(m.table())
			} else {
				add(m.name())
			}
		}
	}
	return targets
}

// FilterObjects returns names with objects of kind excluded by object_exclude
// rules removed, preserving order. A nil option passes everything.
func (o *DBMSFilterOption) FilterObjects(kind string, names []string) []string {
	if o == nil {
		return names
	}
	result := names[:0:0]
	for _, n := range names {
		if o.IsObjectExcluded(kind, n) {
			continue
		}
		result = append(result, n)
	}
	return result
}
