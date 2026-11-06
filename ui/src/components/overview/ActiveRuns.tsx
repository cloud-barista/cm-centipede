'use client';

// The only panel on this screen that looks at all time (lastDays=0): a
// migration still running after seven days must not disappear from it. Narrowed
// to status=running, so the row count stays small.

import React from 'react';
import { useCancelMigration } from '@/queries/centipede';
import { POLL } from '@/queries/keys';
import {
  Button,
  ErrorNote,
  LivePill,
  Loading,
  MigrationDuration,
  Panel,
  ProgressBar,
  SectionBand,
} from '@/components/ui/primitives';
import { CategoryBadge, StatusBadge, categoryOfSummary } from '@/components/ui/status';
import { CATEGORY_ACCENT } from '@/components/ui/primitives';
import {
  currentObjectLabel,
  dbStepFigures,
  formatBytes,
  formatCount,
  transferStepLabel,
} from '@/lib/format';
import type { MigrationSummary } from '@/types/target';

export function ActiveRuns({
  runs,
  isPending,
  error,
}: {
  runs: MigrationSummary[];
  isPending: boolean;
  error: unknown;
}) {
  const cancel = useCancelMigration();

  return (
    <>
      <SectionBand
        title={`Active (${runs.length})`}
        hint="all time"
        right={<LivePill intervalMs={POLL.listActiveMs} />}
      />
      <Panel>
        <ErrorNote error={error} />
        {isPending ? (
          <Loading />
        ) : runs.length === 0 ? (
          <p className="text-sm text-text-muted text-center py-4">
            No migration is running right now.
          </p>
        ) : (
          <ul className="divide-y divide-border-main">
            {runs.map((m) => {
              const cat = categoryOfSummary(m);
              const bar = cat ? CATEGORY_ACCENT[cat].bar : 'bg-indigo-500';
              const stepLabel = transferStepLabel(
                m.currentStep,
                m.currentSteps,
                m.currentStepName,
                m.currentStepDetail,
              );
              // As in Step 5: a byte total only for a single item, whose
              // transferredBytes holds nothing but the item in flight.
              const bytesOf =
                m.totalItems <= 1 && m.currentBytesTotal ? m.currentBytesTotal : undefined;
              return (
                <li key={m.id} className="py-3 first:pt-0 last:pb-0">
                  <div className="flex flex-wrap items-center gap-2 mb-2">
                    <span className="font-bold text-sm truncate">{m.name}</span>
                    <CategoryBadge category={cat} />
                    <StatusBadge status={m.status} />
                    <div className="ml-auto flex gap-2">
                      <Button
                        variant="danger"
                        onClick={() => cancel.mutate(m.id)}
                        disabled={cancel.isPending}
                      >
                        cancel
                      </Button>
                    </div>
                  </div>

                  <div>
                    <div className="flex justify-between text-2xs font-mono text-text-muted mb-1">
                      <span>items</span>
                      <span>
                        {formatCount(m.processedItems)} / {formatCount(m.totalItems)}
                        {m.failedItems > 0 && (
                          <span className="text-rose-600 dark:text-rose-400"> · {m.failedItems} failed</span>
                        )}
                        {/* What moved is a figure on this line, with its total
                            only where the server knows one (a restore's dump).
                            A relay's restarts at zero with each step, so the
                            step stays beside it even at zero. */}
                        {(m.transferredBytes > 0 || stepLabel) && (
                          <span>
                            {' · '}
                            {formatBytes(m.transferredBytes)}
                            {bytesOf !== undefined && ` / ${formatBytes(bytesOf)}`}
                          </span>
                        )}
                        {stepLabel && <span> · {stepLabel}</span>}
                        {dbStepFigures(m).map((f) => (
                          <span key={f}> · {f}</span>
                        ))}
                        {m.startedAt && (
                          <>
                            {' · '}
                            <MigrationDuration migration={m} bare />
                          </>
                        )}
                      </span>
                    </div>
                    {/* A restore of a known-size dump has a real fraction to
                        show; one item otherwise reads 0 or 100% by count. */}
                    {bytesOf !== undefined ? (
                      <ProgressBar value={m.transferredBytes} total={bytesOf} barClass={bar} />
                    ) : (
                      <ProgressBar value={m.processedItems} total={m.totalItems} barClass={bar} />
                    )}
                    {/* The only line that moves while one large item is copying.
                        A summary carries no plan, so no dbType: a MongoDB run
                        reads "table" here too. */}
                    {m.currentObject && (
                      <p className="flex gap-1.5 text-2xs font-mono text-text-muted mt-1 min-w-0">
                        <span className="shrink-0">{currentObjectLabel(cat)}</span>
                        <span className="truncate" title={m.currentObject}>
                          {m.currentObject}
                        </span>
                      </p>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        <ErrorNote error={cancel.error} />
      </Panel>
    </>
  );
}
