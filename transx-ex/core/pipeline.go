package core

import (
	"context"
	"fmt"
)

// =============================================================================
// Executor
// =============================================================================

// Executor carries out a single migration step between a source and destination.
//
// L is the domain's location type: storagex.DataLocation for a file or object
// transfer, dbmsx.DBMSLocation for a database one.
//
// Execute must not modify the source, and must honour ctx: a cancelled context
// aborts the step rather than running it to completion.
type Executor[L any] interface {
	Execute(ctx context.Context, source, destination L) error
}

// =============================================================================
// Pipeline and Step
// =============================================================================

// Step is a single unit of work within a Pipeline.
type Step[L any] struct {
	Name        string
	Source      L
	Destination L
	Executor    Executor[L]
}

// Pipeline is an ordered sequence of Steps produced by a domain planner.
//
// Strategy carries the planner's routing decision — the storage domain records
// its transfer strategy there, the DBMS domain its migration scope — so a
// planned pipeline is self-describing without a domain-specific wrapper.
//
// Cleanup releases whatever the planner had to allocate before the Steps could
// be built — a staging directory, typically. Execute runs it once the steps are
// done, so a planner that allocates does not depend on its caller to tidy up.
// It is nil for a pipeline that allocated nothing.
//
// A pipeline that is planned and never executed keeps what it allocated, because
// there is no other moment at which "nobody will run this" becomes known. That
// leaves an empty directory rather than a populated one, which is the difference
// between untidy and wrong.
type Pipeline[L any] struct {
	Name     string
	Strategy string
	Steps    []Step[L]
	Cleanup  func()
}

// Execute runs each Step in order, stopping at the first failure. The step's
// name and position are attached to the error so a multi-step pipeline reports
// where it stopped.
//
// The context is checked before each step, so cancelling a pipeline stops it at
// the next boundary even when the running executor does not observe ctx itself.
func (p *Pipeline[L]) Execute(ctx context.Context) error {
	// Deferred rather than run at the end, because a failed step and a cancelled
	// context leave exactly the same staging behind as a successful one — and a
	// half-written staging directory is the case where leaving it behind does the
	// most harm, since it is what a later transfer would otherwise pick up.
	if p.Cleanup != nil {
		defer p.Cleanup()
	}

	for i, step := range p.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The step's own context, so an observation says which step produced it
		// without every executor having to be told where it sits.
		stepCtx := withStepReporter(ctx, i+1, len(p.Steps))
		if err := step.Executor.Execute(stepCtx, step.Source, step.Destination); err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i+1, step.Name, err)
		}
	}
	return nil
}
