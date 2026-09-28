package filter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

func init() {
	Register("glob", newGlobMatcher)
}

// globMatcher matches an item's path against a glob pattern.
type globMatcher struct {
	Pattern string `json:"pattern"`
}

func newGlobMatcher(raw json.RawMessage) (Matcher, error) {
	var c struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Pattern == "" {
		return nil, fmt.Errorf("glob: pattern is required")
	}
	return &globMatcher{Pattern: c.Pattern}, nil
}

func (g *globMatcher) Type() string { return "glob" }

func (g *globMatcher) Match(item Item) bool {
	return globMatch(g.Pattern, item.Path, item.IsDir)
}

// RsyncArgs maps the glob rule to an ordered rsync --include/--exclude flag.
func (g *globMatcher) RsyncArgs(action string) ([]string, error) {
	switch action {
	case ActionInclude:
		return []string{"--include=" + g.Pattern}, nil
	case ActionExclude:
		return []string{"--exclude=" + g.Pattern}, nil
	default:
		return nil, fmt.Errorf("glob: unknown action %q", action)
	}
}

// globMatch reports whether path matches pattern under rsync's filter-rule
// semantics. path is relative to the transfer root — the same string rsync
// matches its own patterns against — so that a rule reaches the same verdict
// here and in a transfer rsync runs.
//
// The rules, each verified against rsync 3.2.7:
//
//	pattern           matches
//	*.log             the base name, at any depth ("a/b/x.log")
//	src/*.go          the path TAIL, so "a/src/main.go" matches too
//	/src/*.go         anchored at the root, so only "src/main.go"
//	**/x.txt          zero or more leading directories, so "x.txt" matches
//	a/**/x.txt        at least one directory between, so "a/x.txt" does NOT
//	a/**              everything beneath "a"
//	logs/             directories only
//
// A pattern with no "/" is matched against the base name; one with a "/" is
// matched against the path, anchored only when it starts with "/". Both fall
// out of the leading "(?:.*/)?" the compiler emits for an unanchored pattern.
func globMatch(pattern, path string, isDir bool) bool {
	re, dirOnly, err := compileGlob(pattern)
	if err != nil {
		return false
	}
	if dirOnly && !isDir {
		return false
	}
	return re.MatchString(strings.Trim(path, "/"))
}

// compiled caches the regexp built for a pattern. Filter rules are evaluated
// once per file, so a scan of a large tree compiles each pattern once rather
// than once per item.
var compiled sync.Map // pattern string -> *compiledGlob

type compiledGlob struct {
	re      *regexp.Regexp
	dirOnly bool
	err     error
}

func compileGlob(pattern string) (*regexp.Regexp, bool, error) {
	if v, ok := compiled.Load(pattern); ok {
		c := v.(*compiledGlob)
		return c.re, c.dirOnly, c.err
	}
	re, dirOnly, err := buildGlobRegexp(pattern)
	compiled.Store(pattern, &compiledGlob{re: re, dirOnly: dirOnly, err: err})
	return re, dirOnly, err
}

// buildGlobRegexp translates one glob pattern into an anchored regexp.
func buildGlobRegexp(pattern string) (*regexp.Regexp, bool, error) {
	// A trailing "/" restricts the rule to directories and takes no part in the
	// path comparison: rsync's "logs/" means the directory "logs", not "logs/".
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	// A leading "/" anchors the rule at the transfer root and is likewise not
	// part of the comparison. It is stripped here, before the trailing-slash
	// trim above could be mistaken for it.
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return nil, false, fmt.Errorf("glob: empty pattern")
	}

	var b strings.Builder
	b.WriteString("^")
	// An unanchored pattern matches the tail of the path, which is also what
	// makes a "/"-free pattern match the base name at any depth. An anchored
	// one skips this and starts at the root.
	if !anchored {
		b.WriteString(`(?:.*/)?`)
	}

	for i := 0; i < len(pattern); {
		atStart := i == 0
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// "**" crosses directory separators. As the prefix of the whole
				// pattern "**/" also matches zero directories, which is rsync's
				// FILTRULE_WILD2_PREFIX case; anywhere else the "/" is literal
				// and at least one directory has to be there.
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					i++
					if atStart {
						b.WriteString(`(?:.*/)?`)
					} else {
						b.WriteString(`.*/`)
					}
					continue
				}
				b.WriteString(`.*`)
				continue
			}
			b.WriteString(`[^/]*`)
			i++
		case '?':
			b.WriteString(`[^/]`)
			i++
		case '[':
			class, next, ok := globCharClass(pattern, i)
			if !ok {
				b.WriteString(regexp.QuoteMeta(string(c)))
				i++
				continue
			}
			b.WriteString(class)
			i = next
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, false, fmt.Errorf("glob: cannot compile pattern %q: %w", pattern, err)
	}
	return re, dirOnly, nil
}

// globCharClass copies a "[...]" class starting at pattern[i] into regexp form,
// returning the translated class and the index just past it. ok is false when
// the bracket is unterminated, which leaves it to be treated as a literal.
func globCharClass(pattern string, i int) (class string, next int, ok bool) {
	j := i + 1
	if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
		j++
	}
	// A "]" in the first position is a literal member of the class.
	if j < len(pattern) && pattern[j] == ']' {
		j++
	}
	for j < len(pattern) && pattern[j] != ']' {
		j++
	}
	if j >= len(pattern) {
		return "", i, false
	}
	body := pattern[i+1 : j]
	if strings.HasPrefix(body, "!") {
		body = "^" + body[1:]
	}
	return "[" + body + "]", j + 1, true
}
