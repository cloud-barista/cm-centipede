package filter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// =============================================================================
// row_exclude — MongoDB query-document form
// =============================================================================

func TestUnmarshal_RowExclude_Query(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"row_exclude","table":"orders","match":"{\"status\":\"cancelled\"}"}
	]}`)
	if len(o.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(o.Rules))
	}
	if o.Rules[0].Matcher.Type() != "row_exclude" {
		t.Errorf("type = %q", o.Rules[0].Matcher.Type())
	}

	got := o.RowExcludeQueries("orders")
	if len(got) != 1 {
		t.Fatalf("RowExcludeQueries = %v, want 1 document", got)
	}
	// The document is carried verbatim; converting it to BSON is the driver's job.
	var doc map[string]any
	if err := json.Unmarshal(got[0], &doc); err != nil {
		t.Fatalf("query is not valid JSON: %v", err)
	}
	if doc["status"] != "cancelled" {
		t.Errorf("query = %s", got[0])
	}
}

func TestRowExcludeQueries_OrderAndScoping(t *testing.T) {
	o := parse(t, `{"rules":[
		{"type":"row_exclude","table":"orders","match":"{\"status\":\"cancelled\"}"},
		{"type":"row_exclude","table":"logs","match":"{\"level\":\"debug\"}"},
		{"type":"row_exclude","table":"orders","match":"{\"total\":{\"$lt\":10}}"}
	]}`)

	got := o.RowExcludeQueries("orders")
	if len(got) != 2 {
		t.Fatalf("want the 2 rules naming orders, got %d", len(got))
	}
	// Rule order is preserved so the driver's $nor terms are deterministic.
	if !strings.Contains(string(got[0]), "cancelled") || !strings.Contains(string(got[1]), "$lt") {
		t.Errorf("queries out of order: %s, %s", got[0], got[1])
	}
	if q := o.RowExcludeQueries("missing"); q != nil {
		t.Errorf("unrelated collection = %v, want nil", q)
	}
}

func TestRowExcludeQueries_NilOption(t *testing.T) {
	var o *DBMSFilterOption
	if q := o.RowExcludeQueries("orders"); q != nil {
		t.Errorf("nil option = %v, want nil", q)
	}
}

// The two predicate forms must not be mixed up: a SQL string cannot be handed to
// MongoDB, and a query document cannot be spliced into a SELECT.
func TestValidate_RowExclude_EnginePairing(t *testing.T) {
	sqlForm := parse(t, `{"rules":[{"type":"row_exclude","table":"orders","match":"total < 10"}]}`)
	docForm := parse(t, `{"rules":[{"type":"row_exclude","table":"orders","match":"{\"total\":{\"$lt\":10}}"}]}`)

	for _, engine := range []string{"mysql", "mariadb", "postgresql"} {
		if err := sqlForm.Validate(engine, true); err != nil {
			t.Errorf("SQL match on %s should be valid: %v", engine, err)
		}
		err := docForm.Validate(engine, true)
		if err == nil {
			t.Errorf("query document on %s must be rejected", engine)
		} else if !strings.Contains(err.Error(), "takes a SQL predicate") {
			t.Errorf("unexpected error for query document on %s: %v", engine, err)
		}
	}

	if err := docForm.Validate("mongodb", true); err != nil {
		t.Errorf("query document on mongodb should be valid: %v", err)
	}
	err := sqlForm.Validate("mongodb", true)
	if err == nil {
		t.Error("SQL match on mongodb must be rejected")
	} else if !strings.Contains(err.Error(), "takes a query document") {
		t.Errorf("unexpected error for SQL match on mongodb: %v", err)
	}
}

// MongoDB's match must be a JSON object; anything else is a SQL predicate as far
// as the pairing is concerned, and an empty object would exclude every document.
func TestValidate_RowExclude_MongoMatchShape(t *testing.T) {
	cases := []struct {
		name    string
		match   string
		wantErr string
	}{
		{"array", `[{"a":1}]`, "takes a query document"},
		{"scalar", `"a = 1"`, "takes a query document"},
		{"null", `null`, "takes a query document"},
		{"malformed", `{"a":`, "takes a query document"},
		{"empty object", `{}`, "match is required"},
		{"empty object with spaces", "{ \n }", "match is required"},
	}
	for _, c := range cases {
		o := &DBMSFilterOption{Rules: []FilterRule{ExcludeRows("orders", c.match)}}
		err := o.Validate("mongodb", true)
		if err == nil {
			t.Errorf("%s: expected a validation error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want it to mention %q", c.name, err, c.wantErr)
		}
	}
}

// Document-form row exclusion is direct-only for the same reason the SQL form is:
// mongodump's --query needs a single --collection, so per-collection predicates
// cannot be expressed in one invocation.
func TestValidate_RowExcludeQuery_RequiresDirect(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"row_exclude","table":"orders","match":"{\"status\":\"cancelled\"}"}]}`)
	if err := o.Validate("mongodb", true); err != nil {
		t.Errorf("direct access should be valid: %v", err)
	}
	err := o.Validate("mongodb", false)
	if err == nil {
		t.Fatal("query row exclusion over ssh-tunnel must be rejected")
	}
	if !strings.Contains(err.Error(), "requires direct access") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUnmarshal_RowExclude_PredicateFormErrors(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			// Which shape match takes depends on the engine, so parsing only
			// requires it to be present; Validate checks the shape.
			name:    "match missing",
			doc:     `{"rules":[{"type":"row_exclude","table":"t"}]}`,
			wantErr: "match is required",
		},
		{
			name:    "match is empty",
			doc:     `{"rules":[{"type":"row_exclude","table":"t","match":""}]}`,
			wantErr: "match is required",
		},
		{
			name:    "match is an object, not text",
			doc:     `{"rules":[{"type":"row_exclude","table":"t","match":{"a":1}}]}`,
			wantErr: "cannot unmarshal",
		},
		{
			name:    "table missing",
			doc:     `{"rules":[{"type":"row_exclude","match":"{\"a\":1}"}]}`,
			wantErr: "table is required",
		},
	}

	for _, c := range cases {
		var o DBMSFilterOption
		err := json.Unmarshal([]byte(c.doc), &o)
		if err == nil {
			t.Errorf("%s: expected a parse error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want it to mention %q", c.name, err, c.wantErr)
		}
	}
}

// An empty query document would match — and therefore exclude — every
// document, so it never reaches the driver even from a rule built in code.
func TestRowExcludeQueries_SkipsEmptyDocument(t *testing.T) {
	o := &DBMSFilterOption{Rules: []FilterRule{
		ExcludeDocuments("orders", json.RawMessage(`{}`)),
		ExcludeDocuments("orders", json.RawMessage(`{"status":"cancelled"}`)),
	}}
	got := o.RowExcludeQueries("orders")
	if len(got) != 1 || !strings.Contains(string(got[0]), "cancelled") {
		t.Errorf("RowExcludeQueries = %s, want only the non-empty document", got)
	}
}

func TestRowExcludeQuery_RoundTrip(t *testing.T) {
	doc := `{"rules":[{"type":"row_exclude","table":"orders","match":"{\"status\":\"cancelled\"}"}]}`
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

func TestExcludeDocuments_Builder(t *testing.T) {
	o := &DBMSFilterOption{Rules: []FilterRule{
		ExcludeDocuments("orders", json.RawMessage(`{"status":"cancelled"}`)),
	}}
	if err := o.Validate("mongodb", true); err != nil {
		t.Fatalf("builder rule should validate: %v", err)
	}
	got := o.RowExcludeQueries("orders")
	if len(got) != 1 || !strings.Contains(string(got[0]), "cancelled") {
		t.Errorf("RowExcludeQueries = %v", got)
	}
	// The builder must not produce a rule the relational engines would accept.
	if err := o.Validate("mysql", true); err == nil {
		t.Error("a document-form rule must be rejected for mysql")
	}
}

// AppliesTo is true for every engine; the engine check lives in the
// predicate-form pairing in Validate.
func TestRowExclude_AppliesToAllEngines(t *testing.T) {
	o := parse(t, `{"rules":[{"type":"row_exclude","table":"t","match":"a = 1"}]}`)
	m := o.Rules[0].Matcher
	for _, engine := range []string{"mysql", "mariadb", "postgresql", "mongodb"} {
		if !m.AppliesTo(engine) {
			t.Errorf("AppliesTo(%q) = false, want true", engine)
		}
	}
	if m.AppliesTo("oracle") {
		t.Error("AppliesTo(oracle) = true, want false")
	}
}
