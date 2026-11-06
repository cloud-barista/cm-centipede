// Package progress reports how far a long step has got - generating the dummy
// files, uploading them to a bucket, transferring them to a VM - as files and
// bytes done, throughput and time left.
//
// On a terminal the report is one line redrawn in place, at most five times a
// second. Anywhere else - gen-data.sh piped into tee, a CI log - carriage
// returns would pile up into one unreadable line, so a plain log line is written
// at every 10% instead. Either way the step ends with one summary line.
package progress

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const (
	redrawEvery = 200 * time.Millisecond
	mib         = 1 << 20
	// recentWindow is how far back the "now" rate looks. The overall rate is an
	// average since the start, which hides a step that is slowing down.
	recentWindow = 5 * time.Second
)

// Counter tracks one step. It is safe for concurrent use: the upload and
// transfer workers report into the same Counter.
type Counter struct {
	label      string
	totalFiles int
	totalBytes int64
	tty        bool
	start      time.Time

	mu       sync.Mutex
	files    int
	bytes    int64
	lastDraw time.Time
	lastStep int // the last 10% step logged, when not on a terminal

	// The "now" rate: bytes over the last full window, refreshed as it passes.
	winStart time.Time
	winBytes int64
	nowRate  int64
}

// New starts a Counter for a step of totalFiles files, totalBytes in all.
func New(label string, totalFiles int, totalBytes int64) *Counter {
	return &Counter{
		label:      label,
		totalFiles: totalFiles,
		totalBytes: totalBytes,
		tty:        isTerminal(os.Stderr),
		start:      time.Now(),
		winStart:   time.Now(),
	}
}

// Add records one finished file of size bytes.
func (c *Counter) Add(size int64) { c.record(1, size) }

// AddBytes records n bytes of a file still in flight. A step whose files are
// large reports through it as it goes - one 10 GB file would otherwise sit at 0%
// until it is done - and then calls Add(0) for each file, its bytes already
// counted.
func (c *Counter) AddBytes(n int64) { c.record(0, n) }

func (c *Counter) record(files int, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files += files
	c.bytes += size
	if now := time.Now(); now.Sub(c.winStart) >= recentWindow {
		if !c.winStart.IsZero() {
			c.nowRate = rate(c.bytes-c.winBytes, now.Sub(c.winStart))
		}
		c.winStart, c.winBytes = now, c.bytes
	}
	if c.files >= c.totalFiles {
		return // Finish reports the end
	}
	now := time.Now()
	if c.tty {
		if now.Sub(c.lastDraw) < redrawEvery {
			return
		}
		c.lastDraw = now
		fmt.Fprintf(os.Stderr, "\r\033[K%s", c.line(now))
		return
	}
	if step := c.percent() / 10; step > c.lastStep {
		c.lastStep = step
		log.Print(c.line(now))
	}
}

// percent is how far the step has got, by bytes when the total is known - a
// file count says nothing while the only file is still going - else by files.
func (c *Counter) percent() int {
	switch {
	case c.totalBytes > 0:
		// A retried part is read twice, so the bytes can overshoot the total.
		return int(min(c.bytes*100/c.totalBytes, 100))
	case c.totalFiles > 0:
		return c.files * 100 / c.totalFiles
	}
	return 0
}

// Finish ends the step with a summary line, whether it completed or not: a
// failed upload still says how far it got.
func (c *Counter) Finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	elapsed := time.Since(c.start)
	log.Printf("%s: %d/%d files, %s in %s (%s/s)", c.label, c.files, c.totalFiles,
		size(c.bytes), elapsed.Round(time.Second), size(rate(c.bytes, elapsed)))
}

// line renders the in-flight report:
// "bucket: 120/800 files (15%), 120.0 MiB / 800.0 MiB, 12.3 MiB/s (now 9.8 MiB/s), 55s left".
func (c *Counter) line(now time.Time) string {
	elapsed := now.Sub(c.start)
	pct := c.percent()
	left := "-"
	if r := rate(c.bytes, elapsed); r > 0 && c.totalBytes > c.bytes {
		left = (time.Duration(float64(c.totalBytes-c.bytes)/float64(r)) * time.Second).Round(time.Second).String()
	}
	recent := ""
	if c.nowRate > 0 {
		recent = fmt.Sprintf(" (now %s/s)", size(c.nowRate))
	}
	return fmt.Sprintf("%s: %d/%d files (%d%%), %s / %s, %s/s%s, %s left", c.label,
		c.files, c.totalFiles, pct, size(c.bytes), size(c.totalBytes), size(rate(c.bytes, elapsed)), recent, left)
}

// rate is bytes per second, 0 before any time has passed.
func rate(bytes int64, elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return 0
	}
	return int64(float64(bytes) / elapsed.Seconds())
}

func size(b int64) string {
	if b >= 1024*mib {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1024*mib))
	}
	return fmt.Sprintf("%.1f MiB", float64(b)/mib)
}

// isTerminal reports whether f is a character device - a terminal rather than a
// pipe or a file - without pulling in x/term for one bit.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
