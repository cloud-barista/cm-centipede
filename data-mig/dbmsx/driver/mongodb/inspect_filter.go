package mongodb

// inspect_filter.go — the schema-object inventory as a filtered dump leaves it.
//
// When loc.Filter is set, Inspect narrows its views and validators the way
// dumpSchemaViaDriver narrows what it writes, so an inventory of the source
// reads the same as one of a target the filtered dump was restored into:
//
//	views       a wholly excluded collection name, or a view rule naming it
//	validators  on a wholly excluded collection, or a validator rule naming it
//	            (the collection itself migrates, without its validator)
//
// Both lists hold collection names, so the decision needs nothing more than the
// name. The collection list is left whole: which collections a filter drops is
// the filter's own statement, and a caller reads it from there.

import (
	base "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/driver/base"
	filterpkg "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/filter"
)

// mongoKeepNamed keeps the names a filtered dump still writes for kind.
func mongoKeepNamed(filter *filterpkg.DBMSFilterOption, kind string, names []string) []string {
	if names == nil {
		return nil
	}
	kept := make([]string, 0, len(names))
	for _, n := range names {
		if mongoIncludeCollection(n, filter) && !filter.IsObjectExcluded(kind, n) {
			kept = append(kept, n)
		}
	}
	return kept
}

// mongoFilterSchemaObjects narrows info's views and validators by filter.
func mongoFilterSchemaObjects(filter *filterpkg.DBMSFilterOption, info *base.DBMSInfo) {
	info.Views = mongoKeepNamed(filter, filterpkg.ObjectKindView, info.Views)
	info.Validators = mongoKeepNamed(filter, filterpkg.ObjectKindValidator, info.Validators)
}
