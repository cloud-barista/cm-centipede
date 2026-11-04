'use client';

// A failed DBMS item says where it failed AND what happened to the target. The
// targetCreated + rollbackStatus pair decides whether the database was dropped,
// emptied or left as it was — the first thing a reader needs.

import React from 'react';
import { useMigrationLogs } from '@/queries/centipede';
import { Badge, Button, ErrorNote, Loading, Select } from '@/components/ui/primitives';
import { formatBytes, formatDuration } from '@/lib/format';
import type { MigrationLog } from '@/types/target';

const PAGE_SIZE = 20;

const LOG_STATUSES = ['all', 'success', 'failed', 'skipped'];

/** Puts the two rollback fields into one sentence. */
export function rollbackSentence(log: MigrationLog): string | null {
  if (log.status !== 'failed') return null;
  if (!log.targetCreated) {
    return 'The target already existed before this run, so it was left as it was.';
  }
  switch (log.rollbackStatus) {
    case 'success':
      return 'The target was created by this run and has been rolled back — it was dropped.';
    case 'failed':
      return `The target was created by this run and the rollback failed, so it is still there${
        log.rollbackErrorMsg ? `: ${log.rollbackErrorMsg}` : '.'
      }`;
    case 'skipped':
      return 'The target was created by this run and kept on purpose (dbmsOnFailure=keep).';
    default:
      return 'The target was created by this run; no rollback was recorded.';
  }
}

export function LogTable({
  migrationId,
  running,
}: {
  migrationId: string;
  running: boolean;
}) {
  const [status, setStatus] = React.useState('all');
  const [page, setPage] = React.useState(1);

  const query = useMigrationLogs(
    migrationId,
    { status: status === 'all' ? undefined : status, page, pageSize: PAGE_SIZE },
    running,
  );

  const logs = query.data?.items ?? [];
  const total = query.data?.total ?? 0;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div>
      <div className="flex flex-wrap items-center gap-2 mb-2">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Logs ({total})
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        <Select
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
            setPage(1);
          }}
          className="w-32 !py-1 text-xs"
        >
          {LOG_STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
        <span className="text-2xs font-mono text-text-muted">
          page {page} / {pageCount}
        </span>
      </div>

      <ErrorNote error={query.error} />

      {query.isPending ? (
        <Loading label="loading logs…" />
      ) : logs.length === 0 ? (
        <p className="text-xs text-text-muted py-3">No log entry matches this filter.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs font-mono">
            <thead>
              <tr className="text-2xs uppercase tracking-wider text-text-muted border-b border-border-main">
                <th className="text-left font-bold px-2 py-1.5">item</th>
                <th className="text-left font-bold px-2 py-1.5">status</th>
                <th className="text-right font-bold px-2 py-1.5">size</th>
                <th className="text-right font-bold px-2 py-1.5">dur</th>
                <th className="text-right font-bold px-2 py-1.5">conc</th>
                <th className="text-left font-bold px-2 py-1.5">object</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((log) => {
                const sentence = rollbackSentence(log);
                return (
                  <React.Fragment key={log.id}>
                    <tr className="border-b border-border-main/60">
                      <td className="px-2 py-1.5 max-w-[22rem] truncate" title={log.itemPath}>
                        {log.itemPath}
                      </td>
                      <td className="px-2 py-1.5">
                        <Badge
                          tone={
                            log.status === 'success'
                              ? 'ok'
                              : log.status === 'failed'
                                ? 'bad'
                                : 'neutral'
                          }
                        >
                          {log.status}
                        </Badge>
                      </td>
                      <td className="px-2 py-1.5 text-right">{formatBytes(log.sizeBytes)}</td>
                      <td className="px-2 py-1.5 text-right">{formatDuration(log.durationMs)}</td>
                      {/* What the item ran at, as data-mig settled on it; "-"
                          for an item that did not say. */}
                      <td className="px-2 py-1.5 text-right" title={log.concurrencyNote}>
                        {log.appliedConcurrency || '-'}
                      </td>
                      <td className="px-2 py-1.5 text-text-muted">
                        {log.failedObjectKind
                          ? `${log.failedObjectKind} ${log.failedObjectName ?? ''}`
                          : '-'}
                      </td>
                    </tr>
                    {(log.errorMsg || sentence) && (
                      <tr className="border-b border-border-main/60">
                        <td colSpan={6} className="px-2 pb-2 text-xs">
                          {log.errorMsg && (
                            <div className="text-rose-600 dark:text-rose-400">error &nbsp;{log.errorMsg}</div>
                          )}
                          {sentence && <div className="text-text-muted">target &nbsp;{sentence}</div>}
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {pageCount > 1 && (
        <div className="flex items-center gap-2 mt-2">
          <Button onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={page <= 1}>
            ← prev
          </Button>
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
