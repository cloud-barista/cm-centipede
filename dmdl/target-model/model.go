// Package targetmodel describes the executable migration plan derived from a
// source model: for each resource type, which source→destination connection
// pair to use, which folders/buckets/databases to move and which filter rules
// to apply. It is produced by POST /centipede/plans/target and consumed by the
// migration executor.
package targetmodel

// TargetDataMigrationProperty groups all migration task units by type.
type TargetDataMigrationProperty struct {
	FileSystems    []MigrationFileSystemModel    `json:"fileSystems,omitempty"`
	ObjectStorages []MigrationObjectStorageModel `json:"objectStorages,omitempty"`
	Databases      []MigrationDBModel            `json:"databases,omitempty"`
}

// TargetDataMigrationModel is the top-level plan passed into CreateMigrationReq
// and stored in Migration.Plan.
type TargetDataMigrationModel struct {
	TargetDataMigrationModel TargetDataMigrationProperty `json:"targetDataMigrationModel" validate:"required"`
}
