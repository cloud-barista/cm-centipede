package storagex

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/cloud-barista/cm-centipede/transx-ex/core"
)

// rsync-progress.go reads rsync's output as it runs.
//
// The executor used to take the whole output with CombinedOutput and use it only
// for the error message, so a folder of any size reported nothing until it
// finished. rsync will say what it is doing — one line per file, flushed as each
// one completes, even through a pipe — and this reads it.
//
// Everything that is NOT a progress line still has to be kept: "Permission
// denied (publickey,password)" is what explains a failed transfer, and it arrives
// on the same output. So the two are separated rather than one replacing the
// other.

// progressPrefix marks the lines written for this file to read. A sentinel rather
// than a bare format: -v and --stats put their own lines on the same stream, and
// a prefix tells them apart without guessing at shapes.
const progressPrefix = "@@TX "

// rsyncOutFormat is the --out-format flag producing them:
//
//	@@TX <bytes sent> <file length> <path relative to the transfer root>
//
// It does not duplicate -v's own per-file line; --out-format replaces it.
const rsyncOutFormat = "--out-format=" + progressPrefix + "%b %l %n"

// rsyncTailBytes is how much of the non-progress output is kept for an error
// message. Enough for rsync's error block and the ssh handshake failure above it,
// bounded so a transfer of a million files does not hold its own log in memory.
const rsyncTailBytes = 16 << 10

// tailBuffer keeps the last rsyncTailBytes written to it.
//
// Safe for concurrent use: os/exec and crypto/ssh both copy stderr from their own
// goroutine while the caller reads stdout.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{cap: capacity}
}

// Write implements io.Writer, dropping from the front once full.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.cap; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// streamRsync consumes out until EOF, reporting each file as rsync finishes it,
// and returns the bytes transferred so far. Lines that are not progress lines go
// to tail, which is what an error message is built from.
//
// The running total counts the FILE LENGTH of every entry rsync actually sent,
// not the bytes it put on the wire: the wire figure carries protocol overhead
// (50,059 for a 50,000-byte file) and compression, so summing it would disagree
// with --stats and with the source it was read from. An entry rsync did not send
// — a directory, an unchanged file — reports zero bytes sent and is skipped,
// which is also what keeps directory sizes out of the total.
func streamRsync(ctx context.Context, out io.Reader, tail *tailBuffer) int64 {
	reporter := core.ReporterFrom(ctx)
	var moved int64

	scanner := bufio.NewScanner(out)
	// rsync prints one path per line; a long path is still a line, but the
	// default 64 KB limit would end the scan rather than truncate, taking the
	// rest of the transfer's progress with it.
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		sent, length, name, ok := parseProgressLine(line)
		if !ok {
			tail.Write([]byte(line + "\n")) //nolint:errcheck — tailBuffer cannot fail
			continue
		}
		if sent <= 0 {
			continue
		}
		moved += length
		reporter.Report(core.Update{Bytes: moved, Files: -1, Item: name})
	}
	return moved
}

// parseProgressLine reads one "@@TX <sent> <length> <name>" line. ok is false for
// anything else, including a progress line whose numbers do not parse — rsync
// moved the data either way, and a line that cannot be read must not be counted
// as zero.
func parseProgressLine(line string) (sent, length int64, name string, ok bool) {
	if !strings.HasPrefix(line, progressPrefix) {
		return 0, 0, "", false
	}
	rest := strings.TrimPrefix(line, progressPrefix)

	// Three fields, and the name may contain spaces — so the two numbers are cut
	// from the front and the remainder is the name, whatever is in it.
	first := strings.IndexByte(rest, ' ')
	if first < 0 {
		return 0, 0, "", false
	}
	second := strings.IndexByte(rest[first+1:], ' ')
	if second < 0 {
		return 0, 0, "", false
	}
	second += first + 1

	sent, err := strconv.ParseInt(rest[:first], 10, 64)
	if err != nil {
		return 0, 0, "", false
	}
	length, err = strconv.ParseInt(rest[first+1:second], 10, 64)
	if err != nil {
		return 0, 0, "", false
	}
	return sent, length, rest[second+1:], true
}
