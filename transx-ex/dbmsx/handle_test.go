package dbmsx

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Test Helpers
// =============================================================================

func newTestHandle() *MigrationHandle {
	ctx, cancel := context.WithCancel(context.Background())
	return newMigrationHandle(ctx, cancel)
}

// nopCloser wraps an io.Writer to satisfy io.WriteCloser.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// =============================================================================
// Status
// =============================================================================

func TestHandle_InitialStatus_IsPending(t *testing.T) {
	h := newTestHandle()
	if h.Status() != StatusPending {
		t.Errorf("Status() = %q, want %q", h.Status(), StatusPending)
	}
}

func TestHandle_Status_ReflectsMutation(t *testing.T) {
	h := newTestHandle()
	h.updateProgress(func(p *MigrationProgress) { p.Status = StatusDumping })
	if h.Status() != StatusDumping {
		t.Errorf("Status() = %q, want %q", h.Status(), StatusDumping)
	}
}

// =============================================================================
// Progress
// =============================================================================

func TestHandle_Progress_InitialStatus_IsPending(t *testing.T) {
	h := newTestHandle()
	if h.Progress().Status != StatusPending {
		t.Errorf("Progress().Status = %q, want %q", h.Progress().Status, StatusPending)
	}
}

func TestHandle_Progress_SnapshotImmutability(t *testing.T) {
	h := newTestHandle()
	snap := h.Progress()

	// Mutate internal state after taking snapshot.
	h.updateProgress(func(p *MigrationProgress) {
		p.Status = StatusDumping
		p.BytesTransferred = 1234
	})

	// Snapshot must not reflect the mutation.
	if snap.Status != StatusPending {
		t.Errorf("snapshot Status changed: got %q, want %q", snap.Status, StatusPending)
	}
	if snap.BytesTransferred != 0 {
		t.Errorf("snapshot BytesTransferred changed: got %d, want 0", snap.BytesTransferred)
	}
}

func TestHandle_Progress_ElapsedSec_NonNegative(t *testing.T) {
	h := newTestHandle()
	if p := h.Progress(); p.ElapsedSec < 0 {
		t.Errorf("ElapsedSec = %f, want >= 0", p.ElapsedSec)
	}
}

func TestHandle_Progress_StartedAt_NonZero(t *testing.T) {
	h := newTestHandle()
	if h.Progress().StartedAt.IsZero() {
		t.Error("StartedAt should be non-zero")
	}
}

// =============================================================================
// Wait
// =============================================================================

func TestHandle_Wait_ReturnsAfterDoneClose(t *testing.T) {
	h := newTestHandle()

	result := make(chan error, 1)
	go func() { result <- h.Wait() }()

	h.Finish()

	select {
	case err := <-result:
		if err != nil {
			t.Errorf("Wait() = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not return after done channel was closed")
	}
}

func TestHandle_Wait_ReturnsStoredError(t *testing.T) {
	h := newTestHandle()
	h.SetErr(io.ErrUnexpectedEOF)
	h.Finish()

	if err := h.Wait(); err != io.ErrUnexpectedEOF {
		t.Errorf("Wait() = %v, want %v", err, io.ErrUnexpectedEOF)
	}
}

// =============================================================================
// Cancel
// =============================================================================

func TestHandle_Cancel_PropagatesContext(t *testing.T) {
	h := newTestHandle()
	h.Cancel()

	select {
	case <-h.Context().Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("ctx.Done() not closed after Cancel()")
	}
	if h.Context().Err() != context.Canceled {
		t.Errorf("ctx.Err() = %v, want context.Canceled", h.Context().Err())
	}
}

// =============================================================================
// updateProgress — concurrency safety (run with -race)
// =============================================================================

func TestHandle_UpdateProgress_ConcurrentSafe(t *testing.T) {
	h := newTestHandle()

	const goroutines = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.updateProgress(func(p *MigrationProgress) {
				p.BytesTransferred++
			})
		}()
	}
	wg.Wait()

	total := h.Progress().BytesTransferred

	if total != goroutines {
		t.Errorf("BytesTransferred = %d after %d concurrent increments, want %d",
			total, goroutines, goroutines)
	}
}

// =============================================================================
// setFailed
// =============================================================================

func TestHandle_SetFailed_SetsStatusAndMessage(t *testing.T) {
	h := newTestHandle()
	h.setFailed(io.ErrUnexpectedEOF)

	if h.Status() != StatusFailed {
		t.Errorf("Status() = %q, want %q", h.Status(), StatusFailed)
	}
	if h.Progress().ErrMessage == "" {
		t.Error("ErrMessage should be non-empty after setFailed")
	}
	if h.Err() != io.ErrUnexpectedEOF {
		t.Errorf("Err() = %v, want %v", h.Err(), io.ErrUnexpectedEOF)
	}
}

// =============================================================================
// countingWriter
// =============================================================================

func TestCountingWriter_Write_NotifiesDelta(t *testing.T) {
	var total int64
	cw := &countingWriter{
		w:      nopCloser{io.Discard},
		notify: func(delta int64) { total += delta },
	}

	data := []byte("hello")
	n, err := cw.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d, want %d", n, len(data))
	}
	if total != int64(len(data)) {
		t.Errorf("total = %d, want %d", total, len(data))
	}
}

func TestCountingWriter_Write_Accumulates(t *testing.T) {
	var total int64
	cw := &countingWriter{
		w:      nopCloser{io.Discard},
		notify: func(delta int64) { total += delta },
	}

	cw.Write([]byte("hello"))  // 5 bytes
	cw.Write([]byte("world!")) // 6 bytes

	if total != 11 {
		t.Errorf("accumulated total = %d, want 11", total)
	}
}

func TestCountingWriter_Write_ZeroBytesNoNotify(t *testing.T) {
	notifyCalled := false
	cw := &countingWriter{
		w:      nopCloser{io.Discard},
		notify: func(_ int64) { notifyCalled = true },
	}

	cw.Write([]byte{})
	if notifyCalled {
		t.Error("notify should not be called for zero-byte writes")
	}
}

// =============================================================================
// newTableNotify
// =============================================================================

func TestHandle_TableNotify_IncrementsAndUpdates(t *testing.T) {
	h := newTestHandle()
	notify := h.newTableNotify()

	notify("users")
	notify("orders")

	p := h.Progress()
	if p.TablesDone != 2 {
		t.Errorf("TablesDone = %d, want 2", p.TablesDone)
	}
	if p.CurrentTable != "orders" {
		t.Errorf("CurrentTable = %q, want %q", p.CurrentTable, "orders")
	}
}

// =============================================================================
// fullSchemaMetric
// =============================================================================

func TestFullSchemaMetric(t *testing.T) {
	m := fullSchemaMetric(DBMSTypeMySQL)
	if m == nil || m.MySQL == nil {
		t.Fatal("mysql: expected MySQL metric")
	}
	if m.MySQL.Views == nil || !*m.MySQL.Views || m.MySQL.Procedures == nil || !*m.MySQL.Procedures {
		t.Error("mysql: schema-object flags should be true")
	}
	if m.MySQL.Columns != nil || m.MySQL.RowCountExact != nil {
		t.Error("mysql: per-table flags should be unset (teardown does not need them)")
	}

	if m := fullSchemaMetric(DBMSTypeMariaDB); m == nil || m.MariaDB == nil {
		t.Error("mariadb: expected MariaDB metric")
	}
	pm := fullSchemaMetric(DBMSTypePostgreSQL)
	if pm == nil || pm.PostgreSQL == nil {
		t.Fatal("postgresql: expected PostgreSQL metric")
	}
	if pm.PostgreSQL.Sequences == nil || !*pm.PostgreSQL.Sequences ||
		pm.PostgreSQL.Types == nil || !*pm.PostgreSQL.Types {
		t.Error("postgresql: sequences/types flags should be true")
	}
	if m := fullSchemaMetric(DBMSTypeMongoDB); m != nil {
		t.Errorf("mongodb: expected nil metric, got %+v", m)
	}
}

// =============================================================================
// buildTeardownScript
// =============================================================================

func TestBuildTeardownScript_MySQL(t *testing.T) {
	info := &DBMSInfo{
		Tables:     []TableInfo{{Name: "users"}, {Name: "orders"}},
		Views:      []string{"active_users"},
		Functions:  []string{"user_count"},
		Procedures: []string{"add_user"},
		Triggers:   []string{"trg_orders_ai"},
		Events:     []string{"ev_noop"},
	}
	script := buildTeardownScript(DBMSTypeMySQL, info)

	for _, want := range []string{
		"SET FOREIGN_KEY_CHECKS=0;",
		"DROP TRIGGER IF EXISTS `trg_orders_ai`;",
		"DROP VIEW IF EXISTS `active_users`;",
		"DROP TABLE IF EXISTS `users`;",
		"DROP TABLE IF EXISTS `orders`;",
		"DROP PROCEDURE IF EXISTS `add_user`;",
		"DROP FUNCTION IF EXISTS `user_count`;",
		"DROP EVENT IF EXISTS `ev_noop`;",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n---\n%s", want, script)
		}
	}
}

// The PostgreSQL inventories reach buildTeardownScript already quoted by the
// server's quote_ident, which leaves an ordinary lower-case name bare and
// prefixes it with its schema. The fixtures below are in that form; the script
// must emit them verbatim, since quoting sales.v_monthly again would produce the
// single identifier "sales.v_monthly", which names nothing.
func TestBuildTeardownScript_PostgreSQL(t *testing.T) {
	info := &DBMSInfo{
		PgSchema:          []PgSchemaInfo{{Name: "public"}},
		Tables:            []TableInfo{{PgSchema: "public", Name: "orders"}},
		Views:             []string{"public.active_users"},
		MaterializedViews: []string{"public.mv_user_count"},
		Functions:         []string{"public.user_count()", "public.user_count(integer)"},
		Procedures:        []string{"public.add_user(text, integer)"},
		Sequences:         []string{"public.seq_demo"},
		Types:             []string{"public.mood"},
		Extensions:        []string{"pgcrypto"},
	}
	script := buildTeardownScript(DBMSTypePostgreSQL, info)

	for _, want := range []string{
		`DROP TABLE IF EXISTS "public"."orders" CASCADE;`,
		`DROP MATERIALIZED VIEW IF EXISTS public.mv_user_count CASCADE;`,
		`DROP VIEW IF EXISTS public.active_users CASCADE;`,
		`DROP SEQUENCE IF EXISTS public.seq_demo CASCADE;`,
		`DROP TYPE IF EXISTS public.mood CASCADE;`,
		`DROP EXTENSION IF EXISTS pgcrypto CASCADE;`,
		`DROP FUNCTION IF EXISTS public.user_count() CASCADE;`,
		`DROP FUNCTION IF EXISTS public.user_count(integer) CASCADE;`,
		`DROP PROCEDURE IF EXISTS public.add_user(text, integer) CASCADE;`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n---\n%s", want, script)
		}
	}
	if strings.Contains(script, "FOREIGN_KEY_CHECKS") {
		t.Error("PostgreSQL script must not use FOREIGN_KEY_CHECKS")
	}
	// Every database ships with "public" and the drops above already emptied it,
	// so it must survive the teardown.
	if strings.Contains(script, "DROP SCHEMA") {
		t.Errorf("public must not be dropped\n---\n%s", script)
	}
}

func TestBuildTeardownScript_PostgreSQL_MultipleSchemas(t *testing.T) {
	info := &DBMSInfo{
		PgSchema: []PgSchemaInfo{{Name: "public"}, {Name: "sales"}, {Name: "hr"}},
		Tables: []TableInfo{
			{PgSchema: "public", Name: "users"},
			{PgSchema: "sales", Name: "users"},
		},
		Views: []string{"sales.v_monthly"},
	}
	script := buildTeardownScript(DBMSTypePostgreSQL, info)

	// Two tables of the same name in different schemas must produce two
	// distinct drops; an unqualified script would emit the same statement twice
	// and leave one table standing.
	for _, want := range []string{
		`DROP TABLE IF EXISTS "public"."users" CASCADE;`,
		`DROP TABLE IF EXISTS "sales"."users" CASCADE;`,
		`DROP VIEW IF EXISTS sales.v_monthly CASCADE;`,
		// The migration created these schemas, so they go with their contents.
		`DROP SCHEMA IF EXISTS "sales" CASCADE;`,
		`DROP SCHEMA IF EXISTS "hr" CASCADE;`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n---\n%s", want, script)
		}
	}
	if strings.Contains(script, `DROP SCHEMA IF EXISTS "public"`) {
		t.Errorf("public must not be dropped\n---\n%s", script)
	}
}

func TestBuildTeardownScript_Empty(t *testing.T) {
	if s := buildTeardownScript(DBMSTypeMySQL, &DBMSInfo{}); s != "" {
		t.Errorf("empty MySQL info should yield empty script, got %q", s)
	}
	if s := buildTeardownScript(DBMSTypePostgreSQL, &DBMSInfo{}); s != "" {
		t.Errorf("empty PostgreSQL info should yield empty script, got %q", s)
	}
}
