// Package core holds the machinery both migration domains share: SSH access,
// the pipeline/step execution skeleton, the asynchronous handle, and the error
// and staging vocabulary.
//
// It is an internal layer. Callers use the transxex root package, or a domain
// package (storagex, dbmsx) directly; each of those re-exports the core types it
// exposes so that core never appears in a caller's import list.
//
// core must not import storagex or dbmsx — the dependency runs one way only.
package core
