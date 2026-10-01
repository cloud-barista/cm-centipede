package targetmodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// FSMigrationInfo represents a single directory migration task unit.
// Rules mirror the transx-ex filter pipeline applied to this folder.
type FSMigrationInfo struct {
	Order   int                          `json:"order"`
	SrcPath string                       `json:"srcPath"  validate:"required"`
	DstPath string                       `json:"dstPath"  validate:"required"`
	Rules   []commonmodel.PathFilterRule `json:"rules,omitempty"`
}

// MigrationFileSystemModel holds a filesystem-source migration: SSH→SSH by default,
// or filesystem→objectstorage cross-storage when DstType is objectstorage.
// The source is always a filesystem (SSH); DstType names the destination storage.
type MigrationFileSystemModel struct {
	// PlanEntryID names this entry within the plan. It is what a migration log,
	// a validation detail and a per-entry retry refer to, so that none of them
	// has to identify an entry by its position in the array — a position is not
	// a name, and nothing outside the plan can hold on to one.
	//
	// "entry", not "item": an item is the folder, bucket or database inside one
	// of these (see countPlanItems, MigrationLog.ItemPath, Migration.TotalItems),
	// and a name that meant both layers at once would be worse than the index it
	// replaces. Not "id" either — an entry is anonymous in its own document,
	// sitting in fileSystems[] with no type name beside it, so "id" alone would
	// not say what kind of id it is. The log field that refers to it is spelled
	// the same, which is the point: one token to search for in both documents.
	//
	// Assigned by POST /plans/target. A plan that arrives without one — edited,
	// hand-written, issued elsewhere — is given ids by CreateMigration before it
	// is stored, so a stored plan always has them.
	PlanEntryID string `json:"planEntryId,omitempty"`

	SrcConnection commonmodel.ConnectionRef `json:"srcConnection" validate:"required"`
	DstConnection commonmodel.ConnectionRef `json:"dstConnection" validate:"required"`
	// DstType is the destination storage type: empty/"filesystem" = SSH→SSH,
	// "objectstorage" = filesystem→objectstorage cross-storage.
	DstType string `json:"dstType,omitempty" validate:"omitempty,oneof=filesystem objectstorage"`
	// Strategy is the transx transfer strategy (auto/direct/relay). It only affects
	// SSH→SSH transfers; cross-storage always relays. Set by the plan layer.
	Strategy string            `json:"strategy,omitempty" validate:"omitempty,oneof=auto direct relay"`
	Folders  []FSMigrationInfo `json:"folders,omitempty"`
	Errors   []string          `json:"errors,omitempty"`
}
