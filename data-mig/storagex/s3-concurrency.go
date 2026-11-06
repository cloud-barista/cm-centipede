package storagex

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
)

// =============================================================================
// Object storage concurrency — how many objects, and parts of one, at once
// =============================================================================
//
// One at a time, a transfer of many small objects is bound by round trips, not
// bandwidth: each object costs a presigned URL — through Tumblebug or Spider an
// API call of its own — and a request, and the next waits for both. Several in
// flight hide those round trips behind one another. The migration's Concurrency
// (core.ConcurrencyFrom) says how many.
//
// Objects written in parts go one at a time, in a lane of their own beside the
// rest (forEachInLanes), and that one runs as many parts at once, so it does
// not crawl along on a single stream while the small ones around it finish.
// Several of them at once multiplied objects by parts — 32 × 8 connections to
// one endpoint — more than a source or a link could keep fed; one at a time,
// one endpoint sees the objects plus the parts, and s3MaxConnections caps that.

// s3MaxConnections is the most connections a transfer opens to one endpoint:
// the objects in flight plus the parts of the one object written in parts. The
// idle pools are sized to it, so a part reuses a connection rather than opening
// a TLS session of its own.
const s3MaxConnections = 256

// s3Objects is how many objects a transfer moves at once, beside the one
// written in parts.
func s3Objects(ctx context.Context) int {
	return core.ConcurrencyFrom(ctx)
}

// s3Parts is how many parts of the object written in parts are in flight at
// once: as many as objects, unless that would take the two past
// s3MaxConnections.
func s3Parts(ctx context.Context) int {
	c := core.ConcurrencyFrom(ctx)
	return max(1, min(c, s3MaxConnections-c))
}

// reportS3Concurrency reports what a transfer runs at, before it starts: the
// objects at once, and the parts of each when those had to be cut back.
func reportS3Concurrency(ctx context.Context) {
	objects, parts := s3Objects(ctx), s3Parts(ctx)
	note := ""
	if parts < objects {
		note = fmt.Sprintf("%d parts per object: %d connections at most", parts, s3MaxConnections)
	}
	core.ReportConcurrency(ctx, objects, note)
}

// largestFirst returns 0 to n-1 ordered by size, largest first, ties kept in
// listing order.
//
// The transfer ends when its last object does. Started last, a large object
// runs alone at the end while every other worker has nothing left to do;
// started first, it runs beside the small ones, which then fill in around it
// and finish together.
func largestFirst(n int, size func(i int) int64) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(size(b), size(a)) })
	return order
}
