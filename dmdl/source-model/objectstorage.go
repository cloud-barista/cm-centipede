package sourcemodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// ObjectEntry mirrors transxex.OSEntry (one folder-prefix entry).
type ObjectEntry struct {
	Key          string `json:"key"`                    // folder prefix "bucket/prefix/"
	LastModified string `json:"lastModified,omitempty"` // only when ObjectStorageMetric.PrefixModTime
	ObjectCount  *int   `json:"objectCount,omitempty"`  // only when ObjectStorageMetric.PrefixObjectCount
	Size         *int64 `json:"size,omitempty"`         // only when ObjectStorageMetric.PrefixObjectSize
}

// ObjectStorageInfo mirrors transxex.OSInspectResult.
type ObjectStorageInfo struct {
	Path            string         `json:"path"`
	TotalSize       *int64         `json:"totalSize,omitempty"`       // only when ObjectStorageMetric.TotalSize
	ObjectCount     *int           `json:"objectCount,omitempty"`     // only when ObjectStorageMetric.ObjectCount
	ExtensionCounts map[string]int `json:"extensionCounts,omitempty"` // only when ObjectStorageMetric.ExtensionCount
	Folders         []ObjectEntry  `json:"folders"`
}

// SourceObjectStorageModel bundles a connection reference with its discovered
// buckets/paths (path/aggregates/folders promoted from ObjectStorageInfo).
type SourceObjectStorageModel struct {
	Connection        commonmodel.ConnectionRef `json:"connection"`
	ObjectStorageInfo                           // embedded
}
