package migration

import (
	"encoding/json"
	"fmt"

	"github.com/cloud-barista/cm-centipede/transx-ex"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
)

// filterconv.go bridges centipede's library-free mirror filter types
// (commonmodel.PathFilterRule / commonmodel.DBMSFilterRule) to the concrete transx-ex and
// dbmsx filter options. Both library rule types build their matchers through a
// type-keyed registry exposed only via JSON, so the mirror rules are round-
// tripped through JSON: the flat mirror structs serialise to exactly the
// {"action","type",…} / {"type",…} objects the registries decode.

// ToTransxFilter converts mirror path-filter rules into a transx-ex
// FilterOption. Returns (nil, nil) when there are no rules to apply.
//
// Exported because validation has to reach the same verdict this conversion
// feeds the transfer: a file the migration was told to skip must not be read
// back as missing. Sharing the converter is what keeps the two from drifting.
func ToTransxFilter(rules []commonmodel.PathFilterRule) (*transxex.PathFilterOption, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	opt := &transxex.PathFilterOption{}
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("marshal path filter rules: %w", err)
	}
	if err := json.Unmarshal(raw, &opt.Rules); err != nil {
		return nil, fmt.Errorf("build transx filter rules: %w", err)
	}
	return opt, nil
}

// toDBMSFilter converts one database's mirror exclude rules into a
// transxex.DBMSFilterOption. Returns (nil, nil) when there are no rules, which
// migrates that database in full.
func toDBMSFilter(rules []commonmodel.DBMSFilterRule) (*transxex.DBMSFilterOption, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	opt := &transxex.DBMSFilterOption{}
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("marshal dbms filter rules: %w", err)
	}
	if err := json.Unmarshal(raw, &opt.Rules); err != nil {
		return nil, fmt.Errorf("build dbms filter rules: %w", err)
	}
	return opt, nil
}
