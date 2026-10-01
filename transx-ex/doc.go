// Package transxex migrates data between clouds: filesystems, S3-compatible
// object storage, and the MySQL, MariaDB, PostgreSQL and MongoDB databases.
//
// It is the module's single public API. Everything a caller needs — entry
// functions, models, errors, constants — is re-exported here, so one import is
// enough:
//
//	import "github.com/cloud-barista/cm-centipede/transx-ex"
//
//	model := transxex.StorageMigrationModel{ /* … */ }
//	err := transxex.MigrateStorage(model)
//
// # Naming
//
// Both domains offer the same operations, so names that would otherwise collide
// carry the domain: MigrateStorage and MigrateDBMS, PlanStorage and PlanDBMS.
// Names that are unambiguous keep their plain form — ListDatabases,
// CreateDatabase, DropDatabase, ParseBucketAndKey. Types shared by both
// domains, such as SSHConfig, are declared once and unqualified.
//
// # Layout
//
// The implementation lives in subpackages, which this package only re-exports:
//
//	storagex            filesystem and object storage migration
//	  storagex/filter     glob and size matchers (include/exclude pipeline)
//	  storagex/fieldsec   hybrid field encryption
//	dbmsx               MySQL, MariaDB, PostgreSQL, MongoDB migration
//	  dbmsx/driver        per-engine implementations over a shared base
//	  dbmsx/executor      dump, restore and direct-pipe executors
//	  dbmsx/filter        table, row and schema-object exclusion
//	core                SSH, pipeline, handle and error machinery shared by both
//
// Importing them directly is supported but rarely necessary; prefer this
// package unless you need something it does not expose — registering a custom
// filter matcher with storagex/filter.Register or dbmsx/filter.RegisterFilter,
// for instance.
package transxex
