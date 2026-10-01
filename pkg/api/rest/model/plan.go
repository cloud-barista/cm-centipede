package model

import (
	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	sourcemodel "github.com/cloud-barista/cm-centipede/dmdl/source-model"
)

// ── Plan request model ───────────────────────────────────────────────────────

// TargetPlanReq is the request body for POST /centipede/plans/target.
//
// Source is the discovery result passed through verbatim — a cm-honeybee
// refined response, group-level included, which is why it can hold several
// entries. Plans selects which of those entries to migrate and pairs each with
// a destination: one entry per (source entry, destination), so two sources
// never share a destination by accident and the same source may appear twice
// to fan out. A source entry no plan entry names is not migrated.
//
// Before this shape there was one dstConnection and one filter set for the
// whole request, which a group-level source model had no way to use: every
// entry was copied onto the same destination and the same mapping.
type TargetPlanReq struct {
	Source sourcemodel.SourceDataMigrationModel `json:"source" validate:"required"`
	Plans  []PlanEntry                          `json:"plans"  validate:"required,min=1"`
}

// PlanEntry selects one entry of TargetPlanReq.Source and pairs it with a
// destination.
//
// SrcConnection names that entry. It is a ConnectionRef for symmetry with
// DstConnection and with the target model this plan produces, but only a
// honeybee ref is accepted: the source model is produced by cm-honeybee, so
// that is the only ref a discovery ever yields. Matching is by
// honeybee.connectionId alone (see plan.SrcConnKey) — never by comparing the
// structs, whose other sub-fields are rejected on a source ref and would only
// add ways for an equal connection to compare unequal.
//
// A honeybee connection has exactly one ConnType, so the id also settles which
// of the three source domains the entry belongs to, and therefore which filter
// field this entry may set. The other three are rejected rather than ignored:
// an ignored filter leaves the caller believing a plan that migrated everything
// had honoured it.
//
// The same SrcConnection may appear in several entries. Two entries that name
// the same source and the same destination but different paths are the way one
// connection migrates more than one folder or bucket, since the file system and
// object storage filters each select a single one.
type PlanEntry struct {
	SrcConnection commonmodel.ConnectionRef `json:"srcConnection" validate:"required"`
	DstConnection commonmodel.ConnectionRef `json:"dstConnection" validate:"required"`

	// FileSystemStrategy is the transfer strategy applied to filesystem (SSH→SSH)
	// tasks. Empty is normalised to "relay" (safer default: avoids agent-forward
	// setup requirements). It has no effect on object-storage or DBMS tasks, where
	// relay is always used / not applicable.
	FileSystemStrategy  string                           `json:"fileSystemStrategy,omitempty" validate:"omitempty,oneof=auto direct relay"`
	FileSystemFilter    *commonmodel.FileSystemFilter    `json:"fileSystemFilter,omitempty"`
	ObjectStorageFilter *commonmodel.ObjectStorageFilter `json:"objectStorageFilter,omitempty"`
	DBMSFilter          *commonmodel.DBMSFilter          `json:"dbmsFilter,omitempty"`
}
