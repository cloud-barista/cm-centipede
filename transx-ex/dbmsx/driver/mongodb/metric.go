package mongodb

import base "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver/base"

// =============================================================================
// Resolved metric selections
// =============================================================================

// boolOr returns *p when set, otherwise def.
func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// DefaultSampleSize is the number of documents sampled for MongoDB field
// inference when MongoDBMetric.SampleSize is unset.
const DefaultSampleSize = 100

// mongoInspectMetric is the resolved MongoDB metric selection.
type mongoInspectMetric struct {
	rowCountExact bool
	columns       bool
	indexes       bool
	views         bool
	validators    bool
	sampleSize    int
}

func (m mongoInspectMetric) needPerCollectionScan() bool {
	return m.columns || m.indexes
}

func (m mongoInspectMetric) needSchemaScan() bool {
	return m.views || m.validators
}

// mongoMetric resolves loc.Metric.MongoDB into concrete flags. Toggles default
// false; sampleSize defaults to DefaultSampleSize.
func mongoMetric(loc base.DBMSLocation) mongoInspectMetric {
	if loc.Metric == nil || loc.Metric.MongoDB == nil {
		return mongoInspectMetric{sampleSize: DefaultSampleSize}
	}
	m := loc.Metric.MongoDB
	size := DefaultSampleSize
	if m.SampleSize != nil && *m.SampleSize > 0 {
		size = *m.SampleSize
	}
	return mongoInspectMetric{
		rowCountExact: boolOr(m.RowCountExact, false),
		columns:       boolOr(m.Columns, false),
		indexes:       boolOr(m.Indexes, false),
		views:         boolOr(m.Views, false),
		validators:    boolOr(m.Validators, false),
		sampleSize:    size,
	}
}
