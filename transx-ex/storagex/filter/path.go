package filter

import "strings"

// path.go fixes what an Item.Path is, because a rule reaches the same verdict
// as the transfer only when both compare the same string.
//
// Every Matcher compares against the path RELATIVE TO THE TRANSFER ROOT — the
// folder or key prefix the migration was told to copy. That is the string rsync
// matches its own patterns against, so a filesystem transfer and the Go matcher
// judging it agree by construction rather than by a mirror kept in step by hand.
// Object storage has no rsync, but it shares these rule types with the
// filesystem, so it uses the same convention: one pattern means one thing
// wherever it is attached.

// Rel expresses path relative to root, the form Matchers compare against.
// Both are taken with either "/" convention and returned without a leading or
// trailing one. A path outside root is returned unchanged rather than guessed
// at, which leaves it to be judged on what it is.
//
// The root itself yields "", which Option.Match keeps: rsync never drops the
// transfer root, and a listing that hid it would report a scan of nothing.
func Rel(root, path string) string {
	root = strings.Trim(root, "/")
	rel := strings.Trim(path, "/")
	if root == "" {
		return rel
	}
	if rel == root {
		return ""
	}
	if strings.HasPrefix(rel, root+"/") {
		return rel[len(root)+1:]
	}
	return rel
}
