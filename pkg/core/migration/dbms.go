package migration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
	"github.com/rs/zerolog/log"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/client/beetle"
	"github.com/cloud-barista/cm-centipede/pkg/client/honeybee"
	"github.com/cloud-barista/cm-centipede/pkg/connsec"
)

// DBConnConfigToDBMSLocation converts the unified DBConnConfig (the type all DB
// sources resolve into) to a transxex.DBMSLocation.
//
// DBConnConfig carries server-level access only, so the database name is a
// separate argument: which databases to migrate is a decision of the migration,
// not a property of the connection.
func DBConnConfigToDBMSLocation(cfg commonmodel.DBConnConfig, database string) (transxex.DBMSLocation, error) {
	switch cfg.AccessType {
	case "sshTunnel":
		if cfg.SSHTunnel == nil {
			return transxex.DBMSLocation{}, fmt.Errorf("db config: sshTunnel is required for accessType=sshTunnel")
		}
		tunnel := &transxex.SSHTunnelConfig{
			SSH: &transxex.SSHConfig{
				Host:       cfg.SSHTunnel.Host,
				Port:       cfg.SSHTunnel.Port,
				Username:   cfg.SSHTunnel.Username,
				PrivateKey: cfg.SSHTunnel.PrivateKey,
			},
			DBHost:     cfg.Host,
			DBPort:     cfg.Port,
			Username:   cfg.Username,
			Password:   cfg.Password,
			AuthSource: cfg.AuthSource,
		}
		loc := DBMSLocationSSHTunnel(cfg.DBMSType, database, tunnel)
		loc.ProviderName = cfg.ProviderName
		return loc, nil
	default: // direct
		direct := &transxex.DirectConfig{
			Host:           cfg.Host,
			Port:           cfg.Port,
			Username:       cfg.Username,
			Password:       cfg.Password,
			ConnectTimeout: cfg.ConnectTimeout,
			TLSMode:        cfg.TLSMode,
			TLSCAPEM:       cfg.TLSCAPEM,
			AuthSource:     cfg.AuthSource,
		}
		loc := DBMSLocationDirect(cfg.DBMSType, database, direct)
		loc.ProviderName = cfg.ProviderName
		return loc, nil
	}
}

// DBMSLocationDirect builds a transxex.DBMSLocation for a direct TCP connection.
func DBMSLocationDirect(dbType, database string, direct *transxex.DirectConfig) transxex.DBMSLocation {
	return transxex.DBMSLocation{
		DBMSType:   dbType,
		Database:   database,
		AccessType: transxex.AccessTypeDirect,
		Direct:     direct,
	}
}

// DBMSLocationSSHTunnel builds a transxex.DBMSLocation for an SSH-tunnelled connection.
func DBMSLocationSSHTunnel(dbType, database string, tunnel *transxex.SSHTunnelConfig) transxex.DBMSLocation {
	return transxex.DBMSLocation{
		DBMSType:   dbType,
		Database:   database,
		AccessType: transxex.AccessTypeSSHTunnel,
		SSHTunnel:  tunnel,
	}
}

// DBMSLocationWithFilter attaches a DBMSFilterOption (table/column/row/object
// exclusions) to an existing DBMSLocation. Returns a copy with Filter set.
func DBMSLocationWithFilter(loc transxex.DBMSLocation, f *transxex.DBMSFilterOption) transxex.DBMSLocation {
	loc.Filter = f
	return loc
}

// splitPgSchemas turns the name pairs carried by a migration item into the two
// positional slices transx-ex matches against each other.
//
// When no pair renames anything the destination slice is left empty: transx-ex
// reads an empty destination as "reproduce the source names as they are", which
// keeps a same-name migration clear of the restriction that blocks a rename when
// both sides use an SSH tunnel.
func splitPgSchemas(pairs []commonmodel.PgSchemaMapping) (src, dst []string) {
	if len(pairs) == 0 {
		return nil, nil
	}
	renamed := false
	src = make([]string, 0, len(pairs))
	dst = make([]string, 0, len(pairs))
	for _, p := range pairs {
		to := p.DstName
		if to == "" {
			to = p.SrcName
		}
		if to != p.SrcName {
			renamed = true
		}
		src = append(src, p.SrcName)
		dst = append(dst, to)
	}
	if !renamed {
		return src, nil
	}
	return src, dst
}

// HoneybeeToDBMSLocation maps a honeybee DBMS config to a transxex.DBMSLocation.
// When the honeybee connection has SSH tunnel info (db_access_type = "ssh-tunnel"),
// an SSH-tunnelled DBMSLocation is returned; otherwise a direct one is returned.
func HoneybeeToDBMSLocation(cfg honeybee.HoneybeeConnConfig, database string) transxex.DBMSLocation {
	if cfg.SSHTunnelHost != "" {
		sshCfg := &transxex.SSHConfig{
			Host:       cfg.SSHTunnelHost,
			Port:       cfg.SSHTunnelPort,
			Username:   cfg.SSHTunnelUser,
			PrivateKey: cfg.SSHTunnelPrivateKey,
		}
		tunnel := &transxex.SSHTunnelConfig{
			SSH:        sshCfg,
			DBHost:     cfg.DBHost,
			DBPort:     cfg.DBPort,
			Username:   cfg.DBUsername,
			Password:   cfg.DBPassword,
			AuthSource: cfg.DBAuthSource,
		}
		return DBMSLocationSSHTunnel(cfg.DBType, database, tunnel)
	}
	direct := &transxex.DirectConfig{
		Host:       cfg.DBHost,
		Port:       cfg.DBPort,
		Username:   cfg.DBUsername,
		Password:   cfg.DBPassword,
		TLSMode:    cfg.DBTLSMode,
		TLSCAPEM:   cfg.DBTLSCAPEM,
		AuthSource: cfg.DBAuthSource,
	}
	return DBMSLocationDirect(cfg.DBType, database, direct)
}

// InspectDB returns server and schema metadata for the database described by loc.
func InspectDB(loc transxex.DBMSLocation) (*transxex.DBMSInfo, error) {
	info, err := transxex.InspectDBMS(loc)
	if err != nil {
		return nil, fmt.Errorf("inspect %s database %s: %w", loc.DBMSType, loc.Database, err)
	}
	return info, nil
}

// ResolveDBMSLocation maps a ConnectionRef to a transxex.DBMSLocation.
// database is the database name to operate on (from the migration item).
// dbType is the engine hint; empty falls back to the value resolved from the source.
//
// honeybee resolves via its own connection info; beetleDb is a managed RDB whose
// host/port/user/engine would be fetched from cm-beetle; db is inline
// credentials. Inline credentials and ref passwords are decrypted first.
func ResolveDBMSLocation(ref commonmodel.ConnectionRef, database, dbType string) (transxex.DBMSLocation, error) {
	ref, err := connsec.DecryptRef(ref)
	if err != nil {
		return transxex.DBMSLocation{}, fmt.Errorf("decrypt connection ref: %w", err)
	}

	switch ref.Source {
	case commonmodel.ConnectionSourceHoneybee:
		if ref.Honeybee == nil {
			return transxex.DBMSLocation{}, fmt.Errorf("honeybee ref is nil")
		}
		cfg, err := honeybee.GetConnectionInfo(*ref.Honeybee)
		if err != nil {
			return transxex.DBMSLocation{}, fmt.Errorf("get honeybee connection: %w", err)
		}
		db := database
		if db == "" {
			db = cfg.Database
		}
		return HoneybeeToDBMSLocation(*cfg, db), nil

	case commonmodel.ConnectionSourceBeetleDB:
		if ref.BeetleDB == nil {
			return transxex.DBMSLocation{}, fmt.Errorf("beetleDb ref is nil")
		}
		// cm-beetle reports host/port/user/engine for the managed instance; the
		// password comes from the ref, since the lookup never returns one.
		// Access is always direct.
		acc, err := beetle.GetRDBMSAccessInfo(*ref.BeetleDB)
		if err != nil {
			return transxex.DBMSLocation{}, err
		}
		engine := pick(dbType, acc.DBType)
		if err := validateManagedEngine(engine); err != nil {
			return transxex.DBMSLocation{}, err
		}
		return DBConnConfigToDBMSLocation(
			managedDBConnConfig(engine, acc, *ref.BeetleDB), database)

	case commonmodel.ConnectionSourceDB:
		if ref.DB == nil {
			return transxex.DBMSLocation{}, fmt.Errorf("db config is nil")
		}
		return DBConnConfigToDBMSLocation(*ref.DB, database)

	default:
		return transxex.DBMSLocation{}, fmt.Errorf("unsupported source type for DBMS migration: %s", ref.Source)
	}
}

// validateManagedEngine rejects an engine cm-beetle cannot be holding.
// cb-tumblebug provisions MySQL and MariaDB only, so a beetleDb reference to a
// PostgreSQL or MongoDB server is a mistake worth naming rather than a
// connection to attempt.
func validateManagedEngine(engine string) error {
	switch strings.ToLower(engine) {
	case string(commonmodel.DBMSTypeMySQL), string(commonmodel.DBMSTypeMariaDB):
		return nil
	case "":
		return fmt.Errorf("beetleDb: cm-beetle reported no database engine")
	default:
		return fmt.Errorf(
			"beetleDb: engine %q is not available as a managed instance (cm-beetle provisions %s and %s only); "+
				"use an inline db connection instead",
			engine, commonmodel.DBMSTypeMySQL, commonmodel.DBMSTypeMariaDB)
	}
}

// managedDBConnConfig builds a DBConnConfig for a managed cloud RDB (beetleDb):
// direct access, with the endpoint and admin user from cm-beetle and the
// settings the lookup cannot supply — the password and the TLS choice — from the
// ref.
//
// ProviderName is left empty on purpose. cm-beetle's RDBMS read does not report
// which CSP holds the instance, and the field decides nothing (transx-ex carries
// it into DBMSLocation.ProviderName but never reads it), so naming a provider
// here would mean inventing one.
func managedDBConnConfig(engine string, acc *beetle.DBConfig, ref commonmodel.BeetleDBRef) commonmodel.DBConnConfig {
	return commonmodel.DBConnConfig{
		DBMSType:   engine,
		AccessType: "direct",
		Host:       acc.Host,
		Port:       acc.Port,
		Username:   pick(ref.Username, acc.Username),
		Password:   ref.Password,
		TLSMode:    ref.TLSMode,
		TLSCAPEM:   ref.TLSCAPEM,
	}
}

// pick returns primary when non-empty, otherwise fallback.
func pick(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// ── Target database ownership ────────────────────────────────────────────────

// TargetDatabaseExists reports whether the destination database is already there.
//
// The answer comes from whoever made the database: cm-beetle for a managed
// instance, the database connection itself for everything else. Asking the other
// one invites a split verdict, and that verdict is what decides whether centipede
// owns the database and has to drop it after a failure.
func TargetDatabaseExists(dst commonmodel.ConnectionRef, dstLoc transxex.DBMSLocation) (bool, error) {
	if dst.Source == commonmodel.ConnectionSourceBeetleDB {
		if dst.BeetleDB == nil {
			return false, fmt.Errorf("beetleDb ref is nil")
		}
		// The password travels to beetle as the admin password, so it has to be
		// plaintext by now. DecryptRef passes an already-plaintext value through.
		ref, err := connsec.DecryptRef(dst)
		if err != nil {
			return false, fmt.Errorf("decrypt connection ref: %w", err)
		}
		names, err := beetle.ListDatabases(*ref.BeetleDB)
		if err != nil {
			return false, fmt.Errorf("list target databases: %w", err)
		}
		return containsName(names, dstLoc.Database), nil
	}

	names, err := transxex.ListDatabases(dstLoc)
	if err != nil {
		return false, fmt.Errorf("list target databases: %w", err)
	}
	return containsName(names, dstLoc.Database), nil
}

// containsName reports whether names holds want.
func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// EnsureTargetDatabase creates the destination database when it does not exist,
// and reports whether centipede created it.
//
// transx-ex requires the target to already exist and be empty, and never creates
// one itself, so this step belongs here. The returned flag decides who cleans up
// after a failure: a database centipede created is dropped whole
// (DropTargetDatabase), while one that already existed is left to transx-ex's
// rollback, which empties it and keeps it.
//
// srcInfo supplies the source database's character set so the new database
// matches it; without that the difference resurfaces later as a validation
// warning or a charset incompatibility error.
func EnsureTargetDatabase(
	dst commonmodel.ConnectionRef,
	dstLoc transxex.DBMSLocation,
	srcInfo *transxex.DBMSInfo,
) (bool, error) {
	exists, err := TargetDatabaseExists(dst, dstLoc)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil // already there — nothing to create, nothing to own
	}

	// MongoDB, for the reason set out in plan.validateDBMSTargetReachable: nothing
	// to create here, because the restore's first CreateCollection is what makes
	// the database exist.
	//
	// It reports true all the same. The flag is not a record of who ran a DDL
	// statement - it is what dropIfOwned reads to decide what undoing this item
	// means, and all three things that matter are true: the database was not there
	// before, it is there because of this migration, and dropping it is what
	// restores the target. Reporting false would leave a half-loaded database
	// behind on failure. DropTargetDatabase already passes IfExists for exactly
	// this shape, so a source with no collections - where the restore wrote
	// nothing and the database never appeared - drops cleanly rather than erroring.
	if dstLoc.DBMSType == transxex.DBMSTypeMongoDB {
		return true, nil
	}

	switch dst.Source {
	case commonmodel.ConnectionSourceBeetleDB:
		// A managed database has to be asked for through cm-beetle, which owns
		// the instance, rather than by connecting to the database and issuing
		// CREATE DATABASE.
		if dst.BeetleDB == nil {
			return false, fmt.Errorf("beetleDb ref is nil")
		}
		ref, err := connsec.DecryptRef(dst)
		if err != nil {
			return false, fmt.Errorf("decrypt connection ref: %w", err)
		}
		if err := beetle.CreateDatabase(*ref.BeetleDB, dstLoc.Database); err != nil {
			return false, fmt.Errorf("create target database %q: %w", dstLoc.Database, err)
		}
		return true, nil

	case commonmodel.ConnectionSourceDB:
		if dst.DB == nil {
			return false, fmt.Errorf("db config is nil")
		}
		if !strings.EqualFold(dst.DB.ProviderName, commonmodel.ProviderOnPrem) {
			return false, fmt.Errorf(
				"db target: database %q does not exist; centipede creates one only for "+
					"providerName %q (got %q) — provision it on the provider first",
				dstLoc.Database, commonmodel.ProviderOnPrem, dst.DB.ProviderName)
		}
		if err := transxex.CreateDatabase(dstLoc, createOptionFrom(dstLoc.DBMSType, srcInfo)); err != nil {
			return false, fmt.Errorf("create target database %q: %w", dstLoc.Database, err)
		}
		return true, nil

	default:
		// honeybee is a source-only connection and cannot be a DBMS target.
		return false, fmt.Errorf("unsupported DBMS target source: %s", dst.Source)
	}
}

// DropTargetDatabase removes a database centipede created, after its migration
// failed or was cancelled. It is never called for a database that already
// existed: that case is transx-ex's rollback, which keeps the database.
func DropTargetDatabase(dst commonmodel.ConnectionRef, dstLoc transxex.DBMSLocation) error {
	switch dst.Source {
	case commonmodel.ConnectionSourceBeetleDB:
		// Deletion pairs with creation: both go through cm-beetle.
		if dst.BeetleDB == nil {
			return fmt.Errorf("beetleDb ref is nil")
		}
		ref, err := connsec.DecryptRef(dst)
		if err != nil {
			return fmt.Errorf("decrypt connection ref: %w", err)
		}
		return beetle.DropDatabase(*ref.BeetleDB, dstLoc.Database)

	case commonmodel.ConnectionSourceDB:
		// IfExists covers MongoDB, where creation was a no-op. RequireEmpty is
		// left unset on purpose: the point is to remove a partially loaded
		// database along with whatever it holds.
		return transxex.DropDatabase(dstLoc, &transxex.DropDatabaseOption{IfExists: true})

	default:
		return fmt.Errorf("unsupported DBMS target source: %s", dst.Source)
	}
}

// createOptionFrom builds the engine-specific CREATE DATABASE clauses that
// reproduce the source database's text handling on the target.
func createOptionFrom(dbmsType string, src *transxex.DBMSInfo) *transxex.CreateDatabaseOption {
	opt := &transxex.CreateDatabaseOption{}
	if src == nil {
		return opt
	}
	switch dbmsType {
	case transxex.DBMSTypeMySQL:
		opt.MySQL = &transxex.MySQLCreateOption{CharacterSet: src.CharacterSet, Collate: src.Collation}
	case transxex.DBMSTypeMariaDB:
		opt.MariaDB = &transxex.MariaDBCreateOption{CharacterSet: src.CharacterSet, Collate: src.Collation}
	case transxex.DBMSTypePostgreSQL:
		// Encoding, LcCollate and LcCtype require a template database whose own
		// encoding matches, which in practice means template0: template1 may
		// already hold data in another encoding and the server then refuses.
		opt.PostgreSQL = &transxex.PostgreSQLCreateOption{
			Encoding:  src.CharacterSet,
			LcCollate: src.Collation,
			LcCtype:   src.Ctype,
			Template:  "template0",
		}
	case transxex.DBMSTypeMongoDB:
		// MongoDB has no CREATE DATABASE: the database begins to exist when the
		// migration creates its first collection.
	}
	return opt
}

// ── Migration ────────────────────────────────────────────────────────────────

// MigrateDBMS transfers every database listed in m from src to dst. One
// MigrationDBModel holds a single connection pair and as many database items as
// it migrates, so each item is resolved, prepared and transferred in turn and
// reports its own ProgressEvent with ItemPath "<dbType>/<srcName>".
//
// Each item's target database is created when missing. The transfer
// scope is always full; a non-empty rule list drives the exclude-only pipeline
// that removes the listed tables/columns/rows/schema objects.
//
// Cleanup after a failure follows ownership: RollbackOnFailure is enabled only
// for a database that already existed, in which case transx-ex empties it and
// keeps it. A database centipede created is dropped whole instead, so the two
// paths never both run.
//
// Filter feasibility (e.g. column/row/object exclusion is impossible over an
// ssh-tunnel) is checked up-front by transxex.ValidateDBMS — MigrateDBMSAsync
// does not validate on its own — and any violation is reported as a failed item.
//
// Returns ctx.Err() on cancellation; per-item errors are reported through
// progressCh and do not stop the remaining items.
func MigrateDBMS(ctx context.Context, m targetmodel.MigrationDBModel, onFailure string, progressCh chan<- ProgressEvent) error {
	dbType := string(m.DBType)

	dbs := make([]targetmodel.DBMigrationInfo, len(m.Databases))
	copy(dbs, m.Databases)
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Order < dbs[j].Order })

	for _, d := range dbs {
		select {
		case <-ctx.Done():
			progressCh <- ProgressEvent{
				ItemPath: fmt.Sprintf("%s/%s", dbType, d.SrcName),
				Status:   "cancelled",
				Err:      ctx.Err(),
			}
			return ctx.Err()
		default:
		}

		if err := migrateOneDatabase(ctx, m, d, dbType, onFailure, progressCh); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Item-level failures are already reported; carry on with the rest.
			continue
		}
	}
	return nil
}

// migrateOneDatabase runs a single DBMigrationInfo item end to end.
func migrateOneDatabase(
	ctx context.Context,
	m targetmodel.MigrationDBModel,
	d targetmodel.DBMigrationInfo,
	dbType string,
	onFailure string,
	progressCh chan<- ProgressEvent,
) error {
	itemPath := fmt.Sprintf("%s/%s", dbType, d.SrcName)
	start := time.Now()

	fail := func(err error) error {
		progressCh <- ProgressEvent{
			ItemPath:   itemPath,
			Status:     "failed",
			DurationMs: time.Since(start).Milliseconds(),
			Err:        err,
		}
		return err
	}

	dstName := d.DstName
	if dstName == "" {
		dstName = d.SrcName
	}

	srcLoc, err := ResolveDBMSLocation(m.SrcConnection, d.SrcName, dbType)
	if err != nil {
		return fail(fmt.Errorf("resolve src connection: %w", err))
	}
	dstLoc, err := ResolveDBMSLocation(m.DstConnection, dstName, dbType)
	if err != nil {
		return fail(fmt.Errorf("resolve dst connection: %w", err))
	}

	// Read the source before creating anything: the new database is created to
	// match its character set.
	srcInfo, err := InspectDB(srcLoc)
	if err != nil {
		return fail(err)
	}

	created, err := EnsureTargetDatabase(m.DstConnection, dstLoc, srcInfo)
	if err != nil {
		return fail(err)
	}

	// The schema selection travels with the migration rather than the connection:
	// a connection names a server, while which schemas to move out of one
	// database is a decision of this migration.
	srcLoc.PgSchema, dstLoc.PgSchema = splitPgSchemas(d.PgSchemas)

	filterOpt, err := toDBMSFilter(d.Rules)
	if err != nil {
		return fail(fmt.Errorf("build dbms filter: %w", err))
	}
	if filterOpt != nil {
		srcLoc = DBMSLocationWithFilter(srcLoc, filterOpt)
	}

	// "keep" turns both cleanup paths off at once. Turning off only one would
	// leave the outcome depending on who created the database, which is the
	// distinction this option exists to override.
	keep := onFailure == model.DBMSOnFailureKeep

	dmm := transxex.DBMSMigrationModel{
		Source:      srcLoc,
		Destination: dstLoc,
		Scope:       transxex.ScopeFull,
		// A database we created is dropped whole on failure, so there is nothing
		// for the object-level rollback to do; the two paths are exclusive.
		RollbackOnFailure: !created && !keep,
	}

	if err := transxex.ValidateDBMS(dmm); err != nil {
		if !keep {
			dropIfOwned(m.DstConnection, dstLoc, created, itemPath)
		}
		return fail(err)
	}

	handle := transxex.MigrateDBMSAsync(dmm)
	done := make(chan error, 1)
	go func() { done <- handle.Wait() }()

	select {
	case <-ctx.Done():
		handle.Cancel()
		<-done
		dropIfOwned(m.DstConnection, dstLoc, created, itemPath)
		progressCh <- ProgressEvent{
			ItemPath:      itemPath,
			Status:        "cancelled",
			DurationMs:    time.Since(start).Milliseconds(),
			TargetCreated: created,
			Err:           ctx.Err(),
		}
		return ctx.Err()

	case migErr := <-done:
		status := "success"
		if migErr != nil {
			status = "failed"
			log.Error().
				Str("dbType", dbType).
				Str("srcName", d.SrcName).
				Str("dstName", dstName).
				Err(migErr).
				Msg("DBMS migration failed")
			if keep {
				log.Warn().
					Str("item", itemPath).
					Str("database", dstLoc.Database).
					Bool("targetCreated", created).
					Msg("dbmsOnFailure=keep: leaving the failed target database in place")
			} else {
				dropIfOwned(m.DstConnection, dstLoc, created, itemPath)
			}
		}
		progressCh <- ProgressEvent{
			ItemPath:      itemPath,
			Status:        status,
			DurationMs:    time.Since(start).Milliseconds(),
			TargetCreated: created,
			Err:           migErr,
		}
		return migErr
	}
}

// dropIfOwned removes the target database when centipede created it. A failure
// to drop is logged rather than returned: the item has already failed, and the
// next attempt finds the leftover database and takes the pre-existing path,
// where transx-ex empties it instead.
func dropIfOwned(dst commonmodel.ConnectionRef, dstLoc transxex.DBMSLocation, created bool, itemPath string) {
	if !created {
		return
	}
	if err := DropTargetDatabase(dst, dstLoc); err != nil {
		log.Error().
			Str("item", itemPath).
			Str("database", dstLoc.Database).
			Err(err).
			Msg("failed to drop the target database centipede created")
	}
}
