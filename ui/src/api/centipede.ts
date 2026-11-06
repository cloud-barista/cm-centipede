// cm-centipede REST calls. Reached through /centipede/* on this app's origin,
// where the proxy injects Basic Auth server-side.

import { arr, http } from './http';
import type { ConnectionRef, DBMSFilter, FileSystemFilter, ObjectStorageFilter, Strategy } from '@/types/common';
import type { SourceDataMigrationModel } from '@/types/source';
import type {
  CreateMigrationReq,
  ListResponse,
  Migration,
  MigrationLog,
  MigrationSummary,
  RetryResponse,
  TargetDataMigrationModel,
  ValidationMode,
  ValidationResultResponse,
} from '@/types/target';

const BASE = '/centipede';

/**
 * One (source entry, destination) pair of a plan request.
 *
 * `srcConnection` says which entry of the source model this destination is
 * for. It is always a honeybee ref — the source model is a discovery result,
 * so that is the only ref one ever carries — and the server matches it by
 * honeybee.connectionId alone.
 *
 * Only the filter belonging to that entry's own domain may be set,
 * `fileSystemStrategy` only for a filesystem source and
 * `objectStorageStrategy` only for an object storage one. The others are rejected
 * rather than ignored: an ignored filter leaves the caller reading a plan that
 * migrated everything, believing their filter ran.
 */
export interface PlanEntry {
  srcConnection: ConnectionRef;
  dstConnection: ConnectionRef;
  fileSystemStrategy?: Strategy;
  fileSystemFilter?: FileSystemFilter;
  objectStorageFilter?: ObjectStorageFilter;
  /** Empty runs as direct: each object streams across, nothing staged. */
  objectStorageStrategy?: Strategy;
  dbmsFilter?: DBMSFilter;
}

/**
 * POST /plans/target's body.
 *
 * `source` is the refined response passed through untouched.
 * `plans` names a destination for every entry in it — the request used to
 * carry one dstConnection and one filter set for the whole model, which a
 * source of several connections had no way to use: every entry landed on the
 * same destination under the same mapping, and the ones the mapping did not
 * name were dropped without a word. A source entry no plans entry names is now
 * refused rather than skipped.
 */
export interface TargetPlanReq {
  source: SourceDataMigrationModel;
  plans: PlanEntry[];
}

/**
 * Date window shared by both list endpoints.
 *
 * `lastDays: 0` means "all time" and cannot be combined with dateFrom/dateTo.
 * GET /migration/all defaults to the last 7 days when nothing is given, so the
 * duplicate-name pre-check must pass lastDays: 0 explicitly.
 */
export interface MigrationListFilter {
  status?: string;
  dateFrom?: string;
  dateTo?: string;
  lastDays?: number;
}

function listParams(f?: MigrationListFilter): Record<string, string> {
  const p: Record<string, string> = {};
  if (!f) return p;
  if (f.status) p.status = f.status;
  if (f.lastDays !== undefined) p.lastDays = String(f.lastDays);
  if (f.dateFrom) p.dateFrom = f.dateFrom;
  if (f.dateTo) p.dateTo = f.dateTo;
  return p;
}

export const centipedeApi = {
  readyz: () => http.get<unknown>(`${BASE}/readyz`),

  getTargetPlan: (req: TargetPlanReq) =>
    http.post<TargetDataMigrationModel>(`${BASE}/plans/target`, req).then(normalizePlan),

  createMigration: (req: CreateMigrationReq) =>
    http.post<MigrationSummary>(`${BASE}/migration`, req),

  /** Paginated list — the Runs screen uses the server's own pagination. */
  listMigrations: (
    filter?: MigrationListFilter,
    page = 1,
    pageSize = 20,
  ) =>
    http
      .get<ListResponse<MigrationSummary>>(`${BASE}/migration`, {
        params: { ...listParams(filter), page, pageSize },
      })
      .then((r) => ({ ...r, items: arr(r.items) })),

  /** Unpaginated list. Defaults to the last 7 days unless a date param is given. */
  listAllMigrations: (filter?: MigrationListFilter) =>
    http
      .get<MigrationSummary[]>(`${BASE}/migration/all`, { params: listParams(filter) })
      .then(arr),

  getMigration: (id: string) =>
    http.get<Migration>(`${BASE}/migration/${id}`).then(normalizeMigration),

  deleteMigration: (id: string) => http.del<unknown>(`${BASE}/migration/${id}`),

  cancelMigration: (id: string) => http.post<unknown>(`${BASE}/migration/${id}/cancel`),

  retryMigration: (id: string, retryMode: 'partial' | 'full') =>
    http.post<RetryResponse>(`${BASE}/migration/${id}/retry`, { retryMode }),

  listLogs: (id: string, opts?: { status?: string; page?: number; pageSize?: number }) =>
    http
      .get<ListResponse<MigrationLog>>(`${BASE}/migration/${id}/logs`, {
        params: {
          ...(opts?.status ? { status: opts.status } : {}),
          page: opts?.page ?? 1,
          pageSize: opts?.pageSize ?? 20,
        },
      })
      .then((r) => ({ ...r, items: arr(r.items) })),

  /** 202 on accept; 400 unless the migration is completed. */
  startValidation: (id: string, mode: ValidationMode) =>
    http.post<ValidationResultResponse>(`${BASE}/migration/${id}/validation`, { mode }),

  getValidation: (id: string) =>
    http.get<ValidationResultResponse>(`${BASE}/migration/${id}/validation`),
};

/**
 * The plan's three resource lists are omitempty, so they are absent rather than
 * null — but MigrationDBModel.databases is not, and a nil one arrives as null.
 *
 * Both the planner's own response and the plan stored on a migration go through
 * here, because Step 4 renders one and the Runs card renders the other.
 */
function normalizePlan(plan: TargetDataMigrationModel): TargetDataMigrationModel {
  const p = plan?.targetDataMigrationModel;
  if (!p) return plan;
  return {
    targetDataMigrationModel: {
      ...p,
      databases: p.databases?.map((db) => ({ ...db, databases: arr(db.databases) })),
    },
  };
}

function normalizeMigration(m: Migration): Migration {
  if (!m?.plan) return m;
  return { ...m, plan: normalizePlan(m.plan) };
}
