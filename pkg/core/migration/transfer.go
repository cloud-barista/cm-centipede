package migration

import (
	"context"
	"time"

	"github.com/cloud-barista/cm-centipede/data-mig"
)

// runStorageTransfer performs one folder's or bucket's transfer, reporting what
// it has moved on progressCh while it runs, and returns the bytes it moved and
// the concurrency it ran at.
//
// The async handle rather than datamig.MigrateStorage: the synchronous call
// hands back nothing until it returns, so a folder of any size would show no
// progress at all and a cancelled migration would keep transferring. Both follow
// from there being no handle to ask. centipede sets no PreCmd or PostCmd, so the
// migration is the transfer alone.
//
// Bytes are -1 when the transfer could not count them, which is not zero: rsync
// reports its totals only if --stats output parses, and a caller must be able to
// tell "moved nothing" from "moved an unknown amount".
func runStorageTransfer(
	ctx context.Context,
	m datamig.StorageMigrationModel,
	itemPath string,
	progressCh chan<- ProgressEvent,
) (int64, concurrencyReport, error) {
	handle := datamig.MigrateStorageAsync(m)
	finished := func() (int64, concurrencyReport) {
		p := handle.Progress()
		return p.BytesTransferred, concurrencyReport{n: p.Concurrency, note: p.ConcurrencyNote}
	}
	done := make(chan error, 1)
	go func() { done <- handle.Wait() }()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p := handle.Progress()
			progressCh <- ProgressEvent{
				Kind:      EventTick,
				ItemPath:  itemPath,
				Current:   p.CurrentFile,
				Step:      p.Step,
				Steps:     p.Steps,
				SizeBytes: p.BytesTransferred,
			}

		case <-ctx.Done():
			handle.Cancel()
			<-done
			bytes, conc := finished()
			return bytes, conc, ctx.Err()

		case err := <-done:
			bytes, conc := finished()
			return bytes, conc, err
		}
	}
}
