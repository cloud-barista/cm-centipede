package filter

import (
	"encoding/json"
	"testing"
)

func item(path string, size int64) Item {
	return Item{Path: path, Size: size}
}

func TestSimpleExcludeInclude(t *testing.T) {
	// exclude-only: everything passes except matches
	o := &Option{Exclude: []string{"tmp", "*.log"}}
	if o.Match(item("data/app.go", 0)) != true {
		t.Errorf("app.go should be included")
	}
	if o.Match(item("data/tmp", 0)) != false {
		t.Errorf("tmp should be excluded")
	}
	if o.Match(item("data/x.log", 0)) != false {
		t.Errorf("*.log should be excluded")
	}

	// include-mode: whitelist, only includes pass
	o = &Option{Include: []string{"*.go"}}
	if o.Match(item("a/main.go", 0)) != true {
		t.Errorf("*.go should be included")
	}
	if o.Match(item("a/readme.md", 0)) != false {
		t.Errorf("non-.go should be dropped in include-mode")
	}
}

func TestRulesFirstMatchWins(t *testing.T) {
	// Whitelist .go files at any depth (globMatch supports the "**/" prefix),
	// drop everything else via a catch-all.
	data := `{"rules":[
		{"action":"include","type":"glob","pattern":"**/*.go"},
		{"action":"exclude","type":"glob","pattern":"*"}
	]}`
	var o Option
	if err := json.Unmarshal([]byte(data), &o); err != nil {
		t.Fatal(err)
	}
	if !o.Match(item("project-a/src/main.go", 0)) {
		t.Errorf(".go file should be included by rule 1")
	}
	if o.Match(item("docs/guide.md", 0)) {
		t.Errorf("non-.go file should be excluded by catch-all")
	}
}

func TestSizeFilter(t *testing.T) {
	data := `{"rules":[
		{"action":"exclude","type":"size","op":">","value":10},
		{"action":"include","type":"glob","pattern":"*"}
	]}`
	var o Option
	if err := json.Unmarshal([]byte(data), &o); err != nil {
		t.Fatal(err)
	}
	if o.Match(item("big.bin", 20)) {
		t.Errorf("20 bytes (>10) should be excluded")
	}
	if !o.Match(item("small.bin", 5)) {
		t.Errorf("5 bytes should pass")
	}
}

func TestUnknownTypeError(t *testing.T) {
	data := `{"rules":[{"action":"exclude","type":"bogus","pattern":"x"}]}`
	var o Option
	if err := json.Unmarshal([]byte(data), &o); err == nil {
		t.Errorf("expected error for unknown filter type")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	data := `{"rules":[
		{"action":"exclude","type":"size","op":">","value":100},
		{"action":"include","type":"glob","pattern":"*.txt"}
	]}`
	var o Option
	if err := json.Unmarshal([]byte(data), &o); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(&o)
	if err != nil {
		t.Fatal(err)
	}
	var o2 Option
	if err := json.Unmarshal(out, &o2); err != nil {
		t.Fatalf("re-unmarshal failed: %v (marshaled: %s)", err, out)
	}
	if len(o2.Rules) != 2 {
		t.Fatalf("expected 2 rules after round-trip, got %d", len(o2.Rules))
	}
}

func TestRsyncTranslation(t *testing.T) {
	g := &globMatcher{Pattern: "*.log"}
	args, err := g.RsyncArgs(ActionExclude)
	if err != nil || len(args) != 1 || args[0] != "--exclude=*.log" {
		t.Errorf("glob exclude: got %v, %v", args, err)
	}

	s := &sizeMatcher{Op: ">", Value: 1024}
	args, err = s.RsyncArgs(ActionExclude)
	if err != nil || args[0] != "--max-size=1024" {
		t.Errorf("size exclude >: got %v, %v", args, err)
	}
	// size as include is not natively expressible
	if _, err := s.RsyncArgs(ActionInclude); err == nil {
		t.Errorf("size include should error for rsync")
	}
}

func TestNilOptionMatchesAll(t *testing.T) {
	var o *Option
	if !o.Match(item("anything", 0)) {
		t.Errorf("nil option should match everything")
	}
}
