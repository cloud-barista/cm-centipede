package dbmsx

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// sentinel error for Unwrap / errors.Is tests
var errSentinel = errors.New("sentinel")

// --- MigrationError ---

func TestMigrationError_Error_Format(t *testing.T) {
	e := &MigrationError{Stage: StageDump, Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, StageDump) {
		t.Errorf("expected stage %q in message, got: %s", StageDump, msg)
	}
	if !strings.Contains(msg, errSentinel.Error()) {
		t.Errorf("expected underlying error in message, got: %s", msg)
	}
}

func TestMigrationError_Unwrap_ErrorsIs(t *testing.T) {
	e := &MigrationError{Stage: StageRestore, Err: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should traverse Unwrap to find sentinel")
	}
}

// --- OperationError ---

func TestOperationError_Error_Dump(t *testing.T) {
	e := &OperationError{Operation: OperationDump, DBMSType: DBMSTypeMySQL, Source: "127.0.0.1:3306", Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "dump") {
		t.Errorf("dump message should contain 'dump', got: %s", msg)
	}
	if !strings.Contains(msg, DBMSTypeMySQL) {
		t.Errorf("dump message should contain dbmsType, got: %s", msg)
	}
}

func TestOperationError_Error_Restore(t *testing.T) {
	e := &OperationError{Operation: OperationRestore, DBMSType: DBMSTypePostgreSQL, Destination: "pg-host:5432", Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "restore") {
		t.Errorf("restore message should contain 'restore', got: %s", msg)
	}
}

func TestOperationError_Error_Connect(t *testing.T) {
	e := &OperationError{Operation: OperationConnect, DBMSType: DBMSTypeMongoDB, Source: "mongo:27017", Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "connect") {
		t.Errorf("connect message should contain 'connect', got: %s", msg)
	}
}

func TestOperationError_Error_Rollback(t *testing.T) {
	e := &OperationError{Operation: OperationRollback, DBMSType: DBMSTypeMariaDB, Destination: "dst-host", Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "rollback") {
		t.Errorf("rollback message should contain 'rollback', got: %s", msg)
	}
}

func TestOperationError_Error_Unknown(t *testing.T) {
	e := &OperationError{Operation: "inspect", DBMSType: DBMSTypeMySQL, Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "inspect") {
		t.Errorf("unknown operation message should contain operation name, got: %s", msg)
	}
}

func TestOperationError_Unwrap_ErrorsIs(t *testing.T) {
	e := &OperationError{Operation: OperationConnect, Err: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should traverse Unwrap to find sentinel")
	}
}

// --- UnsupportedDBMSError ---

func TestUnsupportedDBMSError_Error_ContainsType(t *testing.T) {
	e := &UnsupportedDBMSError{DBMSType: "oracle"}
	msg := e.Error()
	if !strings.Contains(msg, "oracle") {
		t.Errorf("expected dbmsType in message, got: %s", msg)
	}
}

// --- TargetNotEmptyError ---

func TestTargetNotEmptyError_Error_ContainsFields(t *testing.T) {
	e := &TargetNotEmptyError{DBMSType: DBMSTypeMySQL, Database: "proddb", TableCount: 42}
	msg := e.Error()
	if !strings.Contains(msg, "proddb") {
		t.Errorf("expected database name in message, got: %s", msg)
	}
	if !strings.Contains(msg, "42") {
		t.Errorf("expected tableCount in message, got: %s", msg)
	}
}

// --- InspectError ---

func TestInspectError_Error_ContainsFields(t *testing.T) {
	e := InspectError{Database: "proddb", Message: errSentinel.Error(), Err: errSentinel}
	msg := e.Error()
	if !strings.Contains(msg, "proddb") {
		t.Errorf("expected database name in message, got: %s", msg)
	}
	if !strings.Contains(msg, errSentinel.Error()) {
		t.Errorf("expected underlying message, got: %s", msg)
	}
}

func TestInspectError_Unwrap_ErrorsIs(t *testing.T) {
	e := InspectError{Database: "proddb", Message: errSentinel.Error(), Err: errSentinel}
	if !errors.Is(e, errSentinel) {
		t.Error("errors.Is should traverse Unwrap to find sentinel")
	}
}

func TestInspectError_JSON_OmitsWrappedError(t *testing.T) {
	// The skipped list is served over HTTP as-is, so Message must be what lands
	// in the payload and Err must stay out of it — a plain error does not marshal.
	b, err := json.Marshal(InspectError{Database: "proddb", Message: "denied", Err: errSentinel})
	if err != nil {
		t.Fatalf("InspectError should marshal to JSON: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"database":"proddb"`) || !strings.Contains(got, `"error":"denied"`) {
		t.Errorf("unexpected JSON: %s", got)
	}
	if strings.Contains(got, "sentinel") {
		t.Errorf("wrapped error should not be marshalled, got: %s", got)
	}
}

// --- errors.As extraction ---

func TestErrorsAs_MigrationError(t *testing.T) {
	wrapped := &MigrationError{Stage: StageDump, Err: errSentinel}
	var target *MigrationError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should extract *MigrationError")
	}
	if target.Stage != StageDump {
		t.Errorf("extracted Stage = %q, want %q", target.Stage, StageDump)
	}
}

func TestErrorsAs_OperationError(t *testing.T) {
	inner := &OperationError{Operation: OperationDump, Err: errSentinel}
	outer := &MigrationError{Stage: StageDump, Err: inner}

	var opErr *OperationError
	if !errors.As(outer, &opErr) {
		t.Fatal("errors.As should traverse chain to find *OperationError")
	}
	if opErr.Operation != OperationDump {
		t.Errorf("extracted Operation = %q, want %q", opErr.Operation, OperationDump)
	}
}

func TestErrorsAs_TargetNotEmptyError(t *testing.T) {
	e := &TargetNotEmptyError{DBMSType: DBMSTypeMongoDB, Database: "db", TableCount: 3}
	var target *TargetNotEmptyError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should extract *TargetNotEmptyError")
	}
	if target.TableCount != 3 {
		t.Errorf("TableCount = %d, want 3", target.TableCount)
	}
}
