package postgresql

import (
	"reflect"
	"testing"

	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

func fkRow(name, tbl, cols, refSchema, ref, refCols string) pgInventoryRow {
	return pgInventoryRow{
		"name": "public." + name, "obj": name, "tbl_schema": "public", "tbl": tbl, "cols": cols,
		"ref_schema": refSchema, "ref_tbl": ref, "ref_cols": refCols,
	}
}

func TestPGKeepForeignKey(t *testing.T) {
	rows := []pgInventoryRow{
		fkRow("fk_items_order", "order_items", "order_id", "public", "orders", "order_id"),
		fkRow("fk_wishlist_customer", "wishlist", "customer_id", "public", "customers", "customer_id"),
		fkRow("fk_items_product", "order_items", "product_id", "public", "products", "product_id"),
		fkRow("fk_audit_user", "audit", "user_id", "sales", "users", "id"),
		fkRow("fk_coupon_owner", "coupons", "owner_code", "public", "customers", "customer_code"),
	}
	inScope := map[string]bool{"public": true}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("wishlist"),                          // child excluded (bare form)
		filterpkg.ExcludeTable("public.products"),                   // parent excluded (qualified form)
		filterpkg.ExcludeTable("public.customers", "customer_code"), // referenced column excluded
	}}
	keep := func(r pgInventoryRow) bool { return pgKeepForeignKey(f, inScope, r) }
	// fk_audit_user references a schema outside the inspection, which the dump drops too.
	if got, want := pgKeptNames(rows, keep), []string{"public.fk_items_order"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// No filter, every schema in scope: everything stays.
	all := func(r pgInventoryRow) bool {
		return pgKeepForeignKey(nil, map[string]bool{"public": true, "sales": true}, r)
	}
	if got := pgKeptNames(rows, all); len(got) != len(rows) {
		t.Errorf("no filter kept %d of %d", len(got), len(rows))
	}
}

func TestPGKeepRoutine(t *testing.T) {
	rows := []pgInventoryRow{
		{"name": "public.sp_pay(p_id integer)", "schema": "public", "obj": "sp_pay", "args": "p_id integer"},
		{"name": "public.sp_pay(p_id integer, p_note text)", "schema": "public", "obj": "sp_pay", "args": "p_id integer, p_note text"},
	}
	cases := []struct {
		rule string
		keep []bool
	}{
		{"sp_pay", []bool{false, false}},
		{"public.sp_pay", []bool{false, false}},
		{"sales.sp_pay", []bool{true, true}},
		// The inventory's own spelling takes that one overload, and the IN
		// mode PostgreSQL 14 prints does not stand in the way.
		{"public.sp_pay(p_id integer)", []bool{false, true}},
		{"public.sp_pay(IN p_id integer, IN p_note text)", []bool{true, false}},
	}
	for _, c := range cases {
		f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
			filterpkg.ExcludeObject(filterpkg.ObjectKindProcedure, c.rule)}}
		for i, row := range rows {
			if got := pgKeepRoutine(f, filterpkg.ObjectKindProcedure, row); got != c.keep[i] {
				t.Errorf("rule %q on %s: keep = %v, want %v", c.rule, row["name"], got, c.keep[i])
			}
		}
		if pgKeepRoutine(f, filterpkg.ObjectKindFunction, rows[0]) != true {
			t.Errorf("a procedure rule %q must not take a function", c.rule)
		}
	}
}

func TestPGKeepTableObjectsTriggersRules(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("wishlist"),
		filterpkg.ExcludeTable("v_hidden"),
		filterpkg.ExcludeObject(filterpkg.ObjectKindView, "public.v_low_stock"),
		filterpkg.ExcludeObject(filterpkg.ObjectKindRule, "r_named"),
	}}
	views := []pgInventoryRow{
		{"name": "public.v_low_stock", "schema": "public", "obj": "v_low_stock"},
		{"name": "public.v_hidden", "schema": "public", "obj": "v_hidden"},
		{"name": "public.v_kept", "schema": "public", "obj": "v_kept"},
	}
	keepView := func(r pgInventoryRow) bool { return pgKeepTableObject(f, filterpkg.ObjectKindView, r) }
	if got, want := pgKeptNames(views, keepView), []string{"public.v_kept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("views: got %v, want %v", got, want)
	}

	triggers := []pgInventoryRow{
		{"name": "public.trg_a", "obj": "trg_a", "tbl_schema": "public", "tbl": "wishlist"},
		{"name": "public.trg_b", "obj": "trg_b", "tbl_schema": "public", "tbl": "orders"},
	}
	keepTrigger := func(r pgInventoryRow) bool { return pgKeepTrigger(f, r) }
	if got, want := pgKeptNames(triggers, keepTrigger), []string{"public.trg_b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("triggers: got %v, want %v", got, want)
	}

	rules := []pgInventoryRow{
		{"name": "public.r_on_wishlist", "schema": "public", "obj": "r_on_wishlist", "tbl_schema": "public", "tbl": "wishlist"},
		{"name": "public.r_named", "schema": "public", "obj": "r_named", "tbl_schema": "public", "tbl": "orders"},
		{"name": "public.r_kept", "schema": "public", "obj": "r_kept", "tbl_schema": "public", "tbl": "orders"},
	}
	keepRule := func(r pgInventoryRow) bool { return pgKeepRule(f, r) }
	if got, want := pgKeptNames(rules, keepRule), []string{"public.r_kept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rules: got %v, want %v", got, want)
	}
}
