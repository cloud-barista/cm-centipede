package filter

import (
	"bytes"
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
// The predicate is engine-shaped, and exactly one form must be set:
//
//   - Match — a raw SQL predicate for the relational engines. The driver negates
//     it into a keep-clause using its own dialect's NULL handling.
//   - Query — a MongoDB query document for MongoDB. The driver combines the
//     documents under $nor, which keeps everything that matches none of them.
//
// Query is held as raw JSON because this package depends only on the standard
// library: converting it to BSON is the MongoDB driver's job.
type rowExcludeMatcher struct {
	Table string          `json:"table"`
	Match string          `json:"match,omitempty"`
	Query json.RawMessage `json:"query,omitempty"`
}

func newRowExcludeMatcher(raw json.RawMessage) (FilterMatcher, error) {
	var c struct {
		Table string          `json:"table"`
		Match string          `json:"match"`
		Query json.RawMessage `json:"query"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Table == "" {
		return nil, fmt.Errorf("row_exclude: table is required")
	}

	// An explicit null is treated as an absent field, not as an empty predicate:
	// an empty query document would match — and therefore exclude — everything.
	if !jsonPresent(c.Query) {
		c.Query = nil
	} else if !isJSONObject(c.Query) {
		return nil, fmt.Errorf("row_exclude: query must be a JSON object")
	}
	hasQuery := c.Query != nil

	// Which form is valid depends on the engine, but "exactly one" does not, so
	// it is settled here rather than in Validate.
	switch {
	case c.Match == "" && !hasQuery:
		return nil, fmt.Errorf("row_exclude: either match (SQL engines) or query (MongoDB) is required")
	case c.Match != "" && hasQuery:
		return nil, fmt.Errorf("row_exclude: match and query are mutually exclusive")
	}

	return &rowExcludeMatcher{Table: c.Table, Match: c.Match, Query: c.Query}, nil
}

// jsonPresent reports whether raw carries a value at all. An omitted field and
// an explicit JSON null both count as absent.
func jsonPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// isJSONObject reports whether raw is a present JSON object — not an array, not a
// scalar, not null.
func isJSONObject(raw json.RawMessage) bool {
	return jsonPresent(raw) && bytes.TrimSpace(raw)[0] == '{'
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
// []byte(`{"status":"cancelled"}`).
func ExcludeDocuments(collection string, query json.RawMessage) FilterRule {
	return FilterRule{Matcher: &rowExcludeMatcher{Table: collection, Query: query}}
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

func (m *rowExcludeMatcher) table() string          { return m.Table }
func (m *rowExcludeMatcher) match() string          { return m.Match }
func (m *rowExcludeMatcher) query() json.RawMessage { return m.Query }

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
