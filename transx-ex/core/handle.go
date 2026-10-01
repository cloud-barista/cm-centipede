package core

import (
	"context"
	"sync"
	"time"
)

// =============================================================================
// Status
// =============================================================================

// Status is the lifecycle state of a migration.
//
// The values below are common to every domain. A domain may declare additional
// states of its own — dbmsx reports checking/dumping/restoring, for instance —
// as further Status constants; nothing here enumerates the set exhaustively.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// =============================================================================
// Progress contract
// =============================================================================

// Progress is what a domain progress struct must support so Handle can maintain
// the three fields every migration reports, whatever else it tracks alongside
// them (files and bytes for storage, tables for a database).
//
// The methods take pointer receivers; Handle reaches them through the PP type
// parameter.
type Progress interface {
	GetStatus() Status
	SetStatus(Status)
	SetStartedAt(time.Time)
	SetElapsed(float64)
	SetError(string)
}

// =============================================================================
// Handle
// =============================================================================

// Handle provides async control over a running migration: poll it with
// Status/Progress, block on Wait, stop it with Cancel.
//
// P is the domain progress struct and PP its pointer type, which is where the
// Progress methods live. Domain packages alias the instantiation so callers
// never write both parameters:
//
//	type MigrationHandle = core.Handle[MigrationProgress, *MigrationProgress]
//
// The goroutine running the migration owns the handle's mutable state: it calls
// Update/SetStatus as work proceeds and Finish exactly once at the end.
type Handle[P any, PP interface {
	*P
	Progress
}] struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.RWMutex
	err      error
	started  time.Time
	progress P
}

// NewHandle constructs a handle in StatusPending, with the progress struct's
// start time already stamped. The caller supplies a cancellable context; the
// goroutine it launches must call Finish when it returns.
func NewHandle[P any, PP interface {
	*P
	Progress
}](ctx context.Context, cancel context.CancelFunc) *Handle[P, PP] {
	h := &Handle[P, PP]{
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		started: time.Now(),
	}
	PP(&h.progress).SetStatus(StatusPending)
	PP(&h.progress).SetStartedAt(h.started)
	return h
}

// Status returns the current lifecycle state. Safe for concurrent use.
//
// It reads the progress struct rather than a field of its own, so a status
// written through Update is reported here too: there is one place the state
// lives.
func (h *Handle[P, PP]) Status() Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return PP(&h.progress).GetStatus()
}

// Progress returns a point-in-time snapshot. ElapsedSec is recomputed on each
// call, so a caller polling a long migration sees time advance.
func (h *Handle[P, PP]) Progress() P {
	h.mu.RLock()
	defer h.mu.RUnlock()
	snap := h.progress
	PP(&snap).SetElapsed(time.Since(h.started).Seconds())
	return snap
}

// Wait blocks until the migration goroutine finishes and returns its error.
func (h *Handle[P, PP]) Wait() error {
	<-h.done
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

// Cancel requests cancellation of the in-progress migration. Use Wait to
// confirm termination.
func (h *Handle[P, PP]) Cancel() { h.cancel() }

// Context returns the migration's context, which Cancel cancels. The running
// goroutine passes it down to the pipeline.
func (h *Handle[P, PP]) Context() context.Context { return h.ctx }

// Err returns the recorded error without blocking. Prefer Wait unless the
// migration is known to have finished.
func (h *Handle[P, PP]) Err() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

// Finish marks the migration complete, unblocking Wait. It is called once by
// the goroutine that ran the migration, and is safe to call more than once.
func (h *Handle[P, PP]) Finish() {
	select {
	case <-h.done:
	default:
		close(h.done)
	}
}

// Update applies fn to the progress struct under the write lock.
func (h *Handle[P, PP]) Update(fn func(PP)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(&h.progress)
}

// SetStatus records a new lifecycle state.
func (h *Handle[P, PP]) SetStatus(s Status) {
	h.mu.Lock()
	defer h.mu.Unlock()
	PP(&h.progress).SetStatus(s)
}

// SetErr records the migration's error without changing its status. Use it for
// terminal states other than failure — a completed rollback, for instance,
// still reports why it happened.
func (h *Handle[P, PP]) SetErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

// Fail transitions to StatusFailed and records err, both as the handle's error
// and as the message in the progress snapshot.
func (h *Handle[P, PP]) Fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
	PP(&h.progress).SetStatus(StatusFailed)
	PP(&h.progress).SetError(err.Error())
}
