package validation

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// Validation modes. They apply to filesystem items only: object storage is
// always compared by size and ETag, and a database by row counts and schema
// objects, whichever mode was asked for.
const (
	// ModeQuick compares each file's size and modification time. Seconds for
	// any number of files, since nothing is read — but a file whose content
	// changed without either moving passes. The default: a validation of
	// gigabytes should not take minutes unless asked to.
	ModeQuick = "quick"
	// ModeChecksum adds a SHA256 of every file the migration carried, on both
	// hosts. It reads all of that data, so it takes as long as reading it does.
	ModeChecksum = "checksum"
)

// ValidMode reports whether mode names a validation mode.
func ValidMode(mode string) bool {
	return mode == ModeQuick || mode == ModeChecksum
}

// Options steers one validation run.
type Options struct {
	// Mode is ModeQuick or ModeChecksum; empty means ModeQuick.
	Mode string
	// Progress, when set, is told where the run has got to in one line. It is
	// called from several goroutines at once and as often as every file, so it
	// must be safe for concurrent use and cheap: throttling is the caller's.
	Progress func(message string)
}

func (o Options) mode() string {
	if o.Mode == "" {
		return ModeQuick
	}
	return o.Mode
}

func (o Options) report(message string) {
	if o.Progress != nil {
		o.Progress(message)
	}
}

// within returns o with every progress line led by prefix, for a step inside a
// larger one — a folder of an entry, a database of a server.
func (o Options) within(prefix string) Options {
	if o.Progress == nil {
		return o
	}
	outer := o.Progress
	o.Progress = func(message string) { outer(prefix + " · " + message) }
	return o
}

// bothSides runs the source's and the destination's half of a step at once —
// they are different hosts, so neither waits on the other — and returns both
// errors joined, nil when both succeeded.
func bothSides(src, dst func() error) error {
	var wg sync.WaitGroup
	var srcErr, dstErr error
	wg.Add(2)
	go func() { defer wg.Done(); srcErr = src() }()
	go func() { defer wg.Done(); dstErr = dst() }()
	wg.Wait()
	return errors.Join(srcErr, dstErr)
}

// sideProgress is the "src 3,412/10,000 · dst 2,980/10,000" half of a progress
// line: two counters advanced from two goroutines, rendered together so each
// report shows both ends.
type sideProgress struct {
	mu       sync.Mutex
	src, dst int
	total    int
	unit     string
	prefix   string
	opt      Options
}

func newSideProgress(opt Options, prefix string, total int, unit string) *sideProgress {
	p := &sideProgress{opt: opt, prefix: prefix, total: total, unit: unit}
	p.emit()
	return p
}

// advance counts one more unit done on the source (src=true) or destination.
func (p *sideProgress) advance(src bool) {
	p.mu.Lock()
	if src {
		p.src++
	} else {
		p.dst++
	}
	p.mu.Unlock()
	p.emit()
}

func (p *sideProgress) emit() {
	p.mu.Lock()
	msg := fmt.Sprintf("%s · src %s/%s · dst %s/%s %s", p.prefix,
		commas(p.src), commas(p.total), commas(p.dst), commas(p.total), p.unit)
	p.mu.Unlock()
	p.opt.report(msg)
}

// commas formats n with thousands separators: 10000 → "10,000".
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}
