package filter

import (
	"reflect"
	"testing"
)

func TestPgRoutineRuleMatches(t *testing.T) {
	const args14 = "IN p_order_id integer, IN p_method text"
	const args13 = "p_order_id integer, p_method text"
	cases := []struct {
		rule, args string
		want       bool
	}{
		{"sp_pay", args13, true},
		{"public.sp_pay", args13, true},
		{"sales.sp_pay", args13, false},
		{"sp_other", args13, false},
		{"public.sp_pay(p_order_id integer, p_method text)", args13, true},
		{"public.sp_pay(p_order_id integer, p_method text)", args14, true},
		{"public.sp_pay(IN p_order_id integer, IN p_method text)", args13, true},
		{"public.sp_pay(p_order_id integer,p_method  text)", args13, true},
		{`"public"."sp_pay"(p_order_id integer, p_method text)`, args13, true},
		{"sp_pay(p_order_id integer, p_method text)", args13, true},
		{"public.sp_pay(p_order_id integer)", args13, false},
		{"public.sp_pay()", args13, false},
		{"public.sp_pay()", "", true},
		{"sales.sp_pay(p_order_id integer, p_method text)", args13, false},
		{"public.sp_pay(p_order_id integer", args13, false},
	}
	for _, c := range cases {
		if got := PgRoutineRuleMatches(c.rule, "public", "sp_pay", c.args); got != c.want {
			t.Errorf("PgRoutineRuleMatches(%q, args %q) = %v, want %v", c.rule, c.args, got, c.want)
		}
	}
}

func TestPgRoutineRuleMatchesQuoted(t *testing.T) {
	if !PgRoutineRuleMatches(`app."Calc ""x"""(a integer)`, "app", `Calc "x"`, "a integer") {
		t.Error("quoted identifier with an embedded quote should match")
	}
	if PgRoutineRuleMatches(`app."calc ""x"""(a integer)`, "app", `Calc "x"`, "a integer") {
		t.Error("a quoted identifier is case-sensitive")
	}
}

// One of two overloads is taken by its signature; a bare name takes both.
func TestPgRoutineRuleMatchesOverloads(t *testing.T) {
	overloads := []string{"a integer", "a integer, b text"}
	for rule, want := range map[string][]bool{
		"public.calc(a integer)":         {true, false},
		"public.calc(a integer, b text)": {false, true},
		"calc":                           {true, true},
	} {
		for i, args := range overloads {
			if got := PgRoutineRuleMatches(rule, "public", "calc", args); got != want[i] {
				t.Errorf("rule %q on calc(%s) = %v, want %v", rule, args, got, want[i])
			}
		}
	}
}

func TestObjectNameMatches(t *testing.T) {
	const proc = "public.sp_pay(IN p_order_id integer, IN p_method text)"
	cases := []struct {
		engine, kind, rule, inv string
		want                    bool
	}{
		{"postgresql", ObjectKindProcedure, "sp_pay", proc, true},
		{"postgresql", ObjectKindProcedure, "public.sp_pay", proc, true},
		{"postgresql", ObjectKindProcedure, "public.sp_pay(p_order_id integer, p_method text)", proc, true},
		{"postgresql", ObjectKindProcedure, "public.sp_pay(p_order_id integer)", proc, false},
		{"postgresql", ObjectKindFunction, "fn_total", "public.fn_total()", true},
		{"postgresql", ObjectKindView, "v_low_stock", "public.v_low_stock", true},
		{"postgresql", ObjectKindView, "public.v_low_stock", "public.v_low_stock", true},
		{"postgresql", ObjectKindView, "sales.v_low_stock", "public.v_low_stock", false},
		{"postgresql", ObjectKindView, "Mixed", `public."Mixed"`, true},
		{"postgresql", ObjectKindExtension, "pgcrypto", "pgcrypto", true},
		{"postgresql", ObjectKindExtension, "uuid-ossp", `"uuid-ossp"`, true},
		// MySQL lists routines by bare name and its dump takes them only so.
		{"mysql", ObjectKindProcedure, "sp_pay", "sp_pay", true},
		{"mysql", ObjectKindProcedure, "shop.sp_pay", "sp_pay", false},
		{"mysql", ObjectKindProcedure, "sp_pay()", "sp_pay", false},
		{"mariadb", ObjectKindView, "v1", "v1", true},
		{"mongodb", ObjectKindView, "a.b", "a.b", true},
		{"mongodb", ObjectKindView, "b", "a.b", false},
	}
	for _, c := range cases {
		if got := ObjectNameMatches(c.engine, c.kind, c.rule, c.inv); got != c.want {
			t.Errorf("ObjectNameMatches(%s, %s, %q, %q) = %v, want %v", c.engine, c.kind, c.rule, c.inv, got, c.want)
		}
	}
}

func TestObjectExcludeNames(t *testing.T) {
	f := &DBMSFilterOption{Rules: []FilterRule{
		ExcludeObject(ObjectKindProcedure, "a"),
		ExcludeObject(ObjectKindFunction, "b"),
		ExcludeIndex("t", "idx"),
		ExcludeObject(ObjectKindProcedure, "public.c(integer)"),
		ExcludeTable("t"),
	}}
	if got, want := f.ObjectExcludeNames(ObjectKindProcedure), []string{"a", "public.c(integer)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ObjectExcludeNames(procedure) = %v, want %v", got, want)
	}
	if got := f.ObjectExcludeNames(ObjectKindIndex); got != nil {
		t.Errorf("a table-qualified index rule is not listed, got %v", got)
	}
	var none *DBMSFilterOption
	if got := none.ObjectExcludeNames(ObjectKindView); got != nil {
		t.Errorf("nil filter = %v, want nil", got)
	}
}
