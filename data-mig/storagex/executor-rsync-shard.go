package storagex

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
	"golang.org/x/sync/errgroup"
)

// =============================================================================
// Sharded rsync — several rsync processes ahead of the one that always runs
// =============================================================================
//
// One rsync is one SSH session, and a session moves at most its channel window
// per round trip: 2 MiB over 30 ms is about 70 MB/s, over 150 ms about 14. It
// also compresses on one core. Several processes, each given a share of the
// files, multiply both.
//
// The shards run first, and then the transfer runs exactly as it always did —
// one rsync with the location's filters, --delete and --stats. By then the
// files are in place and that run only compares them, so it costs little; and
// it is what settles everything a list of files cannot carry: empty
// directories, directories' own modes and times, symbolic links, what
// --delete removes, and anything the shards missed or that changed under them.
// The shards are an optimisation: when they fail, the full run still moves
// everything, so a failure here is reported and passed over, never returned.
//
// The filter is applied here, by Go, with MatchPath — the same reading of the
// same rules rsync gives them (see rsyncFilterArgs) — and the shards get bare
// lists. Giving them the rules too would judge every file twice, and a
// catch-all exclude would drop files the list names.

const (
	// rsyncMaxShards is the most rsync processes a transfer runs at once. Each
	// is an SSH session to an OpenSSH server whose MaxStartups (10 by default)
	// refuses connections beyond it, and the final run needs one more.
	rsyncMaxShards = 8
	// rsyncMinFilesPerShard and rsyncMinBytesPerShard are the least a process
	// of its own is worth: so many files, or so many bytes. Below both, the
	// sessions it opens cost more than it saves.
	//
	// A process costs about a second — two SSH logins, rsync starting, its file
	// list — and saves what its share would have taken one stream: share ÷ the
	// stream's rate. One stream moves 14 to 70 MB/s (2 MiB of SSH window per
	// round trip at 150 to 30 ms, or zlib on one core), so a share pays for
	// itself from 14 to 70 MB; 128 MiB is about twice the most of that.
	rsyncMinFilesPerShard = 50
	rsyncMinBytesPerShard = 128 << 20
)

// shardPlan says how many rsync processes the transfer from source runs ahead
// of the final one, and why that is not what the migration asked for when it
// is not. 1 runs the transfer as it always ran.
func (e *RsyncExecutor) shardPlan(ctx context.Context, source DataLocation) (int, string) {
	want := core.ConcurrencyFrom(ctx)
	switch {
	case want <= 1:
		return 1, ""
	case e.DryRun:
		return 1, "single rsync: dry run"
	}
	if _, err := rsyncFilterArgs(source.Filter); err != nil {
		return 1, "single rsync: filter not expressible as rsync rules"
	}
	if want > rsyncMaxShards {
		return rsyncMaxShards, fmt.Sprintf("capped from %d: at most %d rsync processes (ssh MaxStartups)", want, rsyncMaxShards)
	}
	return want, ""
}

// runShards copies the files under source in n rsync processes, each given a
// share of them by size, and returns the context the final run reports under:
// one that counts what the shards moved ahead of its own figures.
func (e *RsyncExecutor) runShards(ctx context.Context, n int, note string, source, destination DataLocation) context.Context {
	files, err := ListFiles(ctx, source)
	if err != nil {
		core.ReportConcurrency(ctx, 1, "single rsync: listing the source failed: "+err.Error())
		return ctx
	}
	var keep []FileInfo
	for _, f := range files {
		if source.Filter.MatchPath(f.Path, f.Size, false) {
			keep = append(keep, f)
		}
	}
	var size int64
	for _, f := range keep {
		size += f.Size
	}
	if m := shardsFor(n, len(keep), size); m < n {
		if m < 2 {
			core.ReportConcurrency(ctx, 1, "single rsync: "+describeFiles(len(keep), size))
			return ctx
		}
		n, note = m, fmt.Sprintf("%d of %d: %s", m, core.ConcurrencyFrom(ctx), describeFiles(len(keep), size))
	}
	core.ReportConcurrency(ctx, n, note)

	sum := newShardSum(core.ReporterFrom(ctx), n)
	g, gctx := errgroup.WithContext(ctx)
	for i, shard := range splitBySize(keep, n) {
		g.Go(func() error {
			s := *e // a copy: its own temporary key file, its own list
			s.DeleteExtraneous = false
			s.AdditionalArgs = append(slices.Clone(e.AdditionalArgs), "--from0", "--files-from=-")
			s.filesFrom = nulList(shard)
			src, dst := source, destination
			src.Filter, dst.Filter = nil, nil
			return s.transfer(core.WithReporter(gctx, sum.shard(i)), src, dst)
		})
	}
	if err := g.Wait(); err != nil && ctx.Err() == nil {
		// The final run moves whatever the shards did not.
		core.ReportConcurrency(ctx, 1, "single rsync: parallel phase failed: "+err.Error())
	}
	return core.WithReporter(ctx, sum.offset())
}

// shardsFor is how many of n processes files files of size bytes in all are
// worth: as many as either minimum allows — a process per rsyncMinFilesPerShard
// files or per rsyncMinBytesPerShard bytes — and never more than there are
// files, for rsync cannot split one.
func shardsFor(n, files int, size int64) int {
	return min(n, files, max(files/rsyncMinFilesPerShard, int(size/rsyncMinBytesPerShard)))
}

// describeFiles says how much a transfer holds, for the reason it was not cut
// into as many processes as asked: "54 files, 2.4 MiB".
func describeFiles(files int, size int64) string {
	unit := "files"
	if files == 1 {
		unit = "file"
	}
	return fmt.Sprintf("%d %s, %s", files, unit, humanBytes(size))
}

func humanBytes(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1f GiB", float64(b)/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1f MiB", float64(b)/(k*k))
	case b >= k:
		return fmt.Sprintf("%.1f KiB", float64(b)/k)
	}
	return fmt.Sprintf("%d B", b)
}

// splitBySize deals files into n lists of about equal bytes: largest first,
// each to the list lightest so far.
func splitBySize(files []FileInfo, n int) [][]FileInfo {
	sorted := slices.Clone(files)
	slices.SortStableFunc(sorted, func(a, b FileInfo) int { return cmp.Compare(b.Size, a.Size) })
	shards := make([][]FileInfo, n)
	totals := make([]int64, n)
	for _, f := range sorted {
		i := slices.Index(totals, slices.Min(totals))
		shards[i] = append(shards[i], f)
		totals[i] += f.Size
	}
	return shards
}

// nulList is files' paths as rsync --from0 --files-from reads them.
func nulList(files []FileInfo) []byte {
	var b bytes.Buffer
	for _, f := range files {
		b.WriteString(f.Path)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// shardSum folds the running totals of several rsync processes into one, for a
// reporter that expects a single transfer's cumulative figures.
type shardSum struct {
	r     core.Reporter
	mu    sync.Mutex
	bytes []int64
	files []int64
}

func newShardSum(r core.Reporter, n int) *shardSum {
	return &shardSum{r: r, bytes: make([]int64, n), files: make([]int64, n)}
}

// shard is the reporter shard i reports through.
func (s *shardSum) shard(i int) core.Reporter {
	return core.ReporterFunc(func(u core.Update) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if u.Bytes >= 0 {
			s.bytes[i] = u.Bytes
		}
		if u.Files >= 0 {
			s.files[i] = u.Files
		}
		u.Bytes, u.Files = total(s.bytes), total(s.files)
		s.r.Report(u)
	})
}

// offset is the reporter the final run reports through: its figures count
// from what the shards moved.
func (s *shardSum) offset() core.Reporter {
	s.mu.Lock()
	moved, files := total(s.bytes), total(s.files)
	s.mu.Unlock()
	return core.ReporterFunc(func(u core.Update) {
		if u.Bytes >= 0 {
			u.Bytes += moved
		}
		if u.Files >= 0 {
			u.Files += files
		}
		s.r.Report(u)
	})
}

func total(v []int64) int64 {
	var t int64
	for _, x := range v {
		t += x
	}
	return t
}
