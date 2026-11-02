package base

import (
	"strconv"
	"strings"
)

// =============================================================================
// Server Version Comparison
// =============================================================================

// ParseServerVersion extracts the leading numeric components of a server version
// string. It stops at the first separator that introduces vendor text, and at
// the first component that is not a number, so the forms the engines report here
// reduce to the same shape:
//
//	"8.0.32"                      → [8 0 32]   MySQL, SELECT VERSION()
//	"10.6.14-MariaDB-1:10.6.14"   → [10 6 14]  MariaDB, SELECT VERSION()
//	"14.23 (Ubuntu 14.23-1)"      → [14 23]    PostgreSQL, SHOW server_version
//	"7.0.5"                       → [7 0 5]    MongoDB, buildInfo
//
// A string with no leading number yields nil — "PostgreSQL 14.23 on x86_64",
// what SELECT version() returns, is the case this guards. Callers read nil as
// "cannot compare" rather than as a version of zero, so an unreadable version
// never silently becomes the oldest one.
func ParseServerVersion(s string) []int {
	if i := strings.IndexAny(s, " -+~_"); i >= 0 {
		s = s[:i]
	}
	var parts []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

// significantVersionDepth returns how many leading components decide whether one
// server can accept another's dump — which is not the same question as which
// version string is numerically larger.
//
// Everywhere but PostgreSQL a release is MAJOR.MINOR.PATCH and features arrive
// with the minor, so the first two components decide. PostgreSQL from 10 onwards
// numbers releases MAJOR.PATCH — 14.2 and 14.5 are the same feature set, months
// apart — so only the major counts; 9.x and earlier used MAJOR.MAJOR.PATCH,
// where the first two do again.
//
// The effect is that a patch-level difference never blocks a migration. Refusing
// 8.0.35 → 8.0.32, or PostgreSQL 14.5 → 14.2, would be a false alarm: a patch
// release changes nothing about the DDL a dump carries.
func significantVersionDepth(dbmsType string, version []int) int {
	if dbmsType == DBMSTypePostgreSQL && len(version) > 0 && version[0] >= 10 {
		return 1
	}
	return 2
}

// CompareServerVersions orders two version strings by the components that matter
// for dbmsType: negative when a precedes b, zero when they are equivalent,
// positive when a follows b. The second return is false when either string
// carries no leading number, in which case nothing was compared and the int
// means nothing.
//
// Comparison stops at the shallower of the two, so "14" and "14.2" are
// equivalent on PostgreSQL instead of the absent component counting as zero.
func CompareServerVersions(dbmsType, a, b string) (int, bool) {
	pa, pb := ParseServerVersion(a), ParseServerVersion(b)
	if len(pa) == 0 || len(pb) == 0 {
		return 0, false
	}

	depth := significantVersionDepth(dbmsType, pa)
	for _, n := range []int{significantVersionDepth(dbmsType, pb), len(pa), len(pb)} {
		if n < depth {
			depth = n
		}
	}

	for i := 0; i < depth; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1, true
		case pa[i] > pb[i]:
			return 1, true
		}
	}
	return 0, true
}

// =============================================================================
// VersionCompatibility
// =============================================================================

// VersionCompatibility is what CheckVersion reports about a source/target pair:
// the two versions as their servers state them, and the verdict drawn from them.
//
// Comparable is false when either server's version could not be read as a
// number. The pair is then reported rather than judged — Downgrade stays false,
// and a migration proceeds — because a version this library cannot parse is not
// evidence of anything.
type VersionCompatibility struct {
	DBMSType string `json:"dbmsType"`
	// Source and Target are the version strings as reported, vendor suffixes and
	// all, so a caller can show the operator what the servers actually said.
	Source string `json:"source"`
	Target string `json:"target"`
	// Comparable reports whether both strings yielded a number to compare.
	Comparable bool `json:"comparable"`
	// Downgrade reports that the target precedes the source at the depth that
	// matters for this engine. It is the one condition a migration refuses.
	Downgrade bool `json:"downgrade"`
}

// CheckServerVersions builds the verdict for one source/target pair. It performs
// no I/O: the caller supplies the two version strings.
func CheckServerVersions(dbmsType, source, target string) VersionCompatibility {
	res := VersionCompatibility{DBMSType: dbmsType, Source: source, Target: target}
	cmp, ok := CompareServerVersions(dbmsType, source, target)
	res.Comparable = ok
	res.Downgrade = ok && cmp > 0
	return res
}
