// The queryKey / staleTime / refetchInterval table, kept in one
// place so two screens sharing a key really do share one request.

import type { Category } from '@/lib/connectionCategory';
import type { MigrationListFilter } from '@/api/centipede';

export const qk = {
  honeybee: {
    sourceGroups: () => ['honeybee', 'sourceGroups'] as const,
    connections: (sgId: string) => ['honeybee', 'connections', sgId] as const,
    /** `connId` '*' is the source group's refined response. */
    refined: (kind: Category, sgId: string, connId: string) =>
      ['honeybee', 'refined', kind, sgId, connId] as const,
  },
  beetle: {
    infra: (nsId: string) => ['beetle', 'infra', nsId] as const,
    objectStorage: (nsId: string) => ['beetle', 'objectStorage', nsId] as const,
    rdbms: (nsId: string) => ['beetle', 'rdbms', nsId] as const,
    sshReady: (nsId: string, infraId: string) => ['beetle', 'sshReady', nsId, infraId] as const,
  },
  centipede: {
    migrations: (filter?: MigrationListFilter) =>
      ['centipede', 'migrations', filter ?? {}] as const,
    migrationsPage: (filter: MigrationListFilter, page: number, pageSize: number) =>
      ['centipede', 'migrationsPage', filter, page, pageSize] as const,
    migration: (id: string) => ['centipede', 'migration', id] as const,
    logs: (id: string, opts: { status?: string; page?: number }) =>
      ['centipede', 'logs', id, opts] as const,
    validation: (id: string) => ['centipede', 'validation', id] as const,
  },
} as const;

/** Polling cadences. Idle is a slowdown, never a stop. */
export const POLL = {
  listIdleMs: 30_000,
  listActiveMs: 3_000,
  connectionsIdleMs: 15_000,
  connectionsActiveMs: 3_000,
  detailMs: 2_000,
  logsMs: 5_000,
  /**
   * Whether a shortlisted cm-beetle target still exists.
   *
   * Minutes, not seconds, and for a reason the other rows here do not have:
   * every one of these calls goes through cm-beetle to cb-tumblebug and out to
   * the cloud provider, which is also why cm-beetle answers 503 with a
   * Retry-After when the provider pushes back. Target infrastructure changes
   * when a person tears it down, not on its own.
   */
  targetPresenceMs: 5 * 60_000,
  /** Fallback when a 503 carries no Retry-After to obey. */
  targetPresenceBackoffMs: 15 * 60_000,
} as const;

export const STALE = {
  sourceGroups: 10_000,
  connections: 10_000,
  refined: 5 * 60_000,
  beetle: 60_000,
  migrations: 2_000,
  logs: 5_000,
} as const;
