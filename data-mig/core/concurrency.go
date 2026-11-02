package core

import (
	"context"
	"fmt"
)

// concurrency.go carries how many transfers a migration may run at once IN to
// the executors, and what they actually ran at back OUT.
//
// The number travels in the context, as the reporter does: it is a property of
// the migration rather than of either location, and an executor's signature —
// a database driver's Restore among them — has no room for options. Each domain
// reads it where it fans out and narrows it to what its environment allows (an
// SSH server's session limit, a database's free connections); the figure it
// settles on is reported, so a caller can tell a requested 16 from the 8 that
// ran.

const (
	// DefaultConcurrency is what a migration that sets no Concurrency runs at.
	DefaultConcurrency = 4
	// MaxConcurrency is the largest Concurrency a migration may ask for. Beyond
	// it the link, the target's CPU or its disk are full long before another
	// stream would help, and the source pays for every one of them.
	MaxConcurrency = 32
)

// ValidateConcurrency checks a requested Concurrency: zero (the default) or 1
// to MaxConcurrency. A value out of range is refused rather than clamped, so a
// migration never runs at a figure nobody asked for without saying why.
func ValidateConcurrency(n int) error {
	if n < 0 || n > MaxConcurrency {
		return fmt.Errorf("concurrency %d is out of range: must be 1 to %d, or 0 for the default (%d)",
			n, MaxConcurrency, DefaultConcurrency)
	}
	return nil
}

type concurrencyKey struct{}

// WithConcurrency returns ctx carrying the Concurrency a migration asked for,
// which executors reach with ConcurrencyFrom. Zero leaves the default.
func WithConcurrency(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, concurrencyKey{}, n)
}

// ConcurrencyFrom returns the Concurrency in ctx, or DefaultConcurrency when
// none was set. Never less than 1.
func ConcurrencyFrom(ctx context.Context) int {
	if n, ok := ctx.Value(concurrencyKey{}).(int); ok && n > 0 {
		return n
	}
	return DefaultConcurrency
}

// ReportConcurrency tells the reporter in ctx what a step settled on: applied
// transfers at once, and why that differs from what was asked when it does
// ("capped from 16: ssh MaxSessions"). note is empty when the request ran as
// given. Sent once per step, before the transfer starts.
func ReportConcurrency(ctx context.Context, applied int, note string) {
	ReporterFrom(ctx).Report(Update{Bytes: -1, Files: -1, Concurrency: applied, ConcurrencyNote: note})
}
