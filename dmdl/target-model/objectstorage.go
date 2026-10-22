package targetmodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// ObjectMigrationInfo represents a single bucket migration task unit.
// Rules mirror the transx-ex filter pipeline applied to this bucket/prefix.
type ObjectMigrationInfo struct {
	Order   int                          `json:"order"`
	SrcPath string                       `json:"srcPath"       validate:"required"`
	DstPath string                       `json:"dstPath"       validate:"required"`
	Rules   []commonmodel.PathFilterRule `json:"rules,omitempty"`
}

// MigrationObjectStorageModel holds an objectstorage-source migration: S3→S3 by
// default, or objectstorage→filesystem cross-storage when DstType is filesystem.
// The source is always object storage; DstType names the destination storage.
type MigrationObjectStorageModel struct {
	// PlanEntryID names this entry within the plan
	// — see MigrationFileSystemModel.PlanEntryID.
	PlanEntryID string `json:"planEntryId,omitempty"`

	SrcConnection commonmodel.ConnectionRef `json:"srcConnection" validate:"required"`
	DstConnection commonmodel.ConnectionRef `json:"dstConnection" validate:"required"`
	// DstType is the destination storage type: empty/"objectstorage" = S3→S3,
	// "filesystem" = objectstorage→filesystem cross-storage.
	DstType string                `json:"dstType,omitempty" validate:"omitempty,oneof=filesystem objectstorage"`
	Buckets []ObjectMigrationInfo `json:"buckets,omitempty"`
	Errors  []string              `json:"errors,omitempty"`

	// Strategy is the transx transfer strategy (auto/direct/relay). It only affects
	// S3→S3 transfers — direct streams each object, relay stages them locally —
	// and objectstorage→filesystem cross-storage always relays. Set by the plan
	// layer; empty runs as direct.
	Strategy string `json:"strategy,omitempty" validate:"omitempty,oneof=auto direct relay"`
}
