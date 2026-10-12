package base

import (
	"context"
	"io"
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
// data for, which a driver counts before it starts; one without rows has no
// data statement, so it is neither counted nor finished.
type RestoreProgress struct {
	r           core.Reporter
	bytes, rows int64
	done, total int
	current     string
}

// NewRestoreProgress starts following a restore of tablesTotal tables.
func NewRestoreProgress(ctx context.Context, tablesTotal int) *RestoreProgress {
	return &RestoreProgress{r: core.ReporterFrom(ctx), total: tablesTotal}
}

// Statement records one executed statement: n bytes of the dump, the rows it
// wrote, and the table whose data it carried — "" for a statement carrying
// none, such as DDL.
func (p *RestoreProgress) Statement(n int, rows int64, table string) {
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

// Finish counts the table the restore ended on as finished.
func (p *RestoreProgress) Finish() {
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
