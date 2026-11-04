'use client';

import React from 'react';
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { honeybeeApi, type ImportReq } from '@/api/honeybee';
import { POLL, STALE, qk } from './keys';
import { hasPendingAgent, type Category } from '@/lib/connectionCategory';
import type {
  ConnectionInfo,
  CreateConnectionInfoReq,
  CreateSourceGroupReq,
  ListConnectionInfoRes,
} from '@/types/honeybee';

export function useSourceGroups(enabled = true) {
  return useQuery({
    queryKey: qk.honeybee.sourceGroups(),
    queryFn: () => honeybeeApi.listSourceGroups(),
    enabled,
    staleTime: STALE.sourceGroups,
    refetchInterval: POLL.connectionsIdleMs,
    refetchIntervalInBackground: false,
  });
}

/**
 * Lazy: only an expanded group is fetched, and only an expanded group polls.
 * The cadence accelerates while a check has not settled — an SSH registration
 * installs an agent and takes tens of seconds.
 */
export function useConnections(sgId: string, expanded: boolean) {
  return useQuery({
    queryKey: qk.honeybee.connections(sgId),
    queryFn: () => honeybeeApi.listConnections(sgId),
    enabled: expanded && !!sgId,
    staleTime: STALE.connections,
    refetchInterval: (q) => {
      const data = q.state.data as ListConnectionInfoRes | undefined;
      return hasPendingAgent(data?.connection_info ?? [])
        ? POLL.connectionsActiveMs
        : POLL.connectionsIdleMs;
    },
    refetchIntervalInBackground: false,
  });
}

/** The scan result the plan call consumes verbatim. */
export function useRefined(
  category: Category | null,
  sgId: string | null,
  connId: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: qk.honeybee.refined(category ?? 'filesystem', sgId ?? '', connId ?? ''),
    queryFn: () =>
      honeybeeApi.getRefinedScope(category as Category, sgId as string, connId as string),
    enabled: enabled && !!category && !!sgId && !!connId,
    staleTime: STALE.refined,
    retry: false,
  });
}

/**
 * The scan result of one connection or, with `connId` null, of the whole
 * source group. The group form is keyed '*' so a single connection's import
 * can invalidate both.
 */
export function useRefinedScope(
  category: Category | null,
  sgId: string | null,
  connId: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: qk.honeybee.refined(category ?? 'filesystem', sgId ?? '', connId ?? '*'),
    queryFn: () => honeybeeApi.getRefinedScope(category as Category, sgId as string, connId),
    enabled: enabled && !!category && !!sgId,
    staleTime: STALE.refined,
    retry: false,
  });
}

/**
 * Each connection's own refined read, for the connections a group read left out.
 *
 * The group response drops a connection without saying why. Its own read says
 * it: no scan yet, or a stale one (isStaleScanError). Same key as useRefined, so
 * a connection already read one way is not read again the other.
 */
export function useRefinedEach(
  category: Category | null,
  sgId: string | null,
  connIds: string[],
  enabled = true,
) {
  return useQueries({
    queries: connIds.map((connId) => ({
      queryKey: qk.honeybee.refined(category ?? 'filesystem', sgId ?? '', connId),
      queryFn: () => honeybeeApi.getRefinedScope(category as Category, sgId as string, connId),
      enabled: enabled && !!category && !!sgId,
      staleTime: STALE.refined,
      retry: false,
    })),
  });
}

export function useCreateSourceGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: CreateSourceGroupReq) => honeybeeApi.createSourceGroup(req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.honeybee.sourceGroups() });
    },
  });
}

export function useDeleteSourceGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sgId: string) => honeybeeApi.deleteSourceGroup(sgId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.honeybee.sourceGroups() });
    },
  });
}

/**
 * isPending doubles as the agent-installation progress signal: the call answers
 * 200 either way, so the outcome is read from the returned statuses, not from
 * the HTTP code.
 */
export function useCreateConnection(sgId: string) {
  const qc = useQueryClient();
  return useMutation<ConnectionInfo, Error, CreateConnectionInfoReq>({
    mutationFn: (req) => honeybeeApi.createConnection(sgId, req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.honeybee.connections(sgId) });
      // The group roll-up counts are recomputed server-side.
      qc.invalidateQueries({ queryKey: qk.honeybee.sourceGroups() });
    },
  });
}

export function useDeleteConnection(sgId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (connId: string) => honeybeeApi.deleteConnection(sgId, connId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.honeybee.connections(sgId) });
      qc.invalidateQueries({ queryKey: qk.honeybee.sourceGroups() });
    },
  });
}

export function useRefreshConnection(sgId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (connId: string) => honeybeeApi.refreshConnection(sgId, connId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.honeybee.connections(sgId) });
      qc.invalidateQueries({ queryKey: qk.honeybee.sourceGroups() });
    },
  });
}

export interface RunImportVars {
  category: Category;
  sgId: string;
  connId: string;
  req: ImportReq;
}

export function useRunImport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ category, sgId, connId, req }: RunImportVars) =>
      honeybeeApi.runImport(category, sgId, connId, req),
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: qk.honeybee.refined(v.category, v.sgId, v.connId) });
      // The group's response holds this connection's entry too.
      qc.invalidateQueries({ queryKey: qk.honeybee.refined(v.category, v.sgId, '*') });
      qc.invalidateQueries({ queryKey: qk.honeybee.connections(v.sgId) });
    },
  });
}

/** How many imports run at once when several connections are inspected. */
const IMPORT_CONCURRENCY = 3;

export type ImportRun =
  | { state: 'running' }
  | { state: 'done' }
  | { state: 'failed'; error: unknown };

/**
 * Inspects several connections of one group, a few at a time, and keeps each
 * one's outcome.
 *
 * Not cm-honeybee's group import: that one walks the connections in order and
 * stops at the first failure, so a group of N hosts takes N times as long, can
 * outlast the HTTP timeout, and says nothing about the hosts after the one that
 * failed. One import per connection lets every host finish or fail on its own.
 */
export function useRunImports() {
  const qc = useQueryClient();
  const [runs, setRuns] = React.useState<Record<string, ImportRun>>({});
  // Bumped by reset(): a run still in flight for the previous selection must
  // not write its outcome over the new one.
  const generation = React.useRef(0);

  const run = React.useCallback(
    async (category: Category, sgId: string, targets: Array<{ connId: string; req: ImportReq }>) => {
      const gen = generation.current;
      const settle = (connId: string, r: ImportRun) => {
        if (generation.current === gen) setRuns((cur) => ({ ...cur, [connId]: r }));
      };
      setRuns((cur) => {
        const next = { ...cur };
        for (const t of targets) next[t.connId] = { state: 'running' };
        return next;
      });

      const queue = [...targets];
      const worker = async () => {
        for (let t = queue.shift(); t; t = queue.shift()) {
          try {
            await honeybeeApi.runImport(category, sgId, t.connId, t.req);
            settle(t.connId, { state: 'done' });
            qc.invalidateQueries({ queryKey: qk.honeybee.refined(category, sgId, t.connId) });
            qc.invalidateQueries({ queryKey: qk.honeybee.refined(category, sgId, '*') });
          } catch (error) {
            settle(t.connId, { state: 'failed', error });
          }
        }
      };
      await Promise.allSettled(
        Array.from({ length: Math.min(IMPORT_CONCURRENCY, targets.length) }, worker),
      );
      qc.invalidateQueries({ queryKey: qk.honeybee.connections(sgId) });
    },
    [qc],
  );

  const reset = React.useCallback(() => {
    generation.current++;
    setRuns({});
  }, []);

  const running = Object.values(runs).some((r) => r.state === 'running');
  return { runs, run, reset, running };
}
