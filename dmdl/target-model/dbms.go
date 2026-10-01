package targetmodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// DBMigrationInfo represents a single database migration task unit. It has the
// same shape as FSMigrationInfo and ObjectMigrationInfo, but names databases
// rather than paths, so it uses SrcName/DstName. The plan layer resolves the
// request's TargetMapping, so DstName is always filled in here.
//
// Empty Rules migrate the database in full; otherwise the transx-ex
// exclude-only pipeline removes the listed tables/columns/rows/objects
// (commonmodel.DBMSFilterRule mirror).
type DBMigrationInfo struct {
	Order   int    `json:"order"`
	SrcName string `json:"srcName" validate:"required"`
	DstName string `json:"dstName" validate:"required"`

	// PgSchemas selects and renames PostgreSQL schemas within this database.
	// Empty migrates every schema under its own name. The adapter splits the
	// name pairs into the two positional slices transx-ex expects.
	PgSchemas []commonmodel.PgSchemaMapping `json:"pgSchemas,omitempty"`

	Rules []commonmodel.DBMSFilterRule `json:"rules,omitempty"`
}

// MigrationDBModel holds one source/target connection pair and the databases to
// migrate under it. Connection refs carry no database name, so the names travel
// here.
type MigrationDBModel struct {
	// PlanEntryID names this entry within the plan
	// — see MigrationFileSystemModel.PlanEntryID.
	PlanEntryID string `json:"planEntryId,omitempty"`

	SrcConnection commonmodel.ConnectionRef `json:"srcConnection" validate:"required"`
	DstConnection commonmodel.ConnectionRef `json:"dstConnection" validate:"required"`
	DBType        commonmodel.DBMSType      `json:"dbType"        validate:"required,oneof=mysql mariadb postgresql mongodb"`
	Databases     []DBMigrationInfo         `json:"databases"     validate:"required,min=1"`
	Errors        []string                  `json:"errors,omitempty"`
}
