package dbmsx

import (
	"context"
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Public API — Charset Compatibility
// =============================================================================

// CheckCharset reports what stands between the character-set handling of two
// endpoints, most severe first. It reads both and changes neither.
//
// A migration runs this for itself before it starts, so calling it beforehand is
// not required — it exists for a caller that wants to show the answer while a
// migration is still being planned, rather than discover it at launch.
//
// The two locations must name the same engine: a dump carries collation names in
// the DDL it writes, and what one engine calls a collation has no counterpart in
// another's catalog, so there is nothing to compare across engines.
func CheckCharset(src, dst DBMSLocation) ([]CharsetIssue, error) {
	if err := validateLocation("source", src); err != nil {
		return nil, err
	}
	if err := validateLocation("destination", dst); err != nil {
		return nil, err
	}
	if err := validateSameEngine(src, dst); err != nil {
		return nil, err
	}
	return checkCharset(context.Background(), src, dst)
}

// =============================================================================
// Internal
// =============================================================================

// checkCharset profiles both endpoints and diffs them. Both locations are
// assumed validated.
func checkCharset(ctx context.Context, src, dst DBMSLocation) ([]CharsetIssue, error) {
	srcDriver, err := drvpkg.NewDBDriver(src.DBMSType)
	if err != nil {
		return nil, err
	}
	dstDriver, err := drvpkg.NewDBDriver(dst.DBMSType)
	if err != nil {
		return nil, err
	}

	srcProfile, err := srcDriver.CharsetProfile(ctx, src)
	if err != nil {
		return nil, err
	}
	dstProfile, err := dstDriver.CharsetProfile(ctx, dst)
	if err != nil {
		return nil, err
	}
	return drvpkg.DiffCharset(src.DBMSType, srcProfile, dstProfile), nil
}

// enforceCharset is the migration's pre-flight gate. It runs once the target is
// known to be empty and before any data is read, so a target that cannot accept
// the source's names costs a few catalog queries instead of a full dump.
//
// Blocking issues abort the migration; warnings are logged and it continues. A
// warning describes a target that behaves differently afterwards, which is the
// operator's call to make, not the library's.
//
// A ScopeDataOnly migration is skipped: it emits no DDL, so there is no
// collation name for the target to reject.
func enforceCharset(ctx context.Context, dmm DBMSMigrationModel) error {
	if dmm.SkipCharsetCheck || dmm.Scope == ScopeDataOnly {
		return nil
	}

	issues, err := checkCharset(ctx, dmm.Source, dmm.Destination)
	if err != nil {
		// The check is a guard, not the migration. Failing to read a catalog —
		// an account without access to information_schema, say — must not stop a
		// migration that would otherwise run, so this is reported and waived.
		logger.Warn("charset check skipped",
			slog.String("dbms", dmm.Destination.DBMSType),
			slog.String("database", dmm.Destination.Database),
			slog.String("err", err.Error()))
		return nil
	}

	var blockers []CharsetIssue
	for _, issue := range issues {
		if issue.Severity == SeverityBlocker {
			blockers = append(blockers, issue)
			continue
		}
		logger.Warn("charset difference",
			slog.String("scope", issue.Scope),
			slog.String("source", issue.Source),
			slog.String("target", issue.Target),
			slog.String("detail", issue.Detail))
	}
	if len(blockers) == 0 {
		return nil
	}
	return &drvpkg.CharsetIncompatibleError{
		DBMSType: dmm.Destination.DBMSType,
		Database: dmm.Destination.Database,
		Issues:   blockers,
	}
}
