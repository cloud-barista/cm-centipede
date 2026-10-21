package beetle

import (
	"fmt"
	"strings"
)

// charsetPolicy is what one engine takes of the source's character set when a
// logical database is created through cm-beetle, per CSP. It mirrors cm-beetle's
// recommendation step (resolveTargetDatabases), which the logical database API
// itself does not apply: that API passes both values straight to cb-spider.
//
// A provider absent from both maps takes both values as given.
type charsetPolicy struct {
	// omitAll lists the providers that take neither value, with the reason.
	omitAll map[string]string
	// omitCollation lists the providers that take the charset alone, with the
	// reason. The engine then picks that charset's default collation.
	omitCollation map[string]string
}

// Reasons shared by more than one engine.
const (
	reasonNCPSQL = "sending them switches NCP to SQL creation, which needs SQL connectivity from cb-spider into the VPC"
	reasonNHNSQL = "sending them switches NHN to SQL creation, which needs 'Direct Control' enabled in the console"
)

// charsetPolicies holds one entry per engine. Adding an engine means adding an
// entry here; an engine without one sends neither value.
var charsetPolicies = map[string]charsetPolicy{
	"mysql": {
		omitAll: map[string]string{
			"ncp": reasonNCPSQL,
			"nhn": reasonNHNSQL,
		},
	},
	"mariadb": {
		omitAll: map[string]string{
			"ncp": reasonNCPSQL,
			"nhn": reasonNHNSQL,
		},
		omitCollation: map[string]string{
			"alibaba": "Alibaba MariaDB accepts a charset only",
		},
	},
}

// databaseCharsetFor trims the source character set to what the instance's
// engine and CSP take (see charsetPolicies). warning is empty when nothing was
// dropped.
func databaseCharsetFor(provider, engine, charset, collation string) (outCharset, outCollation, warning string) {
	charset, collation = strings.TrimSpace(charset), strings.TrimSpace(collation)
	if charset == "" && collation == "" {
		return "", "", ""
	}

	policy, ok := charsetPolicies[strings.ToLower(strings.TrimSpace(engine))]
	if !ok {
		return "", "", fmt.Sprintf("omitted charset/collation: no charset policy for engine %q", engine)
	}

	p := strings.ToLower(strings.TrimSpace(provider))
	if reason, ok := policy.omitAll[p]; ok {
		return "", "", "omitted charset/collation: " + reason
	}
	if reason, ok := policy.omitCollation[p]; ok && collation != "" {
		return charset, "", fmt.Sprintf("omitted collation %q: %s", collation, reason)
	}
	return charset, collation, ""
}
