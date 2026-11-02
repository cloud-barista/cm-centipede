package filter

import (
	"encoding/json"
	"fmt"
)

// Factory builds a Matcher from the raw JSON of a rule object. The raw message
// contains the whole rule (action, type, and matcher-specific fields), so a
// factory decodes only the fields it needs.
type Factory func(raw json.RawMessage) (Matcher, error)

// registry maps a matcher "type" to its factory. Built-in matchers register in
// their own init(); callers add custom types with Register.
var registry = map[string]Factory{}

// Register makes a matcher type available to the JSON decoder. It is intended
// to be called from init() and is not safe for concurrent use with decoding.
func Register(typ string, factory Factory) {
	registry[typ] = factory
}

// Rule is one entry of the filter pipeline: an action plus the matcher that
// decides whether the action applies. The original JSON is retained so the rule
// round-trips through json.Marshal unchanged (used by field encryption).
type Rule struct {
	Action  string
	Matcher Matcher

	raw json.RawMessage
}

// UnmarshalJSON decodes {"action","type",...} by dispatching on "type" to the
// registered factory.
func (r *Rule) UnmarshalJSON(data []byte) error {
	var head struct {
		Action string `json:"action"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if head.Action != ActionInclude && head.Action != ActionExclude {
		return fmt.Errorf("filter: invalid action %q (want include|exclude)", head.Action)
	}
	factory, ok := registry[head.Type]
	if !ok {
		return fmt.Errorf("filter: unknown filter type %q", head.Type)
	}
	m, err := factory(data)
	if err != nil {
		return fmt.Errorf("filter: %q: %w", head.Type, err)
	}
	r.Action = head.Action
	r.Matcher = m
	r.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON re-emits the original JSON when available (config-loaded rules),
// otherwise reconstructs {"action","type",...matcher fields} for rules built in
// code.
func (r Rule) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return r.raw, nil
	}
	if r.Matcher == nil {
		return nil, fmt.Errorf("filter: rule has no matcher")
	}
	mb, err := json.Marshal(r.Matcher)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(mb, &fields); err != nil {
		return nil, err
	}
	fields["action"], _ = json.Marshal(r.Action)
	fields["type"], _ = json.Marshal(r.Matcher.Type())
	return json.Marshal(fields)
}
