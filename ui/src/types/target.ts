// Mirrors dmdl/target-model and pkg/api/rest/model.

import type {
  ConnectionRef,
  DBMSFilterRule,
  DBMSType,
  PathFilterRule,
  PgSchemaMapping,
  StorageType,
  Strategy,
} from './common';

export interface FSMigrationInfo {
  order: number;
  srcPath: string;
  dstPath: string;
  rules?: PathFilterRule[];
}

export interface MigrationFileSystemModel {
  srcConnection: ConnectionRef;
  dstConnection: ConnectionRef;
  dstType?: StorageType;
  strategy?: Strategy;
  folders?: FSMigrationInfo[];
  errors?: string[];
}

export interface ObjectMigrationInfo {
  order: number;
  srcPath: string;
  dstPath: string;
  rules?: PathFilterRule[];
}

export interface MigrationObjectStorageModel {
  srcConnection: ConnectionRef;
  dstConnection: ConnectionRef;
  dstType?: StorageType;
  /** Only S3→S3 reads it: direct streams, relay stages. Cross-storage relays. */
  strategy?: Strategy;
  buckets?: ObjectMigrationInfo[];
  errors?: string[];
}

export interface DBMigrationInfo {
  order: number;
  srcName: string;
  dstName: string;
  pgSchemas?: PgSchemaMapping[];
  rules?: DBMSFilterRule[];
}

export interface MigrationDBModel {
  srcConnection: ConnectionRef;
  dstConnection: ConnectionRef;
  dbType: DBMSType;
  databases: DBMigrationInfo[];
  errors?: string[];
}

export interface TargetDataMigrationProperty {
  fileSystems?: MigrationFileSystemModel[];
  objectStorages?: MigrationObjectStorageModel[];
  databases?: MigrationDBModel[];
}

export interface TargetDataMigrationModel {
  targetDataMigrationModel: TargetDataMigrationProperty;
}

// ── Migration records ───────────────────────────────────────────────────────

export type MigrationStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type ValidationStatus = 'passed' | 'failed' | 'not_run' | 'running';

export type DBMSOnFailure = 'cleanup' | 'keep';

export interface ValidationDetailItem {
  itemPath: string;
  status: string;
  message?: string;
}

/** Counts that replace `plan` on the two list endpoints. */
export interface PlanSummary {
  fileSystems: number;
  objectStorages: number;
  databases: number;
  /**
   * How many of the entries above land on the other storage type. Validation
   * compares those by path and size only, whichever mode is asked for.
   */
  crossStorage?: number;
}

/** What the list endpoints return — no `plan`, so no credentials leak into a list. */
export interface MigrationSummary {
  id: string;
  name: string;
  description: string;
  planSummary: PlanSummary;
  dbmsOnFailure?: DBMSOnFailure;
  /** As the request named it; absent means the server default. */
  concurrency?: number;
  status: MigrationStatus;
  statusMessage?: string;
  totalItems: number;
  processedItems: number;
  failedItems: number;
  /**
   * How much the finished items moved. There is no total beside it: no layer
   * computes the source total before a run, so the server stopped carrying one
   * rather than answer 0 to a question nobody could answer.
   */
  transferredBytes: number;
  /**
   * The file, object or table being moved right now — one level below the item
   * counted by processedItems. Absent when nothing is in flight, which includes
   * every finished migration.
   */
  currentObject?: string;
  /**
   * Which step of the item's transfer currentObject and the in-flight bytes
   * belong to, out of currentSteps. A relay stages through local storage (1/2)
   * and then sends (2/2), counting its bytes from zero in each — so
   * transferredBytes drops back at the boundary. Absent when nothing is in
   * flight or the transfer has no steps to report.
   */
  currentStep?: number;
  currentSteps?: number;
  /** The step's own name where it has one: a database's "dumping"/"restoring". */
  currentStepName?: string;
  /**
   * What the step is doing beyond its name, where the server says: a restore's
   * "indexes deferred: 2 tables" while it writes rows with those tables'
   * indexes held back, then "rebuilding indexes".
   */
  currentStepDetail?: string;
  /**
   * A database item's progress in its current step: tables (or collections)
   * finished out of currentUnitsTotal, rows written, and what the in-flight
   * bytes count towards — the dump a restore reads. Absent wherever the step
   * cannot tell: a restore through a CLI over SSH counts bytes alone.
   */
  currentUnits?: number;
  currentUnitsTotal?: number;
  currentRows?: number;
  currentBytesTotal?: number;
  validationStatus?: ValidationStatus;
  validationMessage?: string;
  validationDetails?: ValidationDetailItem[];
  validationMode?: ValidationMode;
  validationStartedAt?: string;
  startedAt?: string;
  completedAt?: string;
  createdAt: string;
  updatedAt: string;
}

/** What GET /migration/{id} returns — the summary plus the full plan. */
export interface Migration extends Omit<MigrationSummary, 'planSummary'> {
  plan: TargetDataMigrationModel;
}

export interface MigrationLog {
  id: number;
  migrationId: string;
  itemPath: string;
  status: string;
  errorMsg?: string;
  sizeBytes?: number;
  durationMs: number;
  failedObjectKind?: string;
  failedObjectName?: string;
  targetCreated?: boolean;
  rollbackStatus?: string;
  rollbackErrorMsg?: string;
  /**
   * How many transfers the item ran at once, and why that is not what the run
   * asked for when it is not. Absent when the item did not report it.
   */
  appliedConcurrency?: number;
  concurrencyNote?: string;
  createdAt: string;
}

export interface ValidationResultResponse {
  validationStatus: ValidationStatus;
  /**
   * The result's summary once the validation has finished, and where it has got
   * to while it runs — refreshed every couple of seconds.
   */
  validationMessage?: string;
  details?: ValidationDetailItem[];
  /** How filesystem items were compared. Object storage and databases ignore it. */
  mode?: ValidationMode;
  startedAt?: string;
}

/**
 * How filesystem items are compared: size and modification time (`quick`, the
 * server's default), or size and SHA256 of every migrated file (`checksum`).
 */
export type ValidationMode = 'quick' | 'checksum';

export interface CreateMigrationReq {
  name: string;
  description?: string;
  plan: TargetDataMigrationModel;
  dbmsOnFailure?: DBMSOnFailure;
  /** 1–32; omitted for the server default. */
  concurrency?: number;
}

export interface RetryResponse {
  originalMigrationId: string;
  newMigration: Migration;
}

export interface ListResponse<T> {
  total: number;
  page: number;
  pageSize: number;
  items: T[];
}
