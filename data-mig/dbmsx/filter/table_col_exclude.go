package filter

import (
	"encoding/json"
	"fmt"
)

func init() {
	// table_column applies to every engine (nil = all). For relational engines
	// "table"/"columns" are tables and columns; for MongoDB they are collections
	// and document fields.
	RegisterFilter("table_column", nil, newTableColumnMatcher)
}

// tableColumnMatcher excludes either a whole table (Columns empty) or specific
// columns within a table (Columns set, the table itself is kept).
type tableColumnMatcher struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns,omitempty"`
}

func newTableColumnMatcher(raw json.RawMessage) (FilterMatcher, error) {
	var c struct {
		Table   string   `json:"table"`
		Columns []string `json:"columns,omitempty"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Table == "" {
		return nil, fmt.Errorf("table_column: table is required")
	}
	return &tableColumnMatcher{Table: c.Table, Columns: c.Columns}, nil
}

// ExcludeTable builds a table_column exclusion rule in code (no JSON needed).
// With no columns the whole table is excluded; with columns the table is kept
// and only those columns are dropped.
func ExcludeTable(table string, columns ...string) FilterRule {
	return FilterRule{Matcher: &tableColumnMatcher{Table: table, Columns: columns}}
}

func (m *tableColumnMatcher) Type() string          { return "table_column" }
func (m *tableColumnMatcher) AppliesTo(string) bool { return true }
func (m *tableColumnMatcher) table() string         { return m.Table }
func (m *tableColumnMatcher) columns() []string     { return m.Columns }
