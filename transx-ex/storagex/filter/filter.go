// Package filter provides an extensible, ordered filter pipeline for transx-ex.
//
// A pipeline is a list of Rules evaluated top-to-bottom; the first Rule whose
// Matcher matches an Item decides the outcome (include keeps it, exclude drops
// it). When no Rule matches, the item is included (default keep).
//
// Each Rule carries a Matcher — a pluggable predicate keyed by a "type" string.
// Built-in matchers (glob, size) register themselves; new matcher types can be
// added with Register, so filters can be extended without touching this file.
package filter

import (
	"path"
	"strings"
	"sync"
	"time"
)

// Action constants for a filter Rule.
const (
	ActionInclude = "include"
	ActionExclude = "exclude"
)

// Item is the input a Matcher evaluates: the metadata of one file, directory,
// or object storage prefix.
type Item struct {
	Path    string    // path relative to the transfer root (see path.go)
	Name    string    // base name (last path segment)
	Size    int64     // size in bytes (0 for prefixes)
	ModTime time.Time // modification time (zero when unknown)
	IsDir   bool      // whether the item is a directory/prefix
}

// Matcher is a single filter predicate. Type returns the registry key used to
// (de)serialise the matcher.
type Matcher interface {
	Match(item Item) bool
	Type() string
}

// RsyncTranslator is implemented by matchers that can be expressed as native
// rsync CLI arguments. Matchers that do not implement it cannot be used with
// rsync-based transfers (the executor returns an error).
type RsyncTranslator interface {
	// RsyncArgs returns the rsync flags for this matcher under the given action,
	// or an error when the matcher cannot be expressed natively.
	RsyncArgs(action string) ([]string, error)
}

// Option is the filter configuration attached to a DataLocation.
//
// Rules is the ordered pipeline. Include/Exclude are the simple form: they
// apply only when Rules is empty, and are normalised into equivalent glob rules
// by EffectiveRules. MaxDepth limits the scan depth for inspect (structural,
// independent of the rules).
type Option struct {
	Rules    []Rule   `json:"rules,omitempty"`
	Include  []string `json:"include,omitempty"` // simple form — used only when Rules is empty
	Exclude  []string `json:"exclude,omitempty"` // simple form — used only when Rules is empty
	MaxDepth int      `json:"maxDepth"`          // maximum scan depth; 0 = unlimited

	// Normalised pipeline, built once on first use. An Option is configuration:
	// it is decoded or constructed, then read, so the rules are not re-read
	// after the first Match. Mutating Rules afterwards would not be seen.
	effectiveOnce sync.Once
	effective     []Rule

	// The same pipeline with the size rules dropped, for judging ancestor
	// directories — see MatchPath.
	dirOnce  sync.Once
	dirRules []Rule
}

// Match evaluates the pipeline against item. The first matching rule wins;
// when no rule matches the item is included. A nil Option matches everything.
//
// item.Path is relative to the transfer root (see path.go). The root itself —
// the empty path — is kept whatever the rules say, because rsync never drops
// the root of a transfer and a catch-all would otherwise hide the whole scan.
func (o *Option) Match(item Item) bool {
	if o == nil {
		return true
	}
	if item.Path == "" {
		return true
	}
	return matchRules(o.EffectiveRules(), item)
}

// matchRules runs one pipeline: first matching rule wins, default keep.
func matchRules(rules []Rule, item Item) bool {
	for _, r := range rules {
		if r.Matcher != nil && r.Matcher.Match(item) {
			return r.Action == ActionInclude
		}
	}
	return true
}

// MatchPath reports whether the item at rel — a path relative to the transfer
// root — survives the pipeline, judged the way a transfer judges it.
//
// Match alone asks only about the item. That is not the whole question, because
// an excluded DIRECTORY takes everything beneath it: rsync never descends into
// one, so no rule below can rescue a file inside. Asking about the file alone
// would call those files present when the transfer will never send them, and a
// validation reading this would report them missing from the destination.
//
// So MatchPath also walks rel's ancestors. It walks them against the pipeline
// with the size rules removed, because rsync's size flags are file gates that
// never prune a directory — and a directory, carrying no size, would otherwise
// be dropped wholesale by a rule like "exclude smaller than 1KB" and take the
// entire tree with it.
//
// This is the verdict every caller wants: the transfer deciding what to copy,
// inspect previewing it, and validation reading it back all ask here, so the
// three agree without one of them keeping a hand-built copy of rsync's rules.
func (o *Option) MatchPath(rel string, size int64, isDir bool) bool {
	if o == nil {
		return true
	}
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return true
	}
	if !o.Match(Item{Path: rel, Name: path.Base(rel), Size: size, IsDir: isDir}) {
		return false
	}
	for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if !matchRules(o.directoryRules(), Item{Path: dir, Name: path.Base(dir), IsDir: true}) {
			return false
		}
	}
	return true
}

// directoryRules is the effective pipeline with every non-glob rule dropped —
// the pipeline MatchPath walks ancestors against.
func (o *Option) directoryRules() []Rule {
	o.dirOnce.Do(func() {
		for _, r := range o.EffectiveRules() {
			if _, ok := r.Matcher.(*globMatcher); ok {
				o.dirRules = append(o.dirRules, r)
			}
		}
	})
	return o.dirRules
}

// EffectiveRules returns the normalised pipeline: the rules as evaluated, in
// the order they are evaluated.
//
// It is the ONE place the pipeline is normalised. Both readers go through it —
// Match above, and the rsync translation in storagex — so the two cannot drift
// into disagreeing about order or about which rules are really there, which is
// what they did while each built its own list.
//
// Rules is taken as written. The simple Include/Exclude form is normalised into
// equivalent glob rules: excludes first (exclude wins), then — if any include
// exists — the includes followed by a catch-all exclude, which makes the include
// list a whitelist. Either way withDirPass then adds the traversal rule.
func (o *Option) EffectiveRules() []Rule {
	if o == nil {
		return nil
	}
	o.effectiveOnce.Do(func() { o.effective = o.buildEffective() })
	return o.effective
}

func (o *Option) buildEffective() []Rule {
	if len(o.Rules) > 0 {
		return withDirPass(o.Rules)
	}
	var rules []Rule
	for _, e := range o.Exclude {
		rules = append(rules, Rule{Action: ActionExclude, Matcher: &globMatcher{Pattern: e}})
	}
	if len(o.Include) > 0 {
		for _, i := range o.Include {
			rules = append(rules, Rule{Action: ActionInclude, Matcher: &globMatcher{Pattern: i}})
		}
		rules = append(rules, Rule{Action: ActionExclude, Matcher: &globMatcher{Pattern: "*"}})
	}
	return withDirPass(rules)
}

// withDirPass inserts "include */" — matching directories and nothing else —
// immediately before the catch-all of a whitelisting pipeline.
//
// A catch-all excludes directories as readily as files, and a directory that is
// excluded is never descended into, so "include *.go, exclude *" reaches no .go
// file at all: the whitelist shuts the door on itself. The traversal rule holds
// it open. Matching only directories, it changes no file's verdict.
//
// It sits just before the catch-all rather than at the head of the pipeline,
// which is where the rsync translation used to put it. First match wins, so the
// head is the highest priority there is: from there the rule outran the user's
// own directory excludes, and "exclude node_modules, include *.go" copied every
// .go file under node_modules. Placed last-but-one it rescues only what no rule
// of the user's had an opinion about.
//
// A pipeline with no include rule whitelists nothing and gets no traversal rule:
// there is no door to hold open, and adding one would start creating empty
// directories under a bare "exclude *".
func withDirPass(rules []Rule) []Rule {
	if !hasInclude(rules) {
		return rules
	}
	for i, r := range rules {
		if !isCatchAllExclude(r) {
			continue
		}
		out := make([]Rule, 0, len(rules)+1)
		out = append(out, rules[:i]...)
		out = append(out, Rule{Action: ActionInclude, Matcher: &globMatcher{Pattern: "*/"}})
		return append(out, rules[i:]...)
	}
	return rules
}

func hasInclude(rules []Rule) bool {
	for _, r := range rules {
		if r.Action == ActionInclude {
			return true
		}
	}
	return false
}

// isCatchAllExclude reports whether r drops everything that reaches it. Only a
// glob can: a size rule always lets some file through. Anything below such a
// rule is unreachable, so the first one found is the end of the pipeline.
func isCatchAllExclude(r Rule) bool {
	if r.Action != ActionExclude {
		return false
	}
	g, ok := r.Matcher.(*globMatcher)
	return ok && (g.Pattern == "*" || g.Pattern == "**")
}
