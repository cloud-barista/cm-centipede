package filter

import (
	"encoding/json"
	"fmt"
)

func init() {
	Register("size", newSizeMatcher)
}

// sizeMatcher matches an item by comparing its size (bytes) against a value.
// Supported ops: ">", ">=", "<", "<=", "==".
type sizeMatcher struct {
	Op    string `json:"op"`
	Value int64  `json:"value"`
}

func newSizeMatcher(raw json.RawMessage) (Matcher, error) {
	var c struct {
		Op    string `json:"op"`
		Value int64  `json:"value"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	switch c.Op {
	case ">", ">=", "<", "<=", "==":
	default:
		return nil, fmt.Errorf("size: invalid op %q (want > >= < <= ==)", c.Op)
	}
	return &sizeMatcher{Op: c.Op, Value: c.Value}, nil
}

func (s *sizeMatcher) Type() string { return "size" }

func (s *sizeMatcher) Match(item Item) bool {
	switch s.Op {
	case ">":
		return item.Size > s.Value
	case ">=":
		return item.Size >= s.Value
	case "<":
		return item.Size < s.Value
	case "<=":
		return item.Size <= s.Value
	case "==":
		return item.Size == s.Value
	}
	return false
}

// RsyncArgs maps a size rule to rsync's global --max-size/--min-size gate.
// rsync only expresses "drop by size", so only exclude actions are supported.
//
// Both of rsync's bounds are STRICT — --max-size=N drops what is "larger than" N
// and --min-size=N drops what is "smaller than" N, so a file of exactly N bytes
// survives either one. Match is not strict for ">=" and "<=", so those two are
// shifted by a byte to mean the same thing. Without the shift a ">= N" rule left
// the N-byte file on the target after asking for it to be dropped.
//
// The shift is exact because a size in bytes is an integer: "at least N" and
// "larger than N-1" select the same files.
func (s *sizeMatcher) RsyncArgs(action string) ([]string, error) {
	if action != ActionExclude {
		return nil, fmt.Errorf("size: rsync supports size filters only as 'exclude', got %q", action)
	}
	switch s.Op {
	case ">":
		return []string{fmt.Sprintf("--max-size=%d", s.Value)}, nil
	case ">=":
		// "at least 0 bytes" is every file, and rsync has no --max-size that says
		// so (--max-size=-1 is rejected). Refused rather than silently narrowed.
		if s.Value <= 0 {
			return nil, fmt.Errorf("size: '>= %d' excludes every file; use a glob rule such as {exclude, glob, \"*\"} instead", s.Value)
		}
		return []string{fmt.Sprintf("--max-size=%d", s.Value-1)}, nil
	case "<":
		return []string{fmt.Sprintf("--min-size=%d", s.Value)}, nil
	case "<=":
		return []string{fmt.Sprintf("--min-size=%d", s.Value+1)}, nil
	default:
		return nil, fmt.Errorf("size: rsync cannot express op %q", s.Op)
	}
}
