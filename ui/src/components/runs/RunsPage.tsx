'use client';

import React from 'react';
import { useMigrationsPage } from '@/queries/centipede';
import { POLL } from '@/queries/keys';
import {
  ErrorNote,
  LivePill,
  Loading,
  PageHeader,
  Panel,
  RefreshButton,
  Select,
  EmptyState,
  Button,
  UpdatedStamp,
} from '@/components/ui/primitives';
import { RunCard } from './RunCard';
import { categoryOfSummary } from '@/components/ui/status';
import { CATEGORIES, CATEGORY_LABEL, type Category } from '@/lib/connectionCategory';
import { useAnchoredAccordion } from '@/hooks/useAnchoredAccordion';
import type { MigrationSummary } from '@/types/target';

const STATUSES = ['all', 'pending', 'running', 'completed', 'failed', 'cancelled'];

// One empty list for "no data yet", so the visible memo is not rebuilt each render.
const NO_RUNS: MigrationSummary[] = [];

const PAGE_SIZE = 10;

export function RunsPage() {
  const [status, setStatus] = React.useState('all');
  const [category, setCategory] = React.useState<'all' | Category>('all');
  const [page, setPage] = React.useState(1);
  const [openId, setOpenId] = React.useState<string | null>(null);

  // Same exclusive accordion as the source groups, so it needs the same
  // protection against the collapse above pulling the clicked card upward.
  const { register, anchorTo } = useAnchoredAccordion(openId);

  const toggleRun = (id: string) => {
    anchorTo(id);
    setOpenId((cur) => (cur === id ? null : id));
  };

  const filter = React.useMemo(
    () => ({
      ...(status === 'all' ? {} : { status }),
      // The Runs screen is the all-time view: Overview hands the full history
      // over to it, so the seven-day default must be switched off here.
      lastDays: 0,
    }),
    [status],
  );

  const query = useMigrationsPage(filter, page, PAGE_SIZE);
  const items = query.data?.items ?? NO_RUNS;

  // The category is derived from planSummary, which the server cannot filter
  // on, so this one narrowing happens client-side over the fetched page.
  const visible = React.useMemo(
    () => (category === 'all' ? items : items.filter((m) => categoryOfSummary(m) === category)),
    [items, category],
  );

  const total = query.data?.total ?? 0;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const anyActive = items.some((m) => m.status === 'running' || m.status === 'pending');

  return (
    <div className="space-y-4">
      <PageHeader
        title="Runs"
        subtitle={<span>all time · server-side pagination</span>}
        right={
          <>
            <LivePill intervalMs={anyActive ? POLL.listActiveMs : POLL.listIdleMs} />
            <UpdatedStamp at={query.dataUpdatedAt} />
            <RefreshButton onClick={() => query.refetch()} busy={query.isFetching} />
          </>
        }
      />

      <Panel className="py-3">
        <div className="flex flex-wrap items-end gap-4">
          <label className="text-xs text-text-muted">
            <span className="block mb-1 font-bold">status</span>
            <Select
              value={status}
              onChange={(e) => {
                setStatus(e.target.value);
                setPage(1);
              }}
              className="w-44"
            >
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          </label>

          <label className="text-xs text-text-muted">
            <span className="block mb-1 font-bold">category</span>
            <Select
              value={category}
              onChange={(e) => setCategory(e.target.value as 'all' | Category)}
              className="w-44"
            >
              <option value="all">all</option>
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {CATEGORY_LABEL[c]}
                </option>
              ))}
            </Select>
          </label>

          <span className="text-xs text-text-muted font-mono ml-auto">
            {total} run{total === 1 ? '' : 's'} · page {page} / {pageCount}
          </span>
        </div>
      </Panel>

      <ErrorNote error={query.error} />

      {query.isPending ? (
        <Loading />
      ) : visible.length === 0 ? (
        <EmptyState>
          {items.length === 0
            ? 'No migration matches this filter.'
            : 'No run on this page belongs to that category.'}
        </EmptyState>
      ) : (
        <div className="space-y-3">
          {visible.map((m) => (
            <div key={m.id} ref={register(m.id)}>
              <RunCard
                summary={m}
                open={openId === m.id}
                onToggle={() => toggleRun(m.id)}
              />
            </div>
          ))}
        </div>
      )}

      {pageCount > 1 && (
        <div className="flex items-center justify-center gap-3 pt-2">
          <Button onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={page <= 1}>
            ← prev
          </Button>
          <span className="text-xs font-mono text-text-muted">
            {page} / {pageCount}
          </span>
          <Button
            onClick={() => setPage((p) => Math.min(pageCount, p + 1))}
            disabled={page >= pageCount}
          >
            next →
          </Button>
        </div>
      )}
    </div>
  );
}
