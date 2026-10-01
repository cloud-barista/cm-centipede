package dbmsx

import (
	"context"
	"log/slog"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// =============================================================================
// Public API — Server Version
// =============================================================================

// ParseServerVersion reads the leading numeric components of a version string.
// See driver.ParseServerVersion.
var ParseServerVersion = drvpkg.ParseServerVersion

// CompareServerVersions orders two version strings by the components that decide
// a feature set on the engine. See driver.CompareServerVersions.
var CompareServerVersions = drvpkg.CompareServerVersions

// ServerVersion returns the version string loc's server identifies itself by,
// exactly as reported: "8.0.32" on MySQL, "10.6.14-MariaDB-1:10.6.14" on
// MariaDB, "14.23 (Ubuntu 14.23-1.pgdg22.04+1)" on PostgreSQL, "7.0.5" on
// MongoDB.
//
// It is a server-level call — one query, no table statistics — and loc.Database
// need not exist, so a target can be read before its first migration has created
// anything. Use ParseServerVersion to reduce a returned string to numbers.
func ServerVersion(loc DBMSLocation) (string, error) {
	if err := validateServerLocation("location", loc); err != nil {
		return "", err
	}
	driver, err := drvpkg.NewDBDriver(loc.DBMSType)
	if err != nil {
		return "", err
	}
	return driver.ServerVersion(context.Background(), loc)
}

// CheckVersion reports whether dst's server can accept a dump taken from src's.
// It reads both and changes neither.
//
// A migration runs this for itself before it starts, so calling it beforehand is
// not required — it exists for a caller that wants to show the answer while a
// migration is still being planned, rather than discover it at launch.
//
// The two locations must name the same engine: version numbers are only
// comparable within one, and a cross-engine migration is refused for its own
// reasons well before a version could matter.
func CheckVersion(src, dst DBMSLocation) (*VersionCompatibility, error) {
	if err := validateServerLocation("source", src); err != nil {
		return nil, err
	}
	if err := validateServerLocation("destination", dst); err != nil {
		return nil, err
	}
	if err := validateSameEngine(src, dst); err != nil {
		return nil, err
	}
	return checkVersion(context.Background(), src, dst)
}

// =============================================================================
// Internal
// =============================================================================

// checkVersion reads both endpoints and builds the verdict. Both locations are
// assumed validated.
func checkVersion(ctx context.Context, src, dst DBMSLocation) (*VersionCompatibility, error) {
	srcDriver, err := drvpkg.NewDBDriver(src.DBMSType)
	if err != nil {
		return nil, err
	}
	dstDriver, err := drvpkg.NewDBDriver(dst.DBMSType)
	if err != nil {
		return nil, err
	}

	srcVersion, err := srcDriver.ServerVersion(ctx, src)
	if err != nil {
		return nil, err
	}
	dstVersion, err := dstDriver.ServerVersion(ctx, dst)
	if err != nil {
		return nil, err
	}

	res := drvpkg.CheckServerVersions(src.DBMSType, srcVersion, dstVersion)
	return &res, nil
}

// enforceVersion is the migration's first pre-flight gate. A dump is written in
// the source server's dialect, so a target older than the source meets syntax it
// cannot parse — and only once the restore begins, after the whole dump has been
// taken. Two queries here replace that.
//
// It runs before enforceCharset because it is the cheaper and more fundamental
// question: an older target usually cannot accept the dump at all, whatever its
// collations say.
//
// Only a confirmed downgrade blocks. A version that cannot be read, or cannot be
// reduced to numbers, is reported and waived — the same stance enforceCharset
// takes, and for the same reason: the check is a guard, not the migration, and
// must not stop one that would otherwise run.
func enforceVersion(ctx context.Context, dmm DBMSMigrationModel) error {
	if dmm.SkipVersionCheck {
		return nil
	}

	res, err := checkVersion(ctx, dmm.Source, dmm.Destination)
	if err != nil {
		logger.Warn("version check skipped",
			slog.String("dbms", dmm.Destination.DBMSType),
			slog.String("database", dmm.Destination.Database),
			slog.String("err", err.Error()))
		return nil
	}
	if !res.Comparable {
		logger.Warn("version check skipped: versions are not comparable",
			slog.String("dbms", dmm.Destination.DBMSType),
			slog.String("source", res.Source),
			slog.String("target", res.Target))
		return nil
	}
	if !res.Downgrade {
		return nil
	}
	return &drvpkg.VersionDowngradeError{
		DBMSType:      dmm.Destination.DBMSType,
		Database:      dmm.Destination.Database,
		SourceVersion: res.Source,
		TargetVersion: res.Target,
	}
}
