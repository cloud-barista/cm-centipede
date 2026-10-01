package sourcemodel

import commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"

// FSEntry mirrors transxex.FSEntry (one directory entry).
type FSEntry struct {
	Path        string `json:"path"`
	IsMount     bool   `json:"isMount"`
	ModTime     string `json:"modTime,omitempty"`     // only when FilesystemMetric.FolderModTime
	Permissions string `json:"permissions,omitempty"` // only when FilesystemMetric.FolderPerms
	FileCount   *int   `json:"fileCount,omitempty"`   // only when FilesystemMetric.FolderFileCount
	FileSize    *int64 `json:"fileSize,omitempty"`    // only when FilesystemMetric.FolderFileSize
}

// FSInfo mirrors transxex.FSInspectResult: the path-wide aggregates plus the
// directory listing.
type FSInfo struct {
	Path            string         `json:"path"`
	IsMount         bool           `json:"isMount"`
	TotalSize       *int64         `json:"totalSize,omitempty"`       // only when FilesystemMetric.TotalSize
	FileCount       *int           `json:"fileCount,omitempty"`       // only when FilesystemMetric.FileCount
	ExtensionCounts map[string]int `json:"extensionCounts,omitempty"` // only when FilesystemMetric.ExtensionCount
	Folders         []FSEntry      `json:"folders"`
}

// SourceFileSystemModel bundles a connection reference with its discovered
// directories (path/isMount/aggregates/folders promoted from FSInfo).
type SourceFileSystemModel struct {
	Connection commonmodel.ConnectionRef `json:"connection"`
	FSInfo                               // embedded
}
