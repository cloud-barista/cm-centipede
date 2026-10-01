package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestObjectErrorMessage(t *testing.T) {
	cause := errors.New("Error 1227 (42000): Access denied")

	cases := []struct {
		name string
		err  *ObjectError
		want string
	}{
		{
			// The statement that failed against RDS MySQL 8.4, as the restore
			// loop now reports it.
			name: "restore statement",
			err: &ObjectError{
				Kind: "view", Owner: "centipede_test", Name: "v_customer_stats",
				Action: ActionWriteDDL, Index: 47, Total: 312, Unit: "statement",
				Err: cause,
			},
			want: "create failed on view centipede_test.v_customer_stats (statement 47/312): Error 1227 (42000): Access denied",
		},
		{
			name: "dump object in a batch",
			err: &ObjectError{
				Kind: "view", Owner: "sales", Name: "v_daily",
				Action: ActionReadDDL, Index: 3, Total: 12, Err: cause,
			},
			want: "read DDL failed on view sales.v_daily (3/12): Error 1227 (42000): Access denied",
		},
		{
			name: "no position",
			err: &ObjectError{
				Kind: ObjectKindTable, Owner: "sales", Name: "orders",
				Action: ActionReadRows, Err: cause,
			},
			want: "read rows failed on table sales.orders: Error 1227 (42000): Access denied",
		},
		{
			// Listing fails before any object is known; the kind alone still
			// says what was being enumerated.
			name: "kind only",
			err:  &ObjectError{Kind: "view", Owner: "sales", Action: ActionList, Err: cause},
			want: "list failed on view: Error 1227 (42000): Access denied",
		},
		{
			name: "no owner",
			err: &ObjectError{
				Kind: ObjectKindObject, Name: "raw-data/a.csv",
				Action: ActionUpload, Err: cause,
			},
			want: "upload failed on object raw-data/a.csv: Error 1227 (42000): Access denied",
		},
		{
			name: "index without total",
			err: &ObjectError{
				Kind: ObjectKindTable, Name: "orders",
				Action: ActionReadRows, Index: 5, Err: cause,
			},
			want: "read rows failed on table orders (5): Error 1227 (42000): Access denied",
		},
		{
			// Nothing but a cause must still read as a sentence.
			name: "empty",
			err:  &ObjectError{Err: cause},
			want: "operation failed on object: Error 1227 (42000): Access denied",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.Error(); got != c.want {
				t.Errorf("Error()\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// The object context must survive the stage and step wrappers a pipeline adds,
// so a caller can pull the failed object out of a fully wrapped error.
func TestObjectErrorUnwrapsThroughPipelineWrappers(t *testing.T) {
	cause := errors.New("Error 1227 (42000): Access denied")
	objErr := Obj{
		Kind: "view", Owner: "centipede_test", Name: "v_customer_stats",
		Index: 47, Total: 312, Unit: "statement",
	}.Fail(ActionWriteDDL, cause)

	wrapped := fmt.Errorf("step %d (%s) failed: %w", 2, "restore-target",
		&MigrationError{Stage: StageRestore, Err: objErr})

	var oe *ObjectError
	if !errors.As(wrapped, &oe) {
		t.Fatalf("errors.As did not find *ObjectError in %v", wrapped)
	}
	if oe.Kind != "view" || oe.Name != "v_customer_stats" {
		t.Errorf("got kind %q name %q", oe.Kind, oe.Name)
	}

	var me *MigrationError
	if !errors.As(wrapped, &me) || me.Stage != StageRestore {
		t.Errorf("stage lost: %v", wrapped)
	}
	if !errors.Is(wrapped, cause) {
		t.Errorf("original cause lost: %v", wrapped)
	}
}

// A loop reports success and failure through the same call, so Fail must pass
// a nil error through untouched rather than manufacturing one.
func TestObjFailNil(t *testing.T) {
	if err := (Obj{Kind: ObjectKindTable, Name: "orders"}).Fail(ActionReadRows, nil); err != nil {
		t.Errorf("Fail(nil) = %v, want nil", err)
	}
}
