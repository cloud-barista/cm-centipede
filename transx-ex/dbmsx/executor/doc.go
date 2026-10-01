// Package executor carries out the individual steps of a DBMS migration.
//
// Three implementations cover the two routes a plan can take:
//
//   - DumpExecutor writes a source database to a local staging file;
//   - RestoreExecutor restores a staging file into the destination;
//   - PipeExecutor streams a dump straight into a restore, so a migration
//     between two SSH-tunnelled endpoints never touches local disk.
//
// Each is a core.Executor over DBMSLocation and delegates the engine-specific
// work to a driver, so the executors stay engine-agnostic.
package executor
