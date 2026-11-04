'use client';

// The "last 7 days" screen. The window is stated in the header rather than
// implied, and only the ACTIVE panel looks at all time — a migration running
// longer than a week must not vanish while it is still running.

import React from 'react';
import { useMigrations } from '@/queries/centipede';
import { POLL } from '@/queries/keys';
import { useUiStore } from '@/store/uiStore';
import {
  ErrorNote,
  LivePill,
  Loading,
  PageHeader,
  Panel,
  RefreshButton,
  SectionBand,
  UpdatedStamp,
} from '@/components/ui/primitives';
import { KpiTiles } from './KpiTiles';
import { ActiveRuns } from './ActiveRuns';
import { CategoryBars } from './CategoryBars';
import { RecentRuns } from './RecentRuns';
import { useNow } from '@/hooks/useNow';

export function OverviewPage() {
  const setActiveTab = useUiStore((s) => s.setActiveTab);

  // Two calls draw this screen. Everything the counts, the category split and
  // the recent list need is already on the Migration record.
  const window7 = useMigrations({ lastDays: 7 });
  const active = useMigrations({ status: 'running', lastDays: 0 });

  // Passed down to the run lists, whose stamps are per row. The header's own
  // stamp keeps its clock inside UpdatedStamp.
  const now = useNow();

  const items = window7.data ?? [];
  const hasActive = (active.data?.length ?? 0) > 0;
  const listInterval = hasActive ? POLL.listActiveMs : POLL.listIdleMs;

  return (
    <div className="space-y-4">
      <PageHeader
        title="Overview"
        subtitle={
          <span>
            last 7 days · counts, categories and recent runs are scoped to this window
          </span>
        }
        right={
          <>
            <LivePill intervalMs={listInterval} />
            <UpdatedStamp at={window7.dataUpdatedAt} />
            <RefreshButton
              onClick={() => {
                window7.refetch();
                active.refetch();
              }}
              busy={window7.isFetching || active.isFetching}
            />
          </>
        }
      />

      <ErrorNote error={window7.error} />

      <SectionBand title="Migration runs" hint="last 7 days" />
      {window7.isPending ? <Loading /> : <KpiTiles items={items} />}

      <ActiveRuns
        runs={active.data ?? []}
        isPending={active.isPending}
        error={active.error}
      />

      <SectionBand title="By category" hint="last 7 days" />
      {window7.isPending ? (
        <Panel>
          <Loading />
        </Panel>
      ) : (
        <CategoryBars items={items} />
      )}

      <SectionBand
        title="Recent"
        right={
          <button
            type="button"
            onClick={() => setActiveTab('runs')}
            className="text-xs font-bold text-indigo-600 dark:text-indigo-400 hover:text-indigo-700 dark:hover:text-indigo-300 cursor-pointer font-mono"
          >
            see all runs →
          </button>
        }
      />
      {window7.isPending ? (
        <Panel>
          <Loading />
        </Panel>
      ) : (
        <RecentRuns items={items} now={now} onOpen={() => setActiveTab('runs')} />
      )}
    </div>
  );
}
