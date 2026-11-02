// Package storagex migrates filesystem and object storage data.
//
// A DataMigrationModel names a source and a destination DataLocation; Plan
// turns the pair into a core.Pipeline and Handle runs it asynchronously,
// reporting progress and a terminal MigrationStatus.
//
// Two executor families back the transfers:
//
//   - rsync, for local and remote filesystem paths, over SSH when the location
//     carries an SSHConfig;
//   - S3, for object storage, driven through an S3Provider.
//
// Four providers implement S3Provider: MinIO (and any S3-compatible endpoint)
// and Azure Blob talk to the store directly, while Spider and Tumblebug reach
// it through the CB-Spider and CB-Tumblebug object storage APIs.
//
// # Layout
//
//	filter/     glob and size matchers behind the include/exclude pipeline
//	fieldsec/   hybrid field encryption for credentials carried in a model
//
// Shared machinery — SSH, the pipeline skeleton, the handle and the error
// vocabulary — lives in core and is re-exported here, so callers never import
// core themselves.
package storagex
