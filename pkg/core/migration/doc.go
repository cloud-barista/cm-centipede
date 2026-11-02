// Package migration executes the three migration domains — filesystem, object
// storage and DBMS — on top of data-mig.
//
// Its job is resolution and delegation. A plan carries ConnectionRefs, which
// name a source (honeybee, beetle*, or an inline ssh/minio/db block) rather
// than the access details themselves. The Resolve* and *Loc functions turn a
// ref plus a path into the concrete data-mig location or config the transfer
// needs, calling cm-honeybee or cm-beetle when the ref is a reference rather
// than an inline block. Migrate* then runs the transfer and reports progress
// as ProgressEvents.
//
// Those resolvers are exported because pkg/core/validation reuses them: a
// migration and its validation must resolve a connection the same way, or a
// transfer that succeeded would fail verification.
package migration
