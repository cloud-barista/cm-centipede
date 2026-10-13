package base

import (
	"bufio"
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// progress.go reports how far a dump or a restore has got through the Reporter
// carried in ctx (core.ReporterFrom), the path a file transfer's progress
// already takes. The DBDriver interface stays as it is: a driver that reports
// nothing still works, and a caller that listens to nothing pays nothing.
//
// Bytes for a dump are counted by the caller, which owns the file being
// written, so a dump reports only which table it is on. A restore reports all
// of it: the bytes of the dump it has executed, the rows written, and the
// tables finished.

// ReportDumpTable reports that a dump is on table (or collection) done+1 of
// total — done of them finished.
func ReportDumpTable(ctx context.Context, done, total int, table string) {
	core.ReporterFrom(ctx).Report(core.Update{
		Bytes: -1, Files: -1, Item: table, Units: done, UnitsTotal: total,
	})
}

// RestoreProgress follows a restore statement by statement.
//
// A table counts as finished when the restore moves on to the next table's
// data, or when it ends. tablesTotal is the number of tables the dump carries
// data for; one without rows has no data statement, so it is neither counted
// nor finished. A driver that reads its dump as a stream does not know it
// up front and passes 0, leaving CountTotal to find it.
type RestoreProgress struct {
	mu          sync.Mutex
	r           core.Reporter
	bytes, rows int64
	done, total int
	current     string
	statements  int
	counted     chan struct{}
}

// NewRestoreProgress starts following a restore of tablesTotal tables.
func NewRestoreProgress(ctx context.Context, tablesTotal int) *RestoreProgress {
	return &RestoreProgress{r: core.ReporterFrom(ctx), total: tablesTotal}
}

// CountTotal runs count in the background and, when it returns, takes the
// tables it found as the total and the statements it found for Statements.
// The returned stop cancels a count still running; call it when the restore
// returns.
//
// The count reads the dump a second time, and it runs beside the restore
// rather than ahead of it: a dump of 10 GB read whole before the first
// statement was minutes in which the restore reported nothing at all. The
// read is far quicker than the restore it counts for, so the total arrives
// within the first moments of it.
func (p *RestoreProgress) CountTotal(ctx context.Context, count func(context.Context) (tables, statements int, err error)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	p.counted = make(chan struct{})
	go func() {
		defer close(p.counted)
		tables, statements, err := count(ctx)
		if err != nil {
			// The restore reads the same file, and reports a fault in it
			// itself; without the count it goes on, short only of a total.
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.statements = statements
		if tables > p.total {
			p.total = tables
		}
		p.report()
	}()
	return cancel
}

// Statements returns the number of statements CountTotal found, or 0 while
// the count is still running.
func (p *RestoreProgress) Statements() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statements
}

// Statement records one executed statement: n bytes of the dump, the rows it
// wrote, and the table whose data it carried — "" for a statement carrying
// none, such as DDL.
func (p *RestoreProgress) Statement(n int, rows int64, table string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytes += int64(n)
	if rows > 0 {
		p.rows += rows
	}
	if table != "" && table != p.current {
		if p.current != "" {
			p.done++
		}
		p.current = table
	}
	p.report()
}

// Finish counts the table the restore ended on as finished. A CountTotal still
// running is waited for, so the total it brings cannot land after the end.
func (p *RestoreProgress) Finish() {
	if p.counted != nil {
		<-p.counted
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != "" {
		p.done++
		p.current = ""
	}
	if p.done > p.total {
		p.total = p.done
	}
	p.report()
}

func (p *RestoreProgress) report() {
	p.r.Report(core.Update{
		Bytes: p.bytes, Files: -1, Item: p.current,
		Rows: p.rows, Units: p.done, UnitsTotal: p.total,
	})
}

// CountDump reads the dump at path through each — a driver's streaming
// statement splitter — and returns the statements it holds and the distinct
// tables that table names as carrying data among them. It is the count a
// driver hands RestoreProgress.CountTotal.
func CountDump(ctx context.Context, path string,
	each func(r io.Reader, fn func(stmt string) error) error,
	table func(stmt string) string,
) (tables, statements int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	seen := map[string]bool{}
	err = each(bufio.NewReaderSize(f, 1<<20), func(stmt string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		statements++
		if t := table(StripLeadingComments(stmt)); t != "" {
			seen[t] = true
		}
		return nil
	})
	return len(seen), statements, err
}

// NewReportingReader returns r, reporting the bytes read through it so far as
// they are read. It is for a restore that hands the dump to a CLI on the far
// side of SSH: nothing is known there but how much of the file has been sent.
func NewReportingReader(ctx context.Context, r io.Reader) io.Reader {
	if !core.HasReporter(ctx) {
		return r
	}
	return &reportingReader{r: r, rep: core.ReporterFrom(ctx)}
}

type reportingReader struct {
	r   io.Reader
	rep core.Reporter
	n   atomic.Int64
}

func (rr *reportingReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if n > 0 {
		rr.rep.Report(core.Update{Bytes: rr.n.Add(int64(n)), Files: -1})
	}
	return n, err
}
