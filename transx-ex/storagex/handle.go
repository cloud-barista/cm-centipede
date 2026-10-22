package storagex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// MigrationStatus represents the lifecycle state of a migration.
// It is core.Status, shared with the DBMS domain.
type MigrationStatus = core.Status

const (
	StatusPending   = core.StatusPending
	StatusRunning   = core.StatusRunning
	StatusDone      = core.StatusDone
	StatusFailed    = core.StatusFailed
	StatusCancelled = core.StatusCancelled
)

// MigrationProgress holds real-time progress information for an ongoing migration.
type MigrationProgress struct {
	Status           MigrationStatus `json:"status"`
	StartedAt        time.Time       `json:"startedAt"`
	ElapsedSec       float64         `json:"elapsedSec"`
	BytesTransferred int64           `json:"bytesTransferred"`
	FilesTransferred int             `json:"filesTransferred"`
	CurrentFile      string          `json:"currentFile"`
	// Step is the pipeline step the counts above belong to, and Steps how many
	// the pipeline has: 1/2 is a relay's staging leg, 2/2 its sending leg. Zero
	// until the first observation.
	Step       int    `json:"step,omitempty"`
	Steps      int    `json:"steps,omitempty"`
	ErrMessage string `json:"errMessage,omitempty"`
}

// The accessors below satisfy core.Progress, which is how core.Handle keeps
// the fields common to every domain up to date.

func (p *MigrationProgress) GetStatus() core.Status     { return p.Status }
func (p *MigrationProgress) SetStatus(s core.Status)    { p.Status = s }
func (p *MigrationProgress) SetStartedAt(t time.Time)   { p.StartedAt = t }
func (p *MigrationProgress) SetElapsed(seconds float64) { p.ElapsedSec = seconds }
func (p *MigrationProgress) SetError(msg string)        { p.ErrMessage = msg }

// MigrationHandle provides async control over a running migration.
// Obtain one via MigrateDataAsync; poll with Status()/Progress(); block with Wait().
//
// The embedded core.Handle supplies the lifecycle — Status, Progress, Wait,
// Cancel — shared with the DBMS domain.
type MigrationHandle struct {
	*core.Handle[MigrationProgress, *MigrationProgress]
}

// newMigrationHandle constructs a handle in StatusPending state.
func newMigrationHandle(ctx context.Context, cancel context.CancelFunc) *MigrationHandle {
	return &MigrationHandle{core.NewHandle[MigrationProgress, *MigrationProgress](ctx, cancel)}
}

// observe folds one observation from inside the transfer into the progress a
// caller polls.
//
// The counts are PER STEP. A relay moves the same data twice — source to
// staging, then staging to destination — and each step counts from zero, so
// adding the steps reports double what was migrated. A high-water mark over the
// steps avoided that, but froze the figure at the staging total for the whole
// sending leg, while CurrentFile went on moving: a finished-looking count over a
// transfer still half to go. So a new step starts the counts again from zero,
// and Step/Steps say which leg they describe.
//
// Within a step the counts are a high-water mark, so they never go backwards
// except at a step boundary. The figure a finished transfer leaves is the last
// step's, which for a relay is what reached the destination.
//
// An unknown figure arrives as -1 and loses the comparison, which is what keeps
// it from being read as zero. Its one imprecision: a last step that reports
// nothing but unknowns leaves 0 rather than -1.
func (h *MigrationHandle) observe(u core.Update) {
	h.Update(func(p *MigrationProgress) {
		if u.Step != p.Step {
			p.Step = u.Step
			p.BytesTransferred = 0
			p.FilesTransferred = 0
		}
		p.Steps = u.Steps
		if u.Item != "" {
			p.CurrentFile = u.Item
		}
		if u.Bytes > p.BytesTransferred {
			p.BytesTransferred = u.Bytes
		}
		if int(u.Files) > p.FilesTransferred {
			p.FilesTransferred = int(u.Files)
		}
	})
}

// run executes the full migration described by dmm:
//  1. validation, before anything runs — it only inspects fields, so a bad
//     model fails here without having touched either side
//  2. Source.PreCmd, if set (e.g., a backup into the directory about to move)
//  3. the transfer
//  4. Destination.PostCmd, if set (e.g., a restore from what just arrived)
//
// A failure is returned as a *MigrationError naming the stage it stopped at. A
// cancellation is returned as the bare context error, and skips every step
// after it; a command already running is not interrupted, so it takes effect
// once that command returns.
//
// It is launched as a goroutine by MigrateDataAsync, which calls Finish when it
// returns.
func (h *MigrationHandle) run(dmm DataMigrationModel) error {
	h.SetStatus(StatusRunning)

	if err := Validate(dmm); err != nil {
		return h.fail(&MigrationError{Stage: StageTransfer, Err: fmt.Errorf("validation failed: %w", err)})
	}

	if strings.TrimSpace(dmm.Source.PreCmd) != "" {
		if err := executePreCommand(dmm.Source); err != nil {
			return h.fail(&MigrationError{Stage: StageBackup, Err: err})
		}
	}
	if err := h.cancelled(); err != nil {
		return err
	}

	pipeline, err := Plan(dmm)
	if err != nil {
		return h.fail(&MigrationError{Stage: StageTransfer, Err: fmt.Errorf("planning failed: %w", err)})
	}

	ctx := core.WithReporter(h.Context(), core.ReporterFunc(h.observe))
	if err := pipeline.Execute(ctx); err != nil {
		if cerr := h.cancelled(); cerr != nil {
			return cerr
		}
		return h.fail(&MigrationError{Stage: StageTransfer, Err: err})
	}
	if err := h.cancelled(); err != nil {
		return err
	}

	if strings.TrimSpace(dmm.Destination.PostCmd) != "" {
		if err := executePostCommand(dmm.Destination); err != nil {
			return h.fail(&MigrationError{Stage: StageRestore, Err: err})
		}
	}

	h.SetStatus(StatusDone)
	return nil
}

// fail records err as the migration's failure and returns it.
func (h *MigrationHandle) fail(err error) error {
	h.Fail(err)
	return err
}

// cancelled reports whether the migration was cancelled. If it was, it records
// StatusCancelled and returns the context error unwrapped, so callers can still
// match it with errors.Is(err, context.Canceled).
func (h *MigrationHandle) cancelled() error {
	ctxErr := h.Context().Err()
	if ctxErr == nil {
		return nil
	}
	h.SetStatus(StatusCancelled)
	h.SetErr(ctxErr)
	return ctxErr
}
