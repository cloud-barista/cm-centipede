package database

import (
	"context"
	"log"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// parallel writers
// ---------------------------------------------------------------------------

// bulkWriter is one connection's share of a parallel load. pumpBatches hands it
// batches of generated rows; it owns whatever per-connection state writing them
// needs - a transaction left open across batches, a session setting.
type bulkWriter interface {
	// write stores one batch. Rows are in the table's column order.
	write(ctx context.Context, rows [][]any) error
	// finish ends the writer's work: commit what is open when ok, roll it back
	// when not. It is called once, after the last batch or after a failure.
	finish(ctx context.Context, ok bool) error
}

// pumpBatches generates rows rows of t and has writers store them, all at once.
//
// The rows are generated on this goroutine alone, batch rows at a time, in id
// order: genCtx draws from one seeded source, and drawing from it on several
// goroutines would make the data depend on scheduling. Only the writing runs in
// parallel - which is where a load over a network spends its time, waiting for
// each batch's reply. The batches are queued two per writer deep, enough to keep
// every writer busy without generating far ahead of them.
//
// The first error stops everything: generation stops feeding, the other writers
// drain the queue without writing, and every writer's finish is told the load
// failed. That error is the one returned.
func pumpBatches(ctx context.Context, t bulkTable, rows, batch int, g *genCtx, idOffset int, writers []bulkWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog := newProgress(t.name, rows)
	queue := make(chan [][]any, 2*len(writers))

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}

	for _, w := range writers {
		wg.Add(1)
		go func(w bulkWriter) {
			defer wg.Done()
			for b := range queue {
				if failed() {
					continue // drain, so the generator never blocks on a dead writer
				}
				if err := w.write(ctx, b); err != nil {
					fail(err)
					continue
				}
				prog.add(len(b))
			}
			// finish gets a context that outlives a cancellation, so a rollback
			// still reaches the server after another writer's failure.
			if err := w.finish(context.WithoutCancel(ctx), !failed()); err != nil {
				fail(err)
			}
		}(w)
	}

generate:
	for done := 0; done < rows; {
		n := batch
		if rem := rows - done; rem < n {
			n = rem
		}
		b := make([][]any, n)
		for i := range b {
			b[i] = t.gen(g, idOffset+done+i)
		}
		select {
		case queue <- b:
		case <-ctx.Done():
			break generate
		}
		done += n
	}
	close(queue)
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prog.done()
	return nil
}

// workerCount is the number of writers BulkOptions asks for, at least one.
func workerCount(o BulkOptions) int {
	if o.Workers < 1 {
		return 1
	}
	return o.Workers
}

// ---------------------------------------------------------------------------
// progress of a long DDL statement
// ---------------------------------------------------------------------------

// ddlStillEvery is how often an unchanged progress line is repeated, as a sign
// of life rather than news.
const ddlStillEvery = 30 * time.Second

// ddlWatcher reports on a long statement - an index build - while it runs, by
// asking the server every progressEvery how far it has got. poll is the
// engine's own question; it runs on a connection other than the busy one.
//
// A nil watcher reports nothing, which is what an engine without a progress
// source gets.
type ddlWatcher struct {
	ctx  context.Context
	poll func(ctx context.Context) (string, bool)
}

// start begins reporting under label and returns the function that stops it.
// Stopping waits for the poller to exit, so no progress line lands after the
// caller's "done".
func (w *ddlWatcher) start(label string) func() {
	if w == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		start := time.Now()
		var last string
		var lastAt time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				// A failed or empty read is skipped silently: the statement may
				// have finished between the tick and the query, and progress is a
				// courtesy that must never fail the load.
				line, ok := w.poll(ctx)
				if !ok {
					continue
				}
				// Engines report an estimate that can sit on one value for minutes
				// - MySQL holds 100% through its insert and flush stages - so a
				// line is logged when it changes, and an unchanged one only every
				// ddlStillEvery, with the elapsed time to show the build is alive.
				now := time.Now()
				switch {
				case line != last:
					log.Printf("%s: %s", label, line)
				case now.Sub(lastAt) >= ddlStillEvery:
					log.Printf("%s: %s (still running, %s)", label, line, now.Sub(start).Round(time.Second))
				default:
					continue
				}
				last, lastAt = line, now
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
