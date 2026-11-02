package filter

import (
	"encoding/json"
	"fmt"
)

// rowExcludeSQLEngines lists the engines whose predicate is written as raw SQL
// and attached to the per-table dump SELECT.
var rowExcludeSQLEngines = []string{"mysql", "mariadb", "postgresql"}

// rowExcludeDocEngines lists the engines whose predicate is written as a query
// document rather than SQL.
var rowExcludeDocEngines = []string{"mongodb"}

func init() {
	// row_exclude removes rows (documents) matching a predicate. It is
	// exclude-only like the rest of the pipeline: the table and every
	// non-matching row are kept. Registered for every engine — the predicate is
	// spelled differently per engine and Validate enforces the pairing.
	RegisterFilter("row_exclude", append(append([]string{}, rowExcludeSQLEngines...), rowExcludeDocEngines...),
		newRowExcludeMatcher)
}

// rowExcludeMatcher drops the rows of Table matching a predicate. The table
// itself and all non-matching rows are retained, so it narrows a table's data
// without excluding the table.
//
// Match is engine-shaped, and Validate enforces the pairing:
//
//   - SQL engines — a raw SQL predicate. The driver negates it into a
//     keep-clause using its own dialect's NULL handling.
//   - MongoDB — a query document as JSON text. The driver combines the
//     documents under $nor, which keeps everything that matches none of them.
//
// The query document is held as text because this package depends only on the
// standard library: converting it to BSON is the MongoDB driver's job.
type rowExcludeMatcher struct {
	Table string `json:"table"`
	Match string `json:"match"`
}

func newRowExcludeMatcher(raw json.RawMessage) (FilterMatcher, error) {
	var c struct {
		Table string `json:"table"`
		Match string `json:"match"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Table == "" {
		return nil, fmt.Errorf("row_exclude: table is required")
	}
	if c.Match == "" {
		return nil, fmt.Errorf("row_exclude: match is required")
	}
	return &rowExcludeMatcher{Table: c.Table, Match: c.Match}, nil
}

// jsonObjectText reports whether s is a JSON object — the form MongoDB's row
// predicate takes — and whether that object is empty. Invalid JSON, arrays,
// scalars and null all report ok=false.
func jsonObjectText(s string) (ok, empty bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return false, false
	}
	return true, len(m) == 0
}

// ExcludeRows builds a row_exclude rule for the relational engines (no JSON
// needed). Rows of table for which the SQL predicate match is TRUE are dropped;
// the table and all other rows are kept.
func ExcludeRows(table, match string) FilterRule {
	return FilterRule{Matcher: &rowExcludeMatcher{Table: table, Match: match}}
}

// ExcludeDocuments builds a row_exclude rule for MongoDB. Documents of
// collection matching the query document are dropped; the collection and all
// other documents are kept.
//
// query is a MongoDB query document encoded as JSON, e.g.
// []byte(`{"status":"cancelled"}`); it becomes the rule's match text.
func ExcludeDocuments(collection string, query json.RawMessage) FilterRule {
	return FilterRule{Matcher: &rowExcludeMatcher{Table: collection, Match: string(query)}}
}

func (m *rowExcludeMatcher) Type() string { return "row_exclude" }

// AppliesTo reports true for every supported engine. Whether this particular
// rule is usable depends on which predicate form it carries, which Validate
// checks against the engine.
func (m *rowExcludeMatcher) AppliesTo(dbmsType string) bool {
	for _, e := range rowExcludeSQLEngines {
		if e == dbmsType {
			return true
		}
	}
	for _, e := range rowExcludeDocEngines {
		if e == dbmsType {
			return true
		}
	}
	return false
}

func (m *rowExcludeMatcher) table() string { return m.Table }
func (m *rowExcludeMatcher) match() string { return m.Match }

// rowExcludeSQLEngine reports whether dbmsType takes a raw SQL predicate.
func rowExcludeSQLEngine(dbmsType string) bool {
	for _, e := range rowExcludeSQLEngines {
		if e == dbmsType {
			return true
		}
	}
	return false
}

// rowExcludeDocEngine reports whether dbmsType takes a query document.
func rowExcludeDocEngine(dbmsType string) bool {
	for _, e := range rowExcludeDocEngines {
		if e == dbmsType {
			return true
		}
	}
	return false
}
