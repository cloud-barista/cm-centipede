package filter

import (
	"encoding/json"
	"reflect"
	"testing"
)

// parse is a small helper that unmarshals a filter JSON document.
func parse(t *testing.T, doc string) *DBMSFilterOption {
	t.Helper()
	var o DBMSFilterOption
	if err := json.Unmarshal([]byte(doc), &o); err != nil {
		t.Fatalf("unmarshal %s: %v", doc, err)
	}
	return &o
}

func TestUnmarshal_TableColumn(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"audit_logs"},
		{"type":"table_column","table":"users","columns":["password","ssn"]}
	]}`)
	if len(o.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(o.Rules))
	}
	if o.Rules[0].Matcher.Type() != "table_column" {
		t.Errorf("rule0 type = %q", o.Rules[0].Matcher.Type())
	}
}

func TestUnmarshal_UnknownType(t *testing.T) {
	var o DBMSFilterOption
	err := json.Unmarshal([]byte(`{"rules":[{"type":"nope","table":"t"}]}`), &o)
	if err == nil {
		t.Fatal("want error for unknown type, got nil")
	}
}

func TestUnmarshal_MissingTable(t *testing.T) {
	var o DBMSFilterOption
	if err := json.Unmarshal([]byte(`{"rules":[{"type":"table_column"}]}`), &o); err == nil {
		t.Fatal("want error for missing table, got nil")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	doc := `{"rules":[{"type":"table_column","table":"users","columns":["password"]}]}`
	o := parse(t, doc)
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Re-parse both and compare structurally (key order is not guaranteed).
	var a, b map[string]any
	_ = json.Unmarshal([]byte(doc), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round-trip mismatch:\n in: %s\nout: %s", doc, out)
	}
}

func TestIsTableExcluded(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"audit_logs"},
		{"type":"table_column","table":"users","columns":["password"]}
	]}`)
	if !o.IsTableExcluded("audit_logs") {
		t.Error("audit_logs should be wholly excluded")
	}
	if o.IsTableExcluded("users") {
		t.Error("users has only column exclusion; table must be kept")
	}
	if o.IsTableExcluded("orders") {
		t.Error("orders is not in any rule")
	}
}

func TestFilterTables(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"table_column","table":"audit_logs"}]}`)
	got := o.FilterTables([]string{"users", "audit_logs", "orders"})
	want := []string{"users", "orders"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterTables = %v, want %v", got, want)
	}
}

func TestFilterTables_NilOption(t *testing.T) {
	var o *DBMSFilterOption
	in := []string{"a", "b"}
	if got := o.FilterTables(in); !reflect.DeepEqual(got, in) {
		t.Errorf("nil option must pass everything, got %v", got)
	}
}

func TestExcludedColumnsAndKeep(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"users","columns":["password","ssn"]},
		{"type":"table_column","table":"users","columns":["token"]}
	]}`)
	ex := o.ExcludedColumns("users")
	for _, c := range []string{"password", "ssn", "token"} {
		if !ex[c] {
			t.Errorf("expected %q excluded", c)
		}
	}
	if !o.HasColumnExclusion("users") {
		t.Error("users should report column exclusion")
	}
	got := o.KeepColumns("users", []string{"id", "password", "name", "ssn", "token", "email"})
	want := []string{"id", "name", "email"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KeepColumns = %v, want %v", got, want)
	}
}

func TestExcludedTableNames(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"a"},
		{"type":"table_column","table":"b","columns":["x"]},
		{"type":"table_column","table":"a"}
	]}`)
	got := o.ExcludedTableNames()
	want := []string{"a"} // only whole-table rules, deduplicated
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExcludedTableNames = %v, want %v", got, want)
	}
}

func TestValidate_ColumnExclusionRequiresDirect(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"table_column","table":"users","columns":["password"]}]}`)
	if err := o.Validate("mysql", true); err != nil {
		t.Errorf("direct access should be valid: %v", err)
	}
	if err := o.Validate("mysql", false); err == nil {
		t.Error("column exclusion over ssh-tunnel must be rejected")
	}
}

func TestValidate_WholeTableAllowedOverTunnel(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"table_column","table":"audit_logs"}]}`)
	if err := o.Validate("mysql", false); err != nil {
		t.Errorf("whole-table exclusion over ssh-tunnel should be valid: %v", err)
	}
}

func TestValidate_NilOption(t *testing.T) {
	var o *DBMSFilterOption
	if err := o.Validate("mysql", false); err != nil {
		t.Errorf("nil option must validate: %v", err)
	}
}

func TestUnmarshal_RowExclude(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"row_exclude","table":"orders","match":"created_at < '2023-01-01'"},
		{"type":"row_exclude","table":"orders","match":"status = 'cancelled'"}
	]}`)
	if len(o.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(o.Rules))
	}
	if o.Rules[0].Matcher.Type() != "row_exclude" {
		t.Errorf("rule0 type = %q", o.Rules[0].Matcher.Type())
	}
}

func TestUnmarshal_RowExclude_MissingMatch(t *testing.T) {
	var o DBMSFilterOption
	if err := json.Unmarshal([]byte(`{"rules":[{"type":"row_exclude","table":"t"}]}`), &o); err == nil {
		t.Fatal("want error for missing match, got nil")
	}
}

func TestUnmarshal_RowExclude_MissingTable(t *testing.T) {
	var o DBMSFilterOption
	if err := json.Unmarshal([]byte(`{"rules":[{"type":"row_exclude","match":"id > 0"}]}`), &o); err == nil {
		t.Fatal("want error for missing table, got nil")
	}
}

func TestRowExcludeMatches(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"orders","columns":["secret"]},
		{"type":"row_exclude","table":"orders","match":"created_at < '2023-01-01'"},
		{"type":"row_exclude","table":"orders","match":"status = 'cancelled'"},
		{"type":"row_exclude","table":"users","match":"deleted = 1"}
	]}`)
	got := o.RowExcludeMatches("orders")
	want := []string{"created_at < '2023-01-01'", "status = 'cancelled'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RowExcludeMatches(orders) = %v, want %v", got, want)
	}
	if got := o.RowExcludeMatches("nope"); got != nil {
		t.Errorf("RowExcludeMatches(nope) = %v, want nil", got)
	}
}

func TestRowExcludeMatches_NilOption(t *testing.T) {
	var o *DBMSFilterOption
	if got := o.RowExcludeMatches("t"); got != nil {
		t.Errorf("nil option must return nil, got %v", got)
	}
}

func TestValidate_RowExcludeRequiresDirect(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"row_exclude","table":"orders","match":"id > 100"}]}`)
	if err := o.Validate("mysql", true); err != nil {
		t.Errorf("direct access should be valid: %v", err)
	}
	if err := o.Validate("mysql", false); err == nil {
		t.Error("row exclusion over ssh-tunnel must be rejected")
	}
}

func TestValidate_RowExcludeUnsupportedEngine(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"row_exclude","table":"orders","match":"id > 100"}]}`)
	if err := o.Validate("mongodb", true); err == nil {
		t.Error("row exclusion on mongodb must be rejected")
	}
	for _, engine := range []string{"mysql", "mariadb", "postgresql"} {
		if err := o.Validate(engine, true); err != nil {
			t.Errorf("row exclusion on %s should be valid: %v", engine, err)
		}
	}
}

func TestRowExclude_RoundTrip(t *testing.T) {
	doc := `{"rules":[{"type":"row_exclude","table":"orders","match":"status = 'cancelled'"}]}`
	o := parse(t, doc)
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a, b map[string]any
	_ = json.Unmarshal([]byte(doc), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round-trip mismatch:\n in: %s\nout: %s", doc, out)
	}
}

func TestExcludeRows_Builder(t *testing.T) {
	o := &DBMSFilterOption{Rules: []FilterRule{
		ExcludeRows("orders", "status = 'cancelled'"),
	}}
	if err := o.Validate("postgresql", true); err != nil {
		t.Fatalf("builder rule should validate: %v", err)
	}
	got := o.RowExcludeMatches("orders")
	want := []string{"status = 'cancelled'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RowExcludeMatches = %v, want %v", got, want)
	}
}

func TestUnmarshal_ObjectExclude(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"object_exclude","kind":"view","name":"v_legacy"},
		{"type":"object_exclude","kind":"trigger","name":"trg_audit"}
	]}`)
	if len(o.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(o.Rules))
	}
	if o.Rules[0].Matcher.Type() != "object_exclude" {
		t.Errorf("rule0 type = %q", o.Rules[0].Matcher.Type())
	}
}

func TestUnmarshal_ObjectExclude_Invalid(t *testing.T) {
	cases := []string{
		`{"rules":[{"type":"object_exclude","name":"x"}]}`,               // missing kind
		`{"rules":[{"type":"object_exclude","kind":"view"}]}`,            // missing name
		`{"rules":[{"type":"object_exclude","kind":"nope","name":"x"}]}`, // unknown kind
	}
	for _, doc := range cases {
		var o DBMSFilterOption
		if err := json.Unmarshal([]byte(doc), &o); err == nil {
			t.Errorf("want error for %s, got nil", doc)
		}
	}
}

func TestIsObjectExcludedAndFilter(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"object_exclude","kind":"view","name":"v_legacy"},
		{"type":"object_exclude","kind":"function","name":"fn_old"}
	]}`)
	if !o.IsObjectExcluded("view", "v_legacy") {
		t.Error("v_legacy view should be excluded")
	}
	if o.IsObjectExcluded("function", "v_legacy") {
		t.Error("kind must match: v_legacy is a view, not a function")
	}
	got := o.FilterObjects("view", []string{"v_active", "v_legacy", "v_report"})
	want := []string{"v_active", "v_report"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterObjects(view) = %v, want %v", got, want)
	}
	// A function-kind filter must not touch view names.
	if got := o.FilterObjects("view", []string{"fn_old"}); !reflect.DeepEqual(got, []string{"fn_old"}) {
		t.Errorf("FilterObjects must be kind-scoped, got %v", got)
	}
}

func TestFilterObjects_NilOption(t *testing.T) {
	var o *DBMSFilterOption
	in := []string{"a", "b"}
	if got := o.FilterObjects("view", in); !reflect.DeepEqual(got, in) {
		t.Errorf("nil option must pass everything, got %v", got)
	}
}

func TestValidate_ObjectExcludeRequiresDirect(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"object_exclude","kind":"trigger","name":"trg_audit"}]}`)
	if err := o.Validate("mysql", true); err != nil {
		t.Errorf("direct access should be valid: %v", err)
	}
	if err := o.Validate("mysql", false); err == nil {
		t.Error("object exclusion over ssh-tunnel must be rejected")
	}
}

func TestValidate_ObjectExcludeEngineScope(t *testing.T) {
	// events: MySQL/MariaDB only.
	ev := parse(t, `{"rules":[{"type":"object_exclude","kind":"event","name":"ev_x"}]}`)
	if err := ev.Validate("mysql", true); err != nil {
		t.Errorf("event on mysql should be valid: %v", err)
	}
	if err := ev.Validate("postgresql", true); err == nil {
		t.Error("event on postgresql must be rejected")
	}
	// sequence: PostgreSQL only.
	seq := parse(t, `{"rules":[{"type":"object_exclude","kind":"sequence","name":"s_x"}]}`)
	if err := seq.Validate("postgresql", true); err != nil {
		t.Errorf("sequence on postgresql should be valid: %v", err)
	}
	if err := seq.Validate("mysql", true); err == nil {
		t.Error("sequence on mysql must be rejected")
	}
	// validator: MongoDB only.
	val := parse(t, `{"rules":[{"type":"object_exclude","kind":"validator","name":"orders"}]}`)
	if err := val.Validate("mongodb", true); err != nil {
		t.Errorf("validator on mongodb should be valid: %v", err)
	}
	if err := val.Validate("mysql", true); err == nil {
		t.Error("validator on mysql must be rejected")
	}
}

func TestObjectExclude_RoundTrip(t *testing.T) {
	doc := `{"rules":[{"type":"object_exclude","kind":"view","name":"v_legacy"}]}`
	o := parse(t, doc)
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a, b map[string]any
	_ = json.Unmarshal([]byte(doc), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round-trip mismatch:\n in: %s\nout: %s", doc, out)
	}
}

func TestExcludeObject_Builder(t *testing.T) {
	o := &DBMSFilterOption{Rules: []FilterRule{
		ExcludeObject(ObjectKindView, "v_legacy"),
	}}
	if err := o.Validate("mysql", true); err != nil {
		t.Fatalf("builder rule should validate: %v", err)
	}
	if !o.IsObjectExcluded("view", "v_legacy") {
		t.Error("builder rule should exclude the view")
	}
}

func TestObjectExclude_Index(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"object_exclude","kind":"index","table":"orders","name":"idx_created"},
		{"type":"object_exclude","kind":"index","name":"idx_global"}
	]}`)
	// Table-qualified rule matches only its table.
	if !o.IsObjectExcludedInTable("index", "orders", "idx_created") {
		t.Error("idx_created on orders should be excluded")
	}
	if o.IsObjectExcludedInTable("index", "users", "idx_created") {
		t.Error("idx_created on users must NOT be excluded (table-qualified)")
	}
	// Unqualified index rule matches in any table.
	if !o.IsObjectExcludedInTable("index", "orders", "idx_global") {
		t.Error("idx_global should be excluded in any table")
	}
	if !o.IsObjectExcludedInTable("index", "whatever", "idx_global") {
		t.Error("idx_global should be excluded in any table")
	}
	// IsObjectExcluded (schema-global helper) ignores table-qualified rules.
	if o.IsObjectExcluded("index", "idx_created") {
		t.Error("table-qualified rule must not match the unqualified helper")
	}
	if !o.IsObjectExcluded("index", "idx_global") {
		t.Error("unqualified rule should match the unqualified helper")
	}
}

func TestObjectExclude_TableOnlyForIndex(t *testing.T) {
	var o DBMSFilterOption
	err := json.Unmarshal([]byte(`{"rules":[{"type":"object_exclude","kind":"view","table":"orders","name":"v"}]}`), &o)
	if err == nil {
		t.Fatal("table on a non-index kind must be rejected")
	}
}

func TestValidate_IndexAllEngines(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"object_exclude","kind":"index","table":"orders","name":"idx"}]}`)
	for _, engine := range []string{"mysql", "mariadb", "postgresql", "mongodb"} {
		if err := o.Validate(engine, true); err != nil {
			t.Errorf("index on %s should be valid: %v", engine, err)
		}
	}
	if err := o.Validate("mysql", false); err == nil {
		t.Error("index exclusion over ssh-tunnel must be rejected")
	}
}

func TestExcludeIndex_Builder(t *testing.T) {
	o := &DBMSFilterOption{Rules: []FilterRule{ExcludeIndex("orders", "idx_created")}}
	if err := o.Validate("mysql", true); err != nil {
		t.Fatalf("builder rule should validate: %v", err)
	}
	if !o.IsObjectExcludedInTable("index", "orders", "idx_created") {
		t.Error("builder rule should exclude the index")
	}
}

func TestRuleTargets(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"table_column","table":"sales.orders"},
		{"type":"table_column","table":"users","columns":["ssn"]},
		{"type":"row_exclude","table":"sales.events","match":"id < 10"},
		{"type":"object_exclude","kind":"view","name":"sales.v_legacy"},
		{"type":"object_exclude","kind":"index","table":"sales.orders","name":"idx_created"}
	]}`)
	// The index rule repeats the table its sibling table_column rule names, and a
	// target is reported once however many rules reach for it.
	want := []string{"sales.orders", "users", "sales.events", "sales.v_legacy"}
	if got := o.RuleTargets(); !reflect.DeepEqual(got, want) {
		t.Errorf("RuleTargets() = %v, want %v", got, want)
	}
	if got := (*DBMSFilterOption)(nil).RuleTargets(); got != nil {
		t.Errorf("nil option = %v, want nil", got)
	}
}
