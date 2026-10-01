// Package sourcemodel describes the data resources observed in a source
// environment: file system directories, object storage buckets and DBMS
// schemas. They are the single definition of that description: whoever surveys
// a source builds these types, and whoever plans a migration reads them, so the
// two sides never keep separate copies in step by hand. A producer fills the one
// sub-field of ConnectionRef (see common-model/connection.go) matching the
// connection it holds; the sources it cannot produce are omitempty and stay out
// of its payloads. All discovery/inspect shapes track dbmsx / transx-ex.
package sourcemodel

// SourceDataMigrationProperty holds all discovered source resources grouped by type.
type SourceDataMigrationProperty struct {
	FileSystems    []SourceFileSystemModel    `json:"fileSystems,omitempty"`
	ObjectStorages []SourceObjectStorageModel `json:"objectStorages,omitempty"`
	Databases      []SourceDBModel            `json:"databases,omitempty"`
}

// SourceDataMigrationModel is the top-level input for plan and migration requests.
type SourceDataMigrationModel struct {
	SourceDataMigrationModel SourceDataMigrationProperty `json:"sourceDataMigrationModel"`
}
