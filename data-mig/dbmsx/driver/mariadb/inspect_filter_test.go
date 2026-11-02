package mariadb

import (
	"reflect"
	"testing"

	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

// shopFKs are rows of mysqlForeignKeyDetailQuery: name, table, referenced
// table, referenced columns.
var shopFKs = [][]string{
	{"fk_items_order", "order_items", "orders", "order_id"},
	{"fk_items_product", "order_items", "products", "product_id"},
	{"fk_wishlist_customer", "wishlist", "customers", "customer_id"},
	{"fk_wishlist_product", "wishlist", "products", "product_id"},
	{"fk_coupon_owner", "coupons", "customers", "customer_code"},
}

func TestMariaDBKeepForeignKeyRows(t *testing.T) {
	all := []string{"fk_items_order", "fk_items_product", "fk_wishlist_customer", "fk_wishlist_product", "fk_coupon_owner"}
	cases := []struct {
		name  string
		rules []filterpkg.FilterRule
		want  []string
	}{
		{"no filter keeps every key", nil, all},
		{"an excluded table takes its own keys",
			[]filterpkg.FilterRule{filterpkg.ExcludeTable("wishlist")},
			[]string{"fk_items_order", "fk_items_product", "fk_coupon_owner"}},
		{"an excluded parent takes the keys referencing it",
			[]filterpkg.FilterRule{filterpkg.ExcludeTable("products")},
			[]string{"fk_items_order", "fk_wishlist_customer", "fk_coupon_owner"}},
		{"an excluded referenced column takes the key",
			[]filterpkg.FilterRule{filterpkg.ExcludeTable("customers", "customer_code")},
			[]string{"fk_items_order", "fk_items_product", "fk_wishlist_customer", "fk_wishlist_product"}},
		{"an excluded other column leaves the key",
			[]filterpkg.FilterRule{filterpkg.ExcludeTable("customers", "phone")}, all},
		{"a key named by a rule",
			[]filterpkg.FilterRule{filterpkg.ExcludeObject(filterpkg.ObjectKindForeignKey, "fk_items_order")},
			[]string{"fk_items_product", "fk_wishlist_customer", "fk_wishlist_product", "fk_coupon_owner"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f *filterpkg.DBMSFilterOption
			if c.rules != nil {
				f = &filterpkg.DBMSFilterOption{Rules: c.rules}
			}
			if got := mariadbKeepForeignKeyRows(f, shopFKs); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestMariaDBKeepForeignKeyRowsSkipsShortRows(t *testing.T) {
	if got := mariadbKeepForeignKeyRows(nil, [][]string{{"fk_x", "t"}}); len(got) != 0 {
		t.Errorf("a row without its referenced side must be skipped, got %v", got)
	}
}

func TestMariaDBKeepTriggerRows(t *testing.T) {
	rows := [][]string{{"trg_a", "orders"}, {"trg_b", "wishlist"}, {"trg_c", "orders"}}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("wishlist"),
		filterpkg.ExcludeObject(filterpkg.ObjectKindTrigger, "trg_c"),
	}}
	if got, want := mariadbKeepTriggerRows(f, rows), []string{"trg_a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
