package base

import (
	"fmt"
	"sort"
	"strings"
)

// =============================================================================
// Charset Profile
// =============================================================================

// CharsetProfile is what a migration needs to know about one endpoint's text
// handling, read in a couple of catalog queries. It is deliberately not part of
// DBMSInfo: Inspect walks every table for sizes and row counts, which is far
// more work than a compatibility check should cost, and a migration is handed
// only two locations — nothing about character sets reaches it in the request.
//
// A driver fills the whole struct without knowing which side it is describing.
// The source half and the target half are read from the same catalogs, so
// splitting the call in two would double the round trips to save nothing.
type CharsetProfile struct {
	// Encoding, Collation and Ctype are the database's own defaults: the
	// character set and collation on MySQL and MariaDB, and the encoding with
	// datcollate/datctype on PostgreSQL. MongoDB leaves all three empty.
	Encoding  string `json:"encoding,omitempty"`
	Collation string `json:"collation,omitempty"`
	Ctype     string `json:"ctype,omitempty"` // PostgreSQL only

	// UsedCharsets and UsedCollations are the names the dump will carry into the
	// target's DDL, gathered from the tables and columns the migration actually
	// covers — a name used only by a filtered-out table is not here. Both are
	// sorted and free of duplicates.
	//
	// They matter because a dump transports these as literal names. The target
	// parses them, and one it does not recognise fails the statement outright.
	UsedCharsets   []string `json:"usedCharsets,omitempty"`
	UsedCollations []string `json:"usedCollations,omitempty"`

	// AvailableCharsets and AvailableCollations are what the server recognises.
	// Sorted, and read from the catalog rather than inferred from a version
	// number: what settles a restore is whether the server knows the name, and
	// asking it directly needs no version table to be kept up to date.
	AvailableCharsets   []string `json:"availableCharsets,omitempty"`
	AvailableCollations []string `json:"availableCollations,omitempty"`
}

// SortUnique returns names sorted with duplicates and empty entries removed. It
// is what a driver runs over the raw catalog rows before filling a
// CharsetProfile, so every profile presents its lists the same way.
func SortUnique(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// =============================================================================
// Compatibility Diff
// =============================================================================

// Severity grades what a difference between two profiles means for the
// migration about to run.
type Severity string

const (
	// SeverityBlocker marks a difference that will fail the restore, or that
	// changes the data. The migration is stopped before the dump begins.
	SeverityBlocker Severity = "blocker"
	// SeverityWarning marks a difference that leaves the migration able to run
	// but changes how the target behaves afterwards. It is logged, not enforced.
	SeverityWarning Severity = "warning"
)

// CharsetIssue is one difference DiffCharset found.
type CharsetIssue struct {
	Severity Severity `json:"severity"`
	// Scope names what carries the difference: "database", or the object whose
	// DDL carries the unknown name.
	Scope  string `json:"scope"`
	Source string `json:"source"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

func (i CharsetIssue) String() string {
	return fmt.Sprintf("%s [%s] source=%q target=%q: %s",
		i.Severity, i.Scope, i.Source, i.Target, i.Detail)
}

// DiffCharset reports what stands between src and dst, most severe first.
//
// Two questions are asked, and only two. Does the target recognise every
// character set and collation name the dump will hand it — because a dump
// transports those as names, and an unknown one fails the statement that carries
// it. And, on PostgreSQL alone, does the target's encoding still hold the
// source's data — because PostgreSQL keeps the encoding on the database rather
// than in the DDL, so a dump cannot carry it and the target's own setting
// decides.
//
// It deliberately does not compare server versions. A version is a proxy for
// what the target supports; the catalog lists it outright.
//
// MongoDB yields nothing: it stores UTF-8 only, and a collection's default
// collation travels inside the dump's stored options rather than as a name the
// target has to recognise.
func DiffCharset(dbmsType string, src, dst *CharsetProfile) []CharsetIssue {
	if src == nil || dst == nil || dbmsType == DBMSTypeMongoDB {
		return nil
	}

	var issues []CharsetIssue

	// Names the dump carries that the target cannot parse.
	for _, name := range missingFrom(src.UsedCollations, dst.AvailableCollations) {
		issues = append(issues, CharsetIssue{
			Severity: SeverityBlocker,
			Scope:    "collation " + name,
			Source:   name,
			Target:   "(not supported)",
			Detail: "the target server does not recognise this collation, and the dump names it " +
				"in the DDL it carries; the restore fails on the first object that uses it",
		})
	}
	for _, name := range missingFrom(src.UsedCharsets, dst.AvailableCharsets) {
		issues = append(issues, CharsetIssue{
			Severity: SeverityBlocker,
			Scope:    "character set " + name,
			Source:   name,
			Target:   "(not supported)",
			Detail: "the target server does not recognise this character set, and the dump names " +
				"it in the DDL it carries; the restore fails on the first object that uses it",
		})
	}

	if dbmsType == DBMSTypePostgreSQL {
		issues = append(issues, pgEncodingIssues(src, dst)...)
	}

	// The database default reaches objects whose DDL does not state a collation
	// of its own. It never blocks — the migration runs either way.
	if src.Collation != "" && dst.Collation != "" && src.Collation != dst.Collation {
		issues = append(issues, CharsetIssue{
			Severity: SeverityWarning,
			Scope:    "database",
			Source:   src.Collation,
			Target:   dst.Collation,
			Detail: "the target database's default collation differs from the source's; objects " +
				"created without one of their own take the target's, which compares strings differently",
		})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Severity == SeverityBlocker && issues[j].Severity != SeverityBlocker
	})
	return issues
}

// pgEncodingIssues compares the two database encodings. PostgreSQL is the only
// engine where this matters: it records the encoding on the database, so a dump
// carries no encoding of its own and lands in whatever the target was created
// with.
//
// A target that is UTF8 holds anything, so widening into it is reported but not
// refused. Narrowing out of UTF8 is refused: the restore either fails on a
// character the target cannot represent or stores a replacement for it.
func pgEncodingIssues(src, dst *CharsetProfile) []CharsetIssue {
	if src.Encoding == "" || dst.Encoding == "" || strings.EqualFold(src.Encoding, dst.Encoding) {
		return nil
	}

	if strings.EqualFold(dst.Encoding, pgEncodingUTF8) {
		return []CharsetIssue{{
			Severity: SeverityWarning,
			Scope:    "database",
			Source:   src.Encoding,
			Target:   dst.Encoding,
			Detail: "the target database uses a different encoding; UTF8 represents everything " +
				"the source holds, so the data survives, but text arrives re-encoded",
		}}
	}

	return []CharsetIssue{{
		Severity: SeverityBlocker,
		Scope:    "database",
		Source:   src.Encoding,
		Target:   dst.Encoding,
		Detail: "the target database's encoding cannot represent everything the source's can; " +
			"recreate the target database with the source's encoding before migrating",
	}}
}

// pgEncodingUTF8 is the name PostgreSQL reports for its widest encoding, spelled
// as pg_encoding_to_char returns it.
const pgEncodingUTF8 = "UTF8"

// missingFrom returns the entries of used that available does not list, compared
// case-insensitively: MySQL reports collation names in lower case and
// PostgreSQL preserves whatever case a collation was created with, so an exact
// match would report names the server does in fact know.
func missingFrom(used, available []string) []string {
	if len(used) == 0 {
		return nil
	}
	have := make(map[string]bool, len(available))
	for _, a := range available {
		have[strings.ToLower(a)] = true
	}
	// An empty availability list means the driver does not enumerate that kind
	// of name — MongoDB, or an engine with no per-object character sets. Treating
	// it as "supports nothing" would block every migration.
	if len(have) == 0 {
		return nil
	}

	var missing []string
	for _, u := range used {
		if !have[strings.ToLower(u)] {
			missing = append(missing, u)
		}
	}
	return missing
}
