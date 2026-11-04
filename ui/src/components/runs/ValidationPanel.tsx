'use client';

// Validation is available only on a completed migration; anything else is a 400
// from the server. A "warning" is a reported fact, not a
// failure — filtered-out items land there.

import React from 'react';
import { useStartValidation, useValidation } from '@/queries/centipede';
import {
  Badge,
  Button,
  ErrorNote,
  Loading,
  MigrationDuration,
  Note,
  Radio,
} from '@/components/ui/primitives';
import { ValidationBadge } from '@/components/ui/status';
import type { MigrationSummary, ValidationDetailItem, ValidationMode } from '@/types/target';

/**
 * What each mode compares a file by. Filesystem items only — object storage and
 * databases are compared one way whatever is chosen, which is why the choice is
 * offered only when the plan has a filesystem item.
 */
const MODE_BASIS: Record<ValidationMode, string> = {
  quick: 'size and modification time',
  checksum: 'SHA256 of every migrated file',
};

/** What choosing a mode costs. */
const MODE_COST: Record<ValidationMode, string> = {
  quick: 'seconds, reads no file',
  checksum: 'reads all of it on both hosts',
};

/**
 * How many findings of each kind, in the order they first appear.
 *
 * Counted rather than looked up by name: a detail is a FINDING, and
 * cm-centipede's report writes one for a failure, a collapsed warning or a skip
 * and NOTHING AT ALL for an item that passed (pkg/core/validation/report.go). A
 * fixed "passed / failed / warning" row therefore printed "0 passed" next to a
 * green PASSED badge.
 */
function tally(details: ValidationDetailItem[]): Array<[status: string, n: number]> {
  const t = new Map<string, number>();
  for (const d of details) t.set(d.status, (t.get(d.status) ?? 0) + 1);
  return [...t];
}

/** A finding's tone. Nothing here is good news, so there is no 'ok'. */
const FINDING_TONE: Record<string, 'bad' | 'warn'> = {
  failed: 'bad',
  warning: 'warn',
};

/**
 * A "skipped" detail is not a finding about an item, and it is not drawn as one.
 *
 * The server files one per folder or bucket, carrying a COUNT — "12 file(s)
 * excluded by the migration filter" — under the folder's own path. In a table of
 * items it read as "/testdata: skipped", which says the folder was not
 * validated. The folder was: some files inside it were left out of the transfer
 * by the migration's own filter, so there was nothing to compare them against.
 *
 * It comes out of the table and the badge row, and is said as a sentence instead.
 */
function isSkip(d: ValidationDetailItem): boolean {
  return d.status === 'skipped';
}

export function ValidationPanel({
  migration,
  compact,
  startOnly,
}: {
  migration: MigrationSummary;
  compact?: boolean;
  /**
   * Draw nothing but the start button until a validation exists.
   *
   * For the wizard's Step 6, whose own title already reads "Validate": the
   * heading repeats it, the badge says "not_run" — which is the button being
   * enabled, said twice — and the spinner is a read of a result that has not
   * been asked for. One button is the whole of what the step offers, so it is
   * the whole of what it shows. The Runs screen passes nothing: there the
   * section sits among others and needs its name.
   */
  startOnly?: boolean;
}) {
  const completed = migration.status === 'completed';
  const start = useStartValidation();
  const [raw, setRaw] = React.useState(false);
  // Quick first, as the server defaults: a checksum of tens of gigabytes takes
  // minutes, and is something to ask for rather than wait through by accident.
  const [mode, setMode] = React.useState<ValidationMode>('quick');

  // The mode decides how a file system → file system entry is compared, and
  // nothing else. A cross-storage entry is compared by path and size whatever
  // is chosen, so a plan of nothing but those gets no choice to make. The
  // summary counts cross-storage entries of both directions together, which is
  // exact for a plan the wizard builds (one category) and errs towards showing
  // the choice for a mixed one built by hand.
  const plan = migration.planSummary;
  const crossStorage = plan?.crossStorage ?? 0;
  const allCross =
    crossStorage > 0 && crossStorage === (plan?.fileSystems ?? 0) + (plan?.objectStorages ?? 0);
  const modeApplies = (plan?.fileSystems ?? 0) > 0 && !allCross;

  // ── One reading of "is a validation running" ─────────────────────────────
  // This used to come from migration.validationStatus, and stuck on "running"
  // forever: starting a validation refetches the migration once, which lands
  // while the server has it at "running", and useMigration then stops polling
  // because the MIGRATION is completed. The record froze mid-validation, so the
  // button said "validating…" beside a badge already reading PASSED, and the
  // detail query polled for good.
  //
  // The validation endpoint is the one that answers about the validation, so it
  // is asked first and the frozen record is only the fallback before the first
  // read comes back.
  const query = useValidation(migration.id, start.isPending);
  const result = query.data;
  const status = result?.validationStatus ?? migration.validationStatus;
  const running = status === 'running' || start.isPending;

  const allDetails = result?.details ?? [];
  const details = allDetails.filter((d) => !isSkip(d));
  const skips = allDetails.filter(isSkip);
  const counts = tally(details);

  // Whether there is a report at all. "not_run" and a missing status are both
  // the absence of one.
  const ran = (status !== undefined && status !== 'not_run') || start.isPending;
  const showReport = !startOnly || ran;

  // While it runs, the message is the server's progress line. Read only from a
  // response that itself says "running": until the refetch after a re-validate
  // lands, the cached one still carries the previous run's verdict.
  const progressLine =
    result?.validationStatus === 'running' ? result.validationMessage : undefined;
  const startedAt = result?.startedAt ?? migration.validationStartedAt;

  return (
    <div>
      <div className="flex flex-wrap items-center gap-2 mb-2">
        {showReport && (
          <>
            <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
              Validation
            </h4>
            <div className="flex-1 h-px bg-border-main" />
            <ValidationBadge status={status} />
          </>
        )}
        <Button
          variant="primary"
          onClick={() => start.mutate({ id: migration.id, mode: modeApplies ? mode : 'quick' })}
          disabled={!completed || running}
          title={
            completed
              ? ran
                ? 'Validate this migration again'
                : 'Run validation on this migration'
              : 'Validation runs only on a completed migration'
          }
        >
          {running ? 'validating…' : ran ? 're-validate' : 'start validation'}
        </Button>
      </div>

      {/* Only where it changes something: a plan with no file system → file
          system item is validated the same way under either mode. */}
      {completed && modeApplies && (
        <div className="flex flex-wrap gap-x-6 gap-y-1 mb-2">
          {(['quick', 'checksum'] as ValidationMode[]).map((m) => (
            <Radio
              key={m}
              name={`validation-mode-${migration.id}`}
              value={m}
              checked={mode === m}
              onChange={(v) => setMode(v as ValidationMode)}
              label={m}
              hint={`${MODE_BASIS[m]} · ${MODE_COST[m]}`}
            />
          ))}
        </div>
      )}

      {/* Said before it runs, so a pass is not read as a content check. */}
      {completed && crossStorage > 0 && (
        <p className="text-xs text-text-muted mb-2">
          ⓘ cross-storage —{' '}
          {modeApplies
            ? 'those items are compared by path and size whichever mode is chosen'
            : 'compared by path and size'}
          ; content is not checked
        </p>
      )}

      {!completed && (
        <Note>
          Validation is enabled once the migration is <code>completed</code>. The server rejects it
          in any other state.
        </Note>
      )}

      <ErrorNote error={start.error} />
      <ErrorNote error={query.error} />

      {/* While it runs: where it has got to, and for how long. A checksum of
          tens of gigabytes says nothing else for minutes, and without these two
          it looked stopped. */}
      {running && (
        <p className="text-xs font-mono mt-2 flex flex-wrap gap-x-3 gap-y-1">
          <span className="text-text-muted">{progressLine ?? 'starting…'}</span>
          {startedAt && <MigrationDuration migration={{ status: 'running', startedAt }} />}
        </p>
      )}

      {/* The server's own sentence, and the only place a validation that passed
          is stated as a result: "all items validated successfully", with the
          warning count when there is one. Nothing below can say it, because a
          passing item leaves no finding to count. */}
      {!running && result?.validationMessage && (
        <p className="text-xs text-text-muted mt-2">
          {result.validationMessage}
          {/* Which comparison the verdict stands on — neither a quick pass nor
              a cross-storage one vouches for content. */}
          {modeApplies && result.mode && (
            <span className="font-mono"> · filesystem {result.mode}: {MODE_BASIS[result.mode]}</span>
          )}
          {crossStorage > 0 && (
            <span className="font-mono"> · cross-storage: path and size (content not checked)</span>
          )}
        </p>
      )}

      {/* What the validation did not look at, and why — beside the message
          rather than in the table, because the subject is the migration's own
          filter and not any item's state. The server's count is printed as it
          comes; the clause after it is what the count means. */}
      {skips.length > 0 && (
        <ul className="text-xs text-text-muted mt-2 space-y-0.5">
          {skips.map((d, i) => (
            <li key={`${d.itemPath}-${i}`}>
              {d.itemPath && <span className="font-mono">{d.itemPath} · </span>}
              {d.message}
              {/* Only for the filter count, which carries the folder's path. A
                  skip without one (cross-storage, before the server compared
                  it) already says what it is. */}
              {d.itemPath && ' — the filter kept them out of the transfer, so there was nothing to compare them against'}
            </li>
          ))}
        </ul>
      )}

      {details.length > 0 && (
        <>
          <div className="flex flex-wrap gap-2 mt-3">
            {counts.map(([findingStatus, n]) => (
              <Badge key={findingStatus} tone={FINDING_TONE[findingStatus] ?? 'info'}>
                {n} {findingStatus}
              </Badge>
            ))}
          </div>

          {!compact && (
            <div className="overflow-x-auto mt-3">
              <table className="w-full text-xs font-mono">
                <thead>
                  <tr className="text-2xs uppercase tracking-wider text-text-muted border-b border-border-main">
                    <th className="text-left font-bold px-2 py-1.5">item</th>
                    <th className="text-left font-bold px-2 py-1.5">status</th>
                    <th className="text-left font-bold px-2 py-1.5">message</th>
                  </tr>
                </thead>
                <tbody>
                  {details.slice(0, 50).map((d, i) => (
                    <tr key={`${d.itemPath}-${i}`} className="border-b border-border-main/60">
                      <td className="px-2 py-1.5 max-w-[22rem] truncate" title={d.itemPath}>
                        {d.itemPath}
                      </td>
                      <td className="px-2 py-1.5">
                        <Badge
                          tone={
                            d.status === 'passed' ? 'ok' : d.status === 'failed' ? 'bad' : 'warn'
                          }
                        >
                          {d.status}
                        </Badge>
                      </td>
                      <td className="px-2 py-1.5 text-text-muted">{d.message ?? '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Said only when there is a warning to say it about. */}
          {counts.some(([s]) => s === 'warning') && (
            <p className="text-xs text-text-muted mt-2">
              a warning is reported but does not fail the validation
            </p>
          )}
        </>
      )}

      {/* GET /migration/{id}/validation as it arrived.

          Everything above is a reading of this document: the table caps at 50
          rows, the report itself keeps at most 20 findings per folder and
          collapses its warnings, and the skip lines are reworded. This is where
          the response can be read whole, and compared against what the screen
          made of it.

          Verbatim, not masked. A plan is built against a destination and brings
          its credentials back, which is why Step 4 masks; a validation result is
          a status, a sentence and per-item findings, and carries no credential
          to hide. */}
      {result && !compact && (
        <div className="mt-3">
          <Button onClick={() => setRaw((r) => !r)}>
            {raw ? 'hide raw JSON' : 'view raw JSON'}
          </Button>
          {raw && (
            <pre className="mt-2 max-h-96 overflow-auto rounded-lg bg-bg-input border border-border-input p-3 text-2xs leading-relaxed">
              {JSON.stringify(result, null, 2)}
            </pre>
          )}
        </div>
      )}

      {/* Only once there is a validation to load. Before one is asked for, this
          spinner reported the reading of a result that does not exist. */}
      {query.isPending && completed && showReport && <Loading label="loading validation…" />}
    </div>
  );
}
