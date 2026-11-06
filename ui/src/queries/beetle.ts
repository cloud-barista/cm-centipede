'use client';

import React from 'react';
import { useMutation, useQueries, useQuery } from '@tanstack/react-query';
import { beetleApi } from '@/api/beetle';
import { ApiError } from '@/api/http';
import {
  locatorKey,
  locatorOf,
  locatorsOf,
  presenceIn,
  type TargetKind,
  type TargetPresence,
} from '@/lib/targetPresence';
import type { ConnectionRef } from '@/types/common';
import { POLL, STALE, qk } from './keys';

export function useBeetleInfra(nsId: string, enabled = true) {
  return useQuery({
    queryKey: qk.beetle.infra(nsId),
    queryFn: () => beetleApi.listInfra(nsId),
    enabled: enabled && !!nsId,
    staleTime: STALE.beetle,
  });
}

export function useBeetleObjectStorages(nsId: string, enabled = true) {
  return useQuery({
    queryKey: qk.beetle.objectStorage(nsId),
    queryFn: () => beetleApi.listObjectStorages(nsId),
    enabled: enabled && !!nsId,
    staleTime: STALE.beetle,
  });
}

export function useBeetleRDBMS(nsId: string, enabled = true) {
  return useQuery({
    queryKey: qk.beetle.rdbms(nsId),
    queryFn: () => beetleApi.listRDBMS(nsId),
    enabled: enabled && !!nsId,
    staleTime: STALE.beetle,
  });
}

/**
 * Manual only. The probe opens a real SSH connection to every node and
 * cm-beetle puts a route-level cooldown in front of it, so it never polls.
 */
export function useCheckSSHReady() {
  return useMutation({
    mutationFn: ({ nsId, infraId }: { nsId: string; infraId: string }) =>
      beetleApi.checkSSHReady(nsId, infraId),
  });
}

// ── Target presence ─────────────────────────────────────────────────────────

/** The one list per kind, keyed exactly as the picker's own queries are. */
const TARGET_LIST: Record<
  TargetKind,
  { key: (nsId: string) => readonly unknown[]; fetch: (nsId: string) => Promise<unknown> }
> = {
  infra: { key: qk.beetle.infra, fetch: beetleApi.listInfra },
  objectStorage: { key: qk.beetle.objectStorage, fetch: beetleApi.listObjectStorages },
  rdbms: { key: qk.beetle.rdbms, fetch: beetleApi.listRDBMS },
};

/**
 * How long to wait after cm-beetle said it was being rate-limited.
 *
 * Its 503 carries a Retry-After, and obeying it is the whole point: a poller
 * that keeps its cadence through provider backpressure is what the header
 * exists to stop. The constant is only the fallback for a 503 without one.
 */
function backoffMs(error: unknown): number | null {
  if (!(error instanceof ApiError) || error.status !== 503) return null;
  const fromHeader = error.retryAfterSec;
  if (fromHeader === undefined) return POLL.targetPresenceBackoffMs;
  // Never shorter than the ordinary cadence: Retry-After is a floor on waiting,
  // not an invitation to poll faster than usual.
  return Math.max(fromHeader * 1000, POLL.targetPresenceMs);
}

/**
 * One thing to check. The caller supplies `key` rather than this hook deriving
 * it: the shortlist already identifies its entries by label, and a second
 * function reconstructing that format would go quietly out of step with the
 * picker that writes it — every row would read `unknown` and nothing would say
 * why.
 */
export interface TargetPresenceEntry {
  key: string;
  ref: ConnectionRef;
}

export interface TargetPresenceResult {
  /** Keyed by whatever the caller passed as `key`. */
  presence: Map<string, TargetPresence>;
  /** True while at least one namespace could not be read at all. */
  unreadable: boolean;
  /** Cadence currently in effect, for the LivePill — longer under backpressure. */
  intervalMs: number;
  /** Oldest successful read across the namespaces: everything is at least this fresh. */
  updatedAt: number | null;
  isPending: boolean;
}

/**
 * Checks a set of cm-beetle references against cm-beetle, periodically.
 *
 * One request per distinct (namespace, kind) rather than per reference, sharing
 * the picker's query keys so an open picker makes the overlapping half free.
 * See lib/targetPresence.ts for why this reads lists instead of the single-item
 * GETs cm-beetle also offers.
 *
 * It only runs where it is mounted — the Targets section and the wizard's Step
 * 2 — so no other screen pays for it, and refetchIntervalInBackground stays off
 * so a hidden tab pays nothing either.
 */
export function useTargetPresence(entries: TargetPresenceEntry[]): TargetPresenceResult {
  const locators = React.useMemo(
    () => locatorsOf(entries.map((e) => e.ref)),
    // The array identity changes on every render of a caller that maps in
    // place; its content is what matters, and a locator set is small.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [entries.map((e) => e.key).join('|')],
  );

  const results = useQueries({
    queries: locators.map((l) => ({
      queryKey: TARGET_LIST[l.kind].key(l.nsId),
      queryFn: () => TARGET_LIST[l.kind].fetch(l.nsId),
      staleTime: STALE.beetle,
      refetchInterval: (q: { state: { error: unknown } }) =>
        backoffMs(q.state.error) ?? POLL.targetPresenceMs,
      refetchIntervalInBackground: false,
    })),
  });

  const byLocator = new Map(locators.map((l, i) => [locatorKey(l), results[i]]));

  const presence = new Map<string, TargetPresence>();
  for (const { key, ref } of entries) {
    const l = locatorOf(ref);
    if (!l) continue;
    const q = byLocator.get(locatorKey(l));
    // No answer yet, or no answer at all. Neither is evidence that the target
    // is gone, and neither may be rendered as if it were.
    presence.set(
      key,
      q && q.data !== undefined ? presenceIn(ref, q.data) : { state: 'unknown' },
    );
  }

  const succeeded = results.filter((r) => r.data !== undefined);

  return {
    presence,
    unreadable: results.some((r) => r.error != null),
    intervalMs: results.reduce<number>(
      (acc, r) => Math.max(acc, backoffMs(r.error) ?? POLL.targetPresenceMs),
      POLL.targetPresenceMs,
    ),
    updatedAt: succeeded.length === 0 ? null : Math.min(...succeeded.map((r) => r.dataUpdatedAt)),
    isPending: results.some((r) => r.isPending),
  };
}
