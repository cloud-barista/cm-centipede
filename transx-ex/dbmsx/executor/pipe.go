package executor

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Compile-time Interface Assertion
// =============================================================================

var _ Executor = (*PipeExecutor)(nil)

// =============================================================================
// PipeExecutor
// =============================================================================

// PipeExecutor streams a dump directly from a source SSH host to a restore on
// a destination SSH host using an in-process io.Pipe — no local staging file is
// written.  Both commands must accept/produce a byte stream on stdin/stdout
// (e.g. mysqldump ... | mysql ..., mongodump --archive | mongorestore --archive).
//
// The two goroutines run concurrently:
//   - restore goroutine reads the pipe reader as the restore command's stdin
//   - dump goroutine writes the pipe writer as the dump command's stdout
//
// If dump fails, the pipe writer is closed with the dump error so the restore
// side unblocks promptly.  If restore fails, the pipe reader is closed so the
// dump side stops writing.  dumpErr is checked before restoreErr when both
// goroutines fail.
type PipeExecutor struct {
	dumpCmd    string
	restoreCmd string
	srcSSH     *drvpkg.SSHConfig
	dstSSH     *drvpkg.SSHConfig
}

// NewPipeExecutor constructs a PipeExecutor from pre-built CLI commands and the
// SSH credentials for each host.  The commands are typically produced by
// DBDriver.BuildDumpCmd and DBDriver.BuildRestoreCmd.
func NewPipeExecutor(dumpCmd string, srcSSH *drvpkg.SSHConfig, restoreCmd string, dstSSH *drvpkg.SSHConfig) *PipeExecutor {
	return &PipeExecutor{
		dumpCmd:    dumpCmd,
		restoreCmd: restoreCmd,
		srcSSH:     srcSSH,
		dstSSH:     dstSSH,
	}
}

// Execute runs the dump and restore commands concurrently, piping the dump
// output directly into the restore's stdin.
//
// source and destination are accepted for Executor interface compatibility but
// are not used; all routing is governed by the SSH credentials and commands
// stored in the PipeExecutor.
//
// On failure it returns one of:
//   - *MigrationError{Stage: StageDump}    when the dump command fails
//   - *MigrationError{Stage: StageRestore} when the restore command fails
//
// If both fail (e.g. one caused the other via pipe closure), the dump error
// is returned because it is the root cause.
func (e *PipeExecutor) Execute(ctx context.Context, _, _ drvpkg.DBMSLocation) error {
	logger.Info("pipe execute started",
		slog.String("dumpCmd", e.dumpCmd),
		slog.String("restoreCmd", e.restoreCmd),
	)
	pr, pw := io.Pipe()

	// Every byte of the dump crosses this pipe, which is the only place on this
	// path that sees them: both commands run on remote hosts and report nothing.
	// The raw pw is kept for CloseWithError below — the wrapper counts, it does
	// not own the pipe.
	counted := newPipeCounter(pw, core.ReporterFrom(ctx))

	var (
		wg         sync.WaitGroup
		dumpErr    error
		restoreErr error
	)

	// Cancellation watcher. Neither SSH command takes a context, so a cancel is
	// delivered by tearing down the pipe: both sides then fail their next read
	// or write and unwind. The watcher exits with Execute so it cannot outlive
	// the transfer.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			pr.CloseWithError(ctx.Err())
			pw.CloseWithError(ctx.Err())
		case <-watchDone:
		}
	}()

	// Goroutine 1 — restore: reads from the pipe reader as the command's stdin.
	wg.Add(1)
	go func() {
		defer wg.Done()
		restoreErr = drvpkg.ExecuteViaSSH(e.dstSSH, e.restoreCmd, pr, nil)
		// Close the reader so the dump side unblocks if it is still writing.
		pr.CloseWithError(restoreErr)
	}()

	// Goroutine 2 — dump: writes its stdout into the pipe writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		dumpErr = drvpkg.ExecuteViaSSH(e.srcSSH, e.dumpCmd, nil, counted)
		// Close the writer to signal EOF (or error) to the restore side.
		pw.CloseWithError(dumpErr)
	}()

	wg.Wait()
	counted.flush()

	if dumpErr != nil {
		logger.Error("pipe dump failed", slog.String("err", dumpErr.Error()))
		return &MigrationError{Stage: StageDump, Err: dumpErr}
	}
	if restoreErr != nil {
		logger.Error("pipe restore failed", slog.String("err", restoreErr.Error()))
		return &MigrationError{Stage: StageRestore, Err: restoreErr}
	}
	logger.Info("pipe execute completed")
	return nil
}

// =============================================================================
// pipeCounter
// =============================================================================

// Reporting thresholds. A dump streams in 32 KB writes, and a reporter is called
// on the transfer path — so observations are batched rather than made per write.
// Either limit triggers one: the size keeps a fast dump from reporting thousands
// of times, the interval keeps a slow one from reporting nothing at all.
const (
	pipeReportBytes    = 1 << 20
	pipeReportInterval = 200 * time.Millisecond
)

// pipeCounter counts what passes through it and reports the running total.
//
// It wraps the pipe writer without owning it: Execute still closes the pipe
// itself, because closing is how a failure on one side unblocks the other.
type pipeCounter struct {
	w        io.Writer
	reporter core.Reporter

	mu       sync.Mutex
	total    int64
	reported int64
	lastAt   time.Time
}

func newPipeCounter(w io.Writer, reporter core.Reporter) *pipeCounter {
	return &pipeCounter{w: w, reporter: reporter}
}

// Write implements io.Writer.
func (c *pipeCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.add(int64(n))
	}
	return n, err
}

func (c *pipeCounter) add(n int64) {
	c.mu.Lock()
	c.total += n
	total := c.total
	due := total-c.reported >= pipeReportBytes || time.Since(c.lastAt) >= pipeReportInterval
	if due {
		c.reported = total
		c.lastAt = time.Now()
	}
	c.mu.Unlock()

	if due {
		c.reporter.Report(core.Update{Bytes: total, Files: -1})
	}
}

// flush reports the final total, which the thresholds above may have held back.
func (c *pipeCounter) flush() {
	c.mu.Lock()
	total := c.total
	pending := total != c.reported
	c.reported = total
	c.mu.Unlock()

	if pending {
		c.reporter.Report(core.Update{Bytes: total, Files: -1})
	}
}
