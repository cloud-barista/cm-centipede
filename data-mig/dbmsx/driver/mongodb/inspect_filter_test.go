package mongodb

import (
	"reflect"
	"testing"

	base "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

func TestMongoFilterSchemaObjects(t *testing.T) {
	info := &base.DBMSInfo{
		Views:      []string{"v_orders", "v_wishlist", "v_stock"},
		Validators: []string{"orders", "wishlist", "coupons"},
	}
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{
		filterpkg.ExcludeTable("wishlist"),
		filterpkg.ExcludeTable("v_wishlist"),
		filterpkg.ExcludeObject(filterpkg.ObjectKindView, "v_stock"),
		filterpkg.ExcludeObject(filterpkg.ObjectKindValidator, "coupons"),
	}}
	mongoFilterSchemaObjects(f, info)
	if want := []string{"v_orders"}; !reflect.DeepEqual(info.Views, want) {
		t.Errorf("views = %v, want %v", info.Views, want)
	}
	if want := []string{"orders"}; !reflect.DeepEqual(info.Validators, want) {
		t.Errorf("validators = %v, want %v", info.Validators, want)
	}
}

func TestMongoKeepNamedLeavesUncollectedNil(t *testing.T) {
	f := &filterpkg.DBMSFilterOption{Rules: []filterpkg.FilterRule{filterpkg.ExcludeTable("x")}}
	if got := mongoKeepNamed(f, filterpkg.ObjectKindView, nil); got != nil {
		t.Errorf("a list that was not collected must stay nil, got %v", got)
	}
}
