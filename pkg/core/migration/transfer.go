package migration

import (
	"context"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
)

// runStorageTransfer performs one folder's or bucket's transfer, reporting what
// it has moved on progressCh while it runs, and returns the bytes it moved.
//
// The async handle rather than transxex.TransferStorage: the synchronous call
// hands back nothing until it returns, so a folder of any size showed no progress
// at all and a cancelled migration kept transferring — Transfer runs the pipeline
// on context.Background(). Both follow from there being no handle to ask.
//
// Bytes are -1 when the transfer could not count them, which is not zero: rsync
// reports its totals only if --stats output parses, and a caller must be able to
// tell "moved nothing" from "moved an unknown amount".
func runStorageTransfer(
	ctx context.Context,
	m transxex.StorageMigrationModel,
	itemPath string,
	progressCh chan<- ProgressEvent,
) (int64, error) {
	handle := transxex.TransferStorageAsync(m)
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
				SizeBytes: p.BytesTransferred,
			}

		case <-ctx.Done():
			handle.Cancel()
			<-done
			return handle.Progress().BytesTransferred, ctx.Err()

		case err := <-done:
			return handle.Progress().BytesTransferred, err
		}
	}
}
