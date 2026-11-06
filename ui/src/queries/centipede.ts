'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { centipedeApi, type MigrationListFilter } from '@/api/centipede';
import { POLL, STALE, qk } from './keys';
import type {
  CreateMigrationReq,
  MigrationStatus,
  MigrationSummary,
  ValidationMode,
  ValidationResultResponse,
} from '@/types/target';

const ACTIVE: MigrationStatus[] = ['running', 'pending'];

function anyActive(items: MigrationSummary[] | undefined): boolean {
  return (items ?? []).some((m) => ACTIVE.includes(m.status));
}

/**
 * The shared list query. Overview and Runs both use it, so switching screens
 * costs no extra request.
 *
 * The interval slows down instead of stopping: a stop condition that depends on
 * data only polling can fetch can never wake itself up, and this team starts
 * migrations from testenv scripts while the UI is open.
 */
export function useMigrations(filter?: MigrationListFilter) {
  return useQuery({
    queryKey: qk.centipede.migrations(filter),
    queryFn: () => centipedeApi.listAllMigrations(filter),
    refetchInterval: (q) =>
      anyActive(q.state.data as MigrationSummary[] | undefined)
        ? POLL.listActiveMs
        : POLL.listIdleMs,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    staleTime: STALE.migrations,
  });
}

/** Server-side pagination for the Runs screen. */
export function useMigrationsPage(filter: MigrationListFilter, page: number, pageSize = 20) {
  return useQuery({
    queryKey: qk.centipede.migrationsPage(filter, page, pageSize),
    queryFn: () => centipedeApi.listMigrations(filter, page, pageSize),
    refetchInterval: (q) => {
      const data = q.state.data as { items?: MigrationSummary[] } | undefined;
      return anyActive(data?.items) ? POLL.listActiveMs : POLL.listIdleMs;
    },
    refetchIntervalInBackground: false,
    staleTime: STALE.migrations,
  });
}

/** Stopping is right here: a finished record is immutable on the server too. */
export function useMigration(id: string | null, enabled = true) {
  return useQuery({
    queryKey: qk.centipede.migration(id ?? ''),
    queryFn: () => centipedeApi.getMigration(id as string),
    enabled: !!id && enabled,
    staleTime: 0,
    refetchInterval: (q) => {
      const m = q.state.data as { status?: MigrationStatus } | undefined;
      return m && ACTIVE.includes(m.status as MigrationStatus) ? POLL.detailMs : false;
    },
    refetchIntervalInBackground: false,
  });
}

export function useMigrationLogs(
  id: string | null,
  opts: { status?: string; page?: number; pageSize?: number } = {},
  running = false,
) {
  return useQuery({
    queryKey: qk.centipede.logs(id ?? '', { status: opts.status, page: opts.page }),
    queryFn: () => centipedeApi.listLogs(id as string, opts),
    enabled: !!id,
    staleTime: STALE.logs,
    refetchInterval: running ? POLL.logsMs : false,
    refetchIntervalInBackground: false,
  });
}

/**
 * Validation never changes on its own: it starts only when the user POSTs.
 *
 * Which is why the polling reads THIS response's own status rather than a flag
 * from the caller. The caller's flag used to come from the migration record,
 * which stops being refetched once the migration itself is finished: it froze at
 * validationStatus "running" and this query polled for the rest of the session,
 * long after the answer had arrived. Asked here, the poll ends when the server
 * says it has ended.
 *
 * `starting` covers the gap the response cannot: the POST is in flight and no
 * result has come back yet.
 */
export function useValidation(id: string | null, starting = false) {
  return useQuery({
    queryKey: qk.centipede.validation(id ?? ''),
    queryFn: () => centipedeApi.getValidation(id as string),
    enabled: !!id,
    staleTime: 0,
    refetchInterval: (q) => {
      const d = q.state.data as ValidationResultResponse | undefined;
      return d?.validationStatus === 'running' || starting ? POLL.detailMs : false;
    },
    refetchIntervalInBackground: false,
  });
}

// ── Mutations ───────────────────────────────────────────────────────────────
// No optimistic updates: statuses and counts are computed by the server from
// real connection attempts, and the client cannot predict them.

export function useCreateMigration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateMigrationReq) => centipedeApi.createMigration(req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['centipede', 'migrations'] });
      qc.invalidateQueries({ queryKey: ['centipede', 'migrationsPage'] });
    },
  });
}

export function useCancelMigration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => centipedeApi.cancelMigration(id),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: qk.centipede.migration(id) });
      qc.invalidateQueries({ queryKey: ['centipede', 'migrations'] });
      qc.invalidateQueries({ queryKey: ['centipede', 'migrationsPage'] });
    },
  });
}

export function useRetryMigration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, mode }: { id: string; mode: 'partial' | 'full' }) =>
      centipedeApi.retryMigration(id, mode),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['centipede', 'migrations'] });
      qc.invalidateQueries({ queryKey: ['centipede', 'migrationsPage'] });
    },
  });
}

/** Returns 409 while the migration is running — the caller says "cancel first". */
export function useDeleteMigration() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => centipedeApi.deleteMigration(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['centipede', 'migrations'] });
      qc.invalidateQueries({ queryKey: ['centipede', 'migrationsPage'] });
    },
  });
}

export function useStartValidation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, mode }: { id: string; mode: ValidationMode }) =>
      centipedeApi.startValidation(id, mode),
    onSuccess: (_d, { id }) => {
      qc.invalidateQueries({ queryKey: qk.centipede.validation(id) });
      qc.invalidateQueries({ queryKey: qk.centipede.migration(id) });
    },
  });
}
