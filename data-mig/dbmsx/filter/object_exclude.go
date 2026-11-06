package filter

import (
	"encoding/json"
	"fmt"
)

// Object kinds excludable via the object_exclude matcher. These mirror the
// schema objects Inspect reports per engine; excluding one keeps it out of the
// migration dump.
const (
	ObjectKindView             = "view"
	ObjectKindIndex            = "index"
	ObjectKindFunction         = "function"
	ObjectKindProcedure        = "procedure"
	ObjectKindTrigger          = "trigger"
	ObjectKindEvent            = "event"
	ObjectKindForeignKey       = "foreign_key"
	ObjectKindMaterializedView = "materialized_view"
	ObjectKindSequence         = "sequence"
	ObjectKindType             = "type"
	ObjectKindExtension        = "extension"
	ObjectKindRule             = "rule"
	ObjectKindValidator        = "validator"
)

// objectKindEngines maps each object kind to the engines that support it. The
// object set differs per engine (events are MySQL/MariaDB-only; materialized
// views/sequences/types/extensions/rules are PostgreSQL-only; validators are
// MongoDB-only), so a rule's validity depends on both the kind and the source
// engine — enforced by AppliesTo.
var objectKindEngines = map[string][]string{
	ObjectKindView:             {"mysql", "mariadb", "postgresql", "mongodb"},
	ObjectKindIndex:            {"mysql", "mariadb", "postgresql", "mongodb"},
	ObjectKindFunction:         {"mysql", "mariadb", "postgresql"},
	ObjectKindProcedure:        {"mysql", "mariadb", "postgresql"},
	ObjectKindTrigger:          {"mysql", "mariadb", "postgresql"},
	ObjectKindForeignKey:       {"mysql", "mariadb", "postgresql"},
	ObjectKindEvent:            {"mysql", "mariadb"},
	ObjectKindMaterializedView: {"postgresql"},
	ObjectKindSequence:         {"postgresql"},
	ObjectKindType:             {"postgresql"},
	ObjectKindExtension:        {"postgresql"},
	ObjectKindRule:             {"postgresql"},
	ObjectKindValidator:        {"mongodb"},
}

func init() {
	// object_exclude removes a single named schema object (view, function,
	// trigger, …). The valid kinds differ per engine, so AppliesTo checks the
	// kind against the source engine. Registered for every engine; the per-kind
	// engine scope is enforced by AppliesTo.
	RegisterFilter("object_exclude", []string{"mysql", "mariadb", "postgresql", "mongodb"}, newObjectExcludeMatcher)
}

// objectExcludeMatcher excludes the schema object of Kind named Name (exact
// match). The object's owning table/collection and everything else are kept.
//
// Table is an optional qualifier that is only meaningful for kind "index":
// index names are unique per table/collection (not per schema), so Table pins
// the rule to one table. When Table is empty an index rule matches by name in
// any table. Table must be empty for all other (schema-global) kinds.
type objectExcludeMatcher struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Table string `json:"table,omitempty"`
}

func newObjectExcludeMatcher(raw json.RawMessage) (FilterMatcher, error) {
	var c struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Table string `json:"table,omitempty"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Kind == "" {
		return nil, fmt.Errorf("object_exclude: kind is required")
	}
	if _, ok := objectKindEngines[c.Kind]; !ok {
		return nil, fmt.Errorf("object_exclude: unknown kind %q", c.Kind)
	}
	if c.Name == "" {
		return nil, fmt.Errorf("object_exclude: name is required")
	}
	if c.Table != "" && c.Kind != ObjectKindIndex {
		return nil, fmt.Errorf("object_exclude: table is only valid for kind %q, not %q", ObjectKindIndex, c.Kind)
	}
	return &objectExcludeMatcher{Kind: c.Kind, Name: c.Name, Table: c.Table}, nil
}

// ExcludeObject builds an object_exclude rule in code (no JSON needed). The
// object of kind named name is dropped from the dump; everything else is kept.
func ExcludeObject(kind, name string) FilterRule {
	return FilterRule{Matcher: &objectExcludeMatcher{Kind: kind, Name: name}}
}

// ExcludeIndex builds an object_exclude rule for an index. When table is empty
// the rule matches an index of that name in any table; when set it pins the
// rule to that one table/collection.
func ExcludeIndex(table, name string) FilterRule {
	return FilterRule{Matcher: &objectExcludeMatcher{Kind: ObjectKindIndex, Name: name, Table: table}}
}

func (m *objectExcludeMatcher) Type() string { return "object_exclude" }

func (m *objectExcludeMatcher) AppliesTo(dbmsType string) bool {
	for _, e := range objectKindEngines[m.Kind] {
		if e == dbmsType {
			return true
		}
	}
	return false
}

func (m *objectExcludeMatcher) kind() string  { return m.Kind }
func (m *objectExcludeMatcher) name() string  { return m.Name }
func (m *objectExcludeMatcher) table() string { return m.Table }
