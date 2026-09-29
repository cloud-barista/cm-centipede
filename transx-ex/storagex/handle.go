package storagex

import (
	"context"
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
	ErrMessage       string          `json:"errMessage,omitempty"`
}

// The accessors below satisfy core.Progress, which is how core.Handle keeps
// the fields common to every domain up to date.

func (p *MigrationProgress) GetStatus() core.Status     { return p.Status }
func (p *MigrationProgress) SetStatus(s core.Status)    { p.Status = s }
func (p *MigrationProgress) SetStartedAt(t time.Time)   { p.StartedAt = t }
func (p *MigrationProgress) SetElapsed(seconds float64) { p.ElapsedSec = seconds }
func (p *MigrationProgress) SetError(msg string)        { p.ErrMessage = msg }

// MigrationHandle provides async control over a running migration.
// Obtain one via TransferAsync or MigrateDataAsync; poll with Status()/Progress(); block with Wait().
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
// The counts are a HIGH-WATER MARK, which is neither of the two obvious rules. A
// relay moves the same data twice — source to staging, then staging to
// destination — and each step counts from zero, so adding the steps reports
// double what was migrated, while taking only the last leaves the whole staging
// half reading zero. On a default filesystem transfer that half is most of the
// wait, and a screen that shows nothing through it is the reason this is not
// "last step wins".
//
// The mark never decreases and never exceeds what one leg moved, so it is a true
// statement throughout: that many bytes have been moved. Its one imprecision is
// a relay whose second leg moves less than its first, which is a failed transfer
// either way.
//
// An unknown figure arrives as -1 and loses the comparison, which is what keeps
// it from being read as zero.
func (h *MigrationHandle) observe(u core.Update) {
	h.Update(func(p *MigrationProgress) {
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

// run executes the full migration described by dmm.
// It is launched as a goroutine by TransferAsync/MigrateDataAsync, which calls
// Finish when it returns.
func (h *MigrationHandle) run(dmm DataMigrationModel) error {
	h.SetStatus(StatusRunning)

	pipeline, err := Plan(dmm)
	if err != nil {
		h.Fail(err)
		return err
	}

	ctx := core.WithReporter(h.Context(), core.ReporterFunc(h.observe))
	if err := pipeline.Execute(ctx); err != nil {
		if ctxErr := h.Context().Err(); ctxErr != nil {
			h.SetStatus(StatusCancelled)
			h.SetErr(ctxErr)
			return ctxErr
		}
		h.Fail(err)
		return err
	}

	h.SetStatus(StatusDone)
	return nil
}
