package migration

import (
	"encoding/json"
	"fmt"

	"github.com/cloud-barista/cm-centipede/data-mig"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
)

// filterconv.go bridges centipede's library-free mirror filter types
// (commonmodel.PathFilterRule / commonmodel.DBMSFilterRule) to the concrete data-mig and
// dbmsx filter options. Both library rule types build their matchers through a
// type-keyed registry exposed only via JSON, so the mirror rules are round-
// tripped through JSON: the flat mirror structs serialise to exactly the
// {"action","type",…} / {"type",…} objects the registries decode.

// ToDataMigFilter converts mirror path-filter rules into a data-mig
// FilterOption. Returns (nil, nil) when there are no rules to apply.
//
// Exported because validation has to reach the same verdict this conversion
// feeds the transfer: a file the migration was told to skip must not be read
// back as missing. Sharing the converter is what keeps the two from drifting.
func ToDataMigFilter(rules []commonmodel.PathFilterRule) (*datamig.PathFilterOption, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	opt := &datamig.PathFilterOption{}
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("marshal path filter rules: %w", err)
	}
	if err := json.Unmarshal(raw, &opt.Rules); err != nil {
		return nil, fmt.Errorf("build data-mig filter rules: %w", err)
	}
	return opt, nil
}

// ToDBMSFilter is toDBMSFilter for a caller outside the migration — validation,
// which inspects the source through the very filter the migration dumped it with.
func ToDBMSFilter(rules []commonmodel.DBMSFilterRule) (*datamig.DBMSFilterOption, error) {
	return toDBMSFilter(rules)
}

// toDBMSFilter converts one database's mirror exclude rules into a
// datamig.DBMSFilterOption. Returns (nil, nil) when there are no rules, which
// migrates that database in full.
func toDBMSFilter(rules []commonmodel.DBMSFilterRule) (*datamig.DBMSFilterOption, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	opt := &datamig.DBMSFilterOption{}
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, fmt.Errorf("marshal dbms filter rules: %w", err)
	}
	if err := json.Unmarshal(raw, &opt.Rules); err != nil {
		return nil, fmt.Errorf("build dbms filter rules: %w", err)
	}
	return opt, nil
}
