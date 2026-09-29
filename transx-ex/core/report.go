package core

import "context"

// report.go carries observations OUT of a running transfer.
//
// Executor.Execute returns only an error, and that signature is the contract
// every executor implements — so a transfer had no way to say how far it had
// got, and a caller watching one could only wait. The sink travels in the
// context instead, which already reaches every executor.

// Update is one observation from inside a running transfer.
//
// Bytes and Files are cumulative WITHIN the reporting step, never across the
// pipeline: a relay moves the same data twice — source to staging, staging to
// destination — and each step counts from zero, so a consumer that adds the steps
// together reports double what was migrated. A high-water mark over the steps is
// what storagex keeps.
//
// Step says which step the observation came from and Steps how many there are,
// for a consumer that wants to tell the legs apart — staging from sending — rather
// than fold them into one figure.
//
// A negative Bytes or Files means "unknown" and an empty Item "no news": a
// reporter fills in what it knows and leaves the rest alone.
type Update struct {
	Step  int
	Steps int
	Bytes int64
	Files int64
	Item  string
}

// Reporter receives observations from inside a transfer.
//
// Implementations must be safe for concurrent use and must not block: Report is
// called on the transfer path, so a slow reporter slows the migration.
type Reporter interface {
	Report(Update)
}

// ReporterFunc adapts a function to Reporter.
type ReporterFunc func(Update)

// Report implements Reporter.
func (f ReporterFunc) Report(u Update) { f(u) }

// nopReporter discards everything, so an executor reports unconditionally rather
// than testing for a sink it usually does not have.
type nopReporter struct{}

func (nopReporter) Report(Update) {}

type reporterKey struct{}

// WithReporter returns ctx carrying r, which executors reach with ReporterFrom.
// A nil r returns ctx unchanged.
func WithReporter(ctx context.Context, r Reporter) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFrom returns the reporter in ctx, or one that discards. Never nil.
func ReporterFrom(ctx context.Context) Reporter {
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok && r != nil {
		return r
	}
	return nopReporter{}
}

// HasReporter reports whether anything is listening, for an executor that would
// have to do extra work to produce a number — count a directory, stat a file —
// and should not do it for nobody.
func HasReporter(ctx context.Context) bool {
	_, ok := ctx.Value(reporterKey{}).(Reporter)
	return ok
}

// withStepReporter returns a context whose reporter stamps every Update with the
// step it came from, so an executor reports what it moved and nothing about
// where it sits in a pipeline it cannot see.
func withStepReporter(ctx context.Context, step, steps int) context.Context {
	if !HasReporter(ctx) {
		return ctx
	}
	r := ReporterFrom(ctx)
	return WithReporter(ctx, ReporterFunc(func(u Update) {
		u.Step, u.Steps = step, steps
		r.Report(u)
	}))
}
