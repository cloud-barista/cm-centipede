'use client';

// One run, expanded downward into plan summary, logs and validation — never
// into a side pane.

import React from 'react';
import {
  AccordionRow,
  Button,
  ErrorNote,
  MigrationDuration,
  Note,
  ProgressBar,
} from '@/components/ui/primitives';
import { CategoryBadge, StatusBadge, ValidationBadge, categoryOfSummary } from '@/components/ui/status';
import { CATEGORY_ACCENT } from '@/components/ui/primitives';
import {
  useCancelMigration,
  useDeleteMigration,
  useMigration,
} from '@/queries/centipede';
import { formatBytes, formatCount, timeAgo } from '@/lib/format';
import { LogTable } from './LogTable';
import { ValidationPanel } from './ValidationPanel';
import { RetryModal } from './RetryModal';
import { PlanSummaryView } from './PlanSummaryView';
import type { MigrationSummary } from '@/types/target';
import { ApiError } from '@/api/http';

export function RunCard({
  summary,
  open,
  onToggle,
}: {
  summary: MigrationSummary;
  open: boolean;
  onToggle: () => void;
}) {
  const cat = categoryOfSummary(summary);
  const bar = cat ? CATEGORY_ACCENT[cat].bar : 'bg-indigo-500';
  const running = summary.status === 'running' || summary.status === 'pending';

  // The full record — with its plan — is fetched only for the expanded card.
  const detail = useMigration(summary.id, open);
  const cancel = useCancelMigration();
  const del = useDeleteMigration();
  const [retryOpen, setRetryOpen] = React.useState(false);

  const deleteBlocked =
    del.error instanceof ApiError && del.error.status === 409;

  return (
    <>
      <AccordionRow
        open={open}
        onToggle={onToggle}
        summary={
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-bold text-sm truncate">{summary.name}</span>
              <CategoryBadge category={cat} />
              <StatusBadge status={summary.status} />
              <span
                className="text-xs font-mono text-text-muted"
                title={new Date(summary.createdAt).toLocaleString()}
              >
                {timeAgo(summary.createdAt)}
              </span>
            </div>
            {/* Progress and size are the point of this row, so the figures stay
                at full contrast and only their units are muted. */}
            <div className="text-xs font-mono mt-1 flex flex-wrap gap-x-4 gap-y-1">
              <span>
                {formatCount(summary.processedItems)}/{formatCount(summary.totalItems)}
                <span className="text-text-muted"> items</span>
              </span>
              {summary.transferredBytes > 0 && (
                <span>{formatBytes(summary.transferredBytes)}</span>
              )}
              {summary.failedItems > 0 && (
                <span className="text-rose-600 dark:text-rose-400">{summary.failedItems} failed</span>
              )}
              <MigrationDuration migration={summary} />
              {/* As requested; the default is not shown, it is what "no
                  figure" means. Each item's log row says what it ran at. */}
              {summary.concurrency ? (
                <span>
                  {summary.concurrency}
                  <span className="text-text-muted"> concurrency</span>
                </span>
              ) : null}
              <ValidationBadge status={summary.validationStatus} />
            </div>
            {running && (
              <ProgressBar
                value={summary.processedItems}
                total={summary.totalItems}
                barClass={bar}
                className="mt-2 max-w-md"
              />
            )}
          </div>
        }
        right={
          running ? (
            <Button variant="danger" onClick={() => cancel.mutate(summary.id)}>
              cancel
            </Button>
          ) : undefined
        }
      >
        {summary.statusMessage && (
          <div className="mb-3">
            <Note tone={summary.status === 'failed' ? 'bad' : 'info'}>{summary.statusMessage}</Note>
          </div>
        )}

        <PlanSummaryView detail={detail.data ?? null} pending={detail.isPending} error={detail.error} />

        <div className="mt-5">
          <LogTable migrationId={summary.id} running={running} />
        </div>

        <div className="mt-5">
          <ValidationPanel migration={summary} />
        </div>

        <div className="mt-5 flex flex-wrap items-center gap-2 border-t border-border-main pt-4">
          <Button onClick={() => setRetryOpen(true)} disabled={running}>
            retry…
          </Button>
          <Button
            variant="danger"
            onClick={() => del.mutate(summary.id)}
            disabled={del.isPending}
          >
            delete run
          </Button>
          <span className="text-xs text-text-muted">
            partial re-runs failed items · full drops the target first
          </span>
        </div>

        {deleteBlocked ? (
          <div className="mt-3">
            <Note tone="warn">
              This migration is still running, so it cannot be deleted. Cancel it first, then delete.
            </Note>
          </div>
        ) : (
          <div className="mt-3">
            <ErrorNote error={del.error} />
          </div>
        )}
        <ErrorNote error={cancel.error} />
      </AccordionRow>

      {retryOpen && (
        <RetryModal
          migration={summary}
          onClose={() => setRetryOpen(false)}
        />
      )}
    </>
  );
}
