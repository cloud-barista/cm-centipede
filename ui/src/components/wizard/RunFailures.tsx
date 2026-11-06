'use client';

// Why a run failed, in the step that started it.
//
// The step used to say "failed 3" in the same muted grey as the byte counter,
// with the server's statusMessage beside it — a failure set in the type of a
// metric, and no word anywhere about what actually went wrong with those three
// items. The reasons were a screen away, on Runs, which is not where someone
// watching their own run is looking.
//
// The first few reasons, not all of them: a run with hundreds of failures is a
// log to read, and Runs is the screen that pages, filters and retries. This says
// enough to know what happened and whether it is worth going there.

import React from 'react';
import { Note } from '@/components/ui/primitives';
import { useMigrationLogs } from '@/queries/centipede';
import type { Migration, MigrationLog } from '@/types/target';

/** How many failed items are spelled out here. */
const SHOWN = 5;

/** What happened to a DBMS target after the item failed, where the log says. */
function rollbackNote(log: MigrationLog): string | null {
  if (log.targetCreated === undefined && log.rollbackStatus === undefined) return null;
  if (!log.targetCreated) return 'target existed before this run — left as it was';
  switch (log.rollbackStatus) {
    case 'success':
      return 'target created by this run — rolled back';
    case 'failed':
      return `target created by this run — rollback FAILED${
        log.rollbackErrorMsg ? `: ${log.rollbackErrorMsg}` : ', it is still there'
      }`;
    case 'skipped':
      return 'target created by this run — kept on purpose (dbmsOnFailure=keep)';
    default:
      return 'target created by this run — no rollback recorded';
  }
}

export function RunFailures({
  migration,
  running,
}: {
  migration: Migration;
  /** Keeps the reasons arriving while items are still being processed. */
  running: boolean;
}) {
  const failed = migration.failedItems ?? 0;
  const runFailed = migration.status === 'failed';

  // Asked for only when there is something to ask about: a run with no failed
  // item has no failed log, and this must not add a request to every poll of a
  // healthy run.
  const logs = useMigrationLogs(
    failed > 0 ? migration.id : null,
    { status: 'failed', page: 1, pageSize: SHOWN },
    running,
  );

  if (!runFailed && failed === 0) return null;

  const items = logs.data?.items ?? [];
  // Against the log endpoint's own total, not migration.failedItems: the two
  // are counted by different sides, and "… and -1 more" is what subtracting
  // across them would eventually print.
  const more = Math.max(0, (logs.data?.total ?? 0) - items.length);

  return (
    <Note tone="bad">
      <div className="space-y-1.5">
        <p className="font-bold">
          {runFailed
            ? 'This migration failed.'
            : /* The run itself finished, so nothing else will say this. */
              `${failed} item${failed === 1 ? '' : 's'} failed.`}
        </p>

        {/* The server's own words about the run, kept verbatim. */}
        {migration.statusMessage && <p className="font-mono">{migration.statusMessage}</p>}

        {items.length > 0 && (
          <ul className="font-mono space-y-1">
            {items.map((log) => {
              const rollback = rollbackNote(log);
              return (
                <li key={log.id} className="min-w-0">
                  <span className="break-all">{log.itemPath}</span>
                  {log.failedObjectName && (
                    <span className="break-all">
                      {' '}
                      · {log.failedObjectKind ?? 'object'} {log.failedObjectName}
                    </span>
                  )}
                  {log.errorMsg && <div className="break-all">{log.errorMsg}</div>}
                  {rollback && <div className="opacity-80">{rollback}</div>}
                </li>
              );
            })}
          </ul>
        )}

        {more > 0 && (
          <p>
            … and {more} more. The full log, with its filters and a retry, is on the Runs screen.
          </p>
        )}
      </div>
    </Note>
  );
}
