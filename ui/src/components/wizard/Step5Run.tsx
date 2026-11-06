'use client';

import React from 'react';
import {
  Badge,
  Button,
  ErrorNote,
  Field,
  MigrationDuration,
  Note,
  ProgressBar,
  Radio,
  TextInput,
} from '@/components/ui/primitives';
import { StatusBadge } from '@/components/ui/status';
import { RunFailures } from './RunFailures';
import { useCancelMigration, useMigration, useMigrationLogs } from '@/queries/centipede';
import { runners } from '@/lib/runner';
import { isNameTaken, suggestName, translateCreateError } from '@/lib/migrationName';
import { useWizardStore } from '@/store/wizardStore';
import {
  concurrencyLine,
  currentObjectLabel,
  dbStepFigures,
  formatBytes,
  formatCount,
  transferStepLabel,
} from '@/lib/format';
import { ApiError } from '@/api/http';
import type { Category } from '@/lib/connectionCategory';
import type { DBMSOnFailure, MigrationLog } from '@/types/target';

/** The server refuses anything above it (datamig.MaxConcurrency). */
const MAX_CONCURRENCY = 32;

/**
 * What the figure means per category, and where it does not reach. Fixed text:
 * whether a database item goes over an SSH tunnel is settled on the server from
 * the connection, so this step cannot tell item by item — the log row says
 * what each item actually ran at.
 */
const CONCURRENCY_NOTE: Record<Category, string> = {
  objectstorage: 'Objects and parts sent at once. Small objects: 16–32.',
  filesystem: 'rsync processes, up to 8. Few files or dry run: single rsync.',
  database: 'Restore connections, up to 16. Not applied to MySQL·PostgreSQL over SSH tunnel.',
};

/**
 * What the finished items ran at, as their log rows say: one figure when they
 * agree ("3"), a range when they do not ("2–8"), the reasons a figure fell
 * short of the request, and — when there are none and the items agree — what
 * the figure counts ("3 objects at once"). Null before any item has reported.
 */
function appliedConcurrency(
  logs: MigrationLog[],
  category: Category,
): { figure: string; notes: string[]; line: string | null } | null {
  const values = logs.map((l) => l.appliedConcurrency ?? 0).filter((v) => v > 0);
  if (values.length === 0) return null;
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const notes = [...new Set(logs.map((l) => l.concurrencyNote).filter((n): n is string => !!n))];
  return {
    figure: lo === hi ? String(lo) : `${lo}–${hi}`,
    notes,
    line: lo === hi ? concurrencyLine(lo, undefined, category) : null,
  };
}

/** Why `n` cannot be sent, or null when it can (null itself: not set). */
function concurrencyProblem(n: number | null): string | null {
  if (n === null) return null;
  return Number.isInteger(n) && n >= 1 && n <= MAX_CONCURRENCY
    ? null
    : `concurrency must be a whole number from 1 to ${MAX_CONCURRENCY}, or empty for the server default.`;
}

export function Step5Run({ category, onDone }: { category: Category; onDone: () => void }) {
  const state = useWizardStore((s) => s.byCategory[category]);
  const patch = useWizardStore((s) => s.patch);
  const cancel = useCancelMigration();

  const [starting, setStarting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [nameWarning, setNameWarning] = React.useState<string | null>(null);

  // A timestamp suffix makes a repeat run unique without the user thinking
  // about it.
  React.useEffect(() => {
    if (!state.runName) patch(category, { runName: suggestName(category) });
  }, [category, state.runName, patch]);

  const migration = useMigration(state.migrationId, !!state.migrationId);
  const engine = runners.find((r) => r.id === state.engine) ?? runners[0];
  const showEngineChooser = runners.length > 1;

  async function start() {
    if (!state.plan) return;
    setStarting(true);
    setError(null);
    setNameWarning(null);
    try {
      // Pre-check against the whole history. lastDays: 0 is what makes it the
      // whole history — the endpoint otherwise answers for the last 7 days only.
      if (await isNameTaken(state.runName)) {
        setNameWarning('That name is already used by another migration. Pick another one.');
        return;
      }

      const handle = await engine.start({
        name: state.runName.trim(),
        description: state.runDescription.trim() || undefined,
        plan: state.plan,
        dbmsOnFailure: state.dbmsOnFailure,
        concurrency: state.concurrency ?? undefined,
        category,
        targetModelId: state.targetModelId ?? undefined,
      });

      const id = await engine.resolveMigrationId(handle);
      // The step does NOT leave for Step 6 here. Starting a migration is the
      // moment its progress, its failures and its cancel button matter, and all
      // three live on this screen — advancing collapsed them under a step that
      // cannot do anything until the run is finished anyway. The button at the
      // bottom goes there when the reader is ready.
      patch(category, { migrationId: id });
    } catch (e) {
      const raw = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
      setError(translateCreateError(raw));
    } finally {
      setStarting(false);
    }
  }

  const concurrencyError = concurrencyProblem(state.concurrency);
  const m = migration.data;
  const running = m?.status === 'running' || m?.status === 'pending';
  // What each item actually ran at is on its log row, not on the migration.
  // Asked for once an item has finished — before that there is no row — and
  // as the Runs page's log table asks (page 1 of 20), so the two share it.
  const logs = useMigrationLogs(
    m && m.processedItems > 0 ? m.id : null,
    { page: 1, pageSize: 20 },
    running,
  );
  const applied = appliedConcurrency(logs.data?.items ?? [], category);
  const stepLabel = transferStepLabel(
    m?.currentStep,
    m?.currentSteps,
    m?.currentStepName,
    m?.currentStepDetail,
  );
  // What the in-flight bytes count towards — the dump a restore reads. Only for
  // a single item: transferredBytes is the run's total, which past the first
  // item also holds the bytes of items already finished.
  const bytesOf =
    m && m.totalItems <= 1 && m.currentBytesTotal ? m.currentBytesTotal : undefined;
  const dbs = state.plan?.targetDataMigrationModel.databases ?? [];
  const objectLabel = currentObjectLabel(
    category,
    dbs.length > 0 && dbs.every((d) => d.dbType === 'mongodb'),
  );

  return (
    <div className="space-y-4">
      {showEngineChooser && (
        <Field label="execution engine">
          <div className="flex flex-wrap gap-6 pt-1">
            {runners.map((r) => (
              <Radio
                key={r.id}
                name={`engine-${category}`}
                value={r.id}
                checked={state.engine === r.id}
                onChange={(v) => patch(category, { engine: v as typeof state.engine })}
                label={r.label}
                hint={
                  r.id === 'cicada' && !state.targetModelId
                    ? 'blocked — save the plan as a target model in Step 4 first'
                    : undefined
                }
              />
            ))}
          </div>
        </Field>
      )}

      <div className="grid md:grid-cols-2 gap-3">
        <Field label="name" required hint="unique across ALL migrations">
          <TextInput
            value={state.runName}
            onChange={(e) => {
              patch(category, { runName: e.target.value });
              setNameWarning(null);
            }}
          />
        </Field>
        <Field label="description">
          <TextInput
            value={state.runDescription}
            onChange={(e) => patch(category, { runDescription: e.target.value })}
          />
        </Field>
      </div>

      {nameWarning && <Note tone="warn">{nameWarning}</Note>}

      {/* No figure for the default: the server's configuration decides it, and
          this step cannot read that. The log row says what each item ran at. */}
      <Field label="concurrency" hint={`1–${MAX_CONCURRENCY}, empty = server default`}>
        {/* Plain text rather than type="number", which brings spin buttons:
            digits only, anything else dropped as it is typed. */}
        <TextInput
          inputMode="numeric"
          pattern="[0-9]*"
          value={state.concurrency ?? ''}
          onChange={(e) => {
            const digits = e.target.value.replace(/\D/g, '');
            patch(category, { concurrency: digits === '' ? null : Number(digits) });
          }}
          className="max-w-[8rem]"
        />
        <span className="block mt-1 text-xs text-text-muted">{CONCURRENCY_NOTE[category]}</span>
      </Field>
      {concurrencyError && <Note tone="warn">{concurrencyError}</Note>}

      {category === 'database' && (
        <Field label="dbmsOnFailure" hint="database only">
          <div className="flex gap-6 pt-1">
            {(['cleanup', 'keep'] as DBMSOnFailure[]).map((v) => (
              <Radio
                key={v}
                name={`on-failure-${category}`}
                value={v}
                checked={state.dbmsOnFailure === v}
                onChange={(x) => patch(category, { dbmsOnFailure: x as DBMSOnFailure })}
                label={v}
                hint={
                  v === 'cleanup'
                    ? 'default — undoes a failed database item'
                    : 'leaves a failed database in place'
                }
              />
            ))}
          </div>
        </Field>
      )}

      {error && <Note tone="bad">{error}</Note>}

      <Button
        variant="primary"
        onClick={start}
        disabled={
          starting ||
          !state.plan ||
          !state.runName.trim() ||
          !!state.migrationId ||
          !!concurrencyError
        }
      >
        {starting ? 'starting…' : 'start migration'}
      </Button>

      {state.migrationId && (
        <div className="border-t border-border-main pt-4 space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-xs font-mono">
            <Badge tone="info">engine {engine.id}</Badge>
            <span className="text-text-muted">migrationId</span>
            <span>{state.migrationId}</span>
            {m && <StatusBadge status={m.status} />}
          </div>

          {m && (
            <>
              {/* One row of figures, each reading VALUE then unit, the way the
                  Runs cards read. These were label-left / value-right pairs at
                  the smallest step with both halves muted, which put every
                  number hard against the panel's right edge, a long way from
                  the word naming it, and gave the figures no more weight than
                  the labels. The numbers are what this step is watched for. */}
              <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs font-mono">
                {/* "migrated items", not the domain's unit word: "0 / 1 folders"
                    read as a count of folders found rather than of folder copies
                    done. "items" is what the server calls these (processedItems,
                    ItemPath), and "migrated" says the number is progress. */}
                <span>
                  {formatCount(m.processedItems)} / {formatCount(m.totalItems)}
                  <span className="text-text-muted"> migrated items</span>
                </span>
                {/* A counter, not a bar: there is no total to divide by. Absent
                    while it is zero, which is what it reads before the first
                    item finishes on a domain that reports per item — except in
                    a relay, whose count restarts at zero with each step: there
                    the step name stays and says why the figure fell back. */}
                {(m.transferredBytes > 0 || stepLabel) && (
                  <span>
                    {formatBytes(m.transferredBytes)}
                    {bytesOf !== undefined && (
                      <span className="text-text-muted"> / {formatBytes(bytesOf)}</span>
                    )}
                    <span className="text-text-muted"> transferred</span>
                    {stepLabel && <span className="text-text-muted"> · {stepLabel}</span>}
                  </span>
                )}
                {/* A database's step in tables and rows, where the server could
                    count them. */}
                {dbStepFigures(m).map((f) => (
                  <span key={f}>{f}</span>
                ))}
                {/* Whole pair muted at zero and whole pair rose above it: a
                    failure count is not a reading of the same kind as bytes
                    transferred, and at zero it is not news at all. */}
                <span
                  className={
                    m.failedItems > 0 ? 'text-rose-600 dark:text-rose-400' : 'text-text-muted'
                  }
                >
                  {formatCount(m.failedItems)} failed
                </span>
                <MigrationDuration migration={m} />
                {applied && (
                  <span>
                    {applied.figure}
                    <span className="text-text-muted"> concurrency</span>
                  </span>
                )}
              </div>
              {/* Why the figure is not what was asked — a cap, a single rsync
                  for a handful of files, a restore CLI that runs one stream. */}
              {applied && (applied.notes.length > 0 || applied.line) && (
                <div className="text-xs text-text-muted space-y-0.5">
                  {applied.notes.length > 0 ? (
                    applied.notes.map((n) => <div key={n}>concurrency &nbsp;{n}</div>)
                  ) : (
                    <div>concurrency &nbsp;{applied.line}</div>
                  )}
                </div>
              )}

              {/* A bar needs more than one step to be a bar. A filesystem or
                  object storage plan holds exactly one folder or bucket, so its
                  bar could only ever read 0% or 100% — which the status badge
                  above already says, and says better. */}
              {m.totalItems > 1 && <ProgressBar value={m.processedItems} total={m.totalItems} />}
              {/* The one bar a single item does get: a restore reading a dump of
                  known size, where the bytes are a real fraction. */}
              {bytesOf !== undefined && <ProgressBar value={m.transferredBytes} total={bytesOf} />}

              {/* One folder can take an hour, and a count that reads 0 / 1 the
                  whole time says nothing about whether anything is happening.
                  This is the line that moves, so the path is the contrasted half
                  and the word is not. The server clears it when the item
                  finishes, so it is absent on a run that is over. */}
              {m.currentObject && (
                <div className="flex gap-2 text-xs font-mono min-w-0">
                  <span className="shrink-0 text-text-muted">{objectLabel}</span>
                  <span className="truncate" title={m.currentObject}>
                    {m.currentObject}
                  </span>
                </div>
              )}

              {/* Only while the run is still its own news. Once it has failed the
                  message is the failure's, and RunFailures says it there rather
                  than twice. */}
              {m.statusMessage && m.status !== 'failed' && (
                <p className="text-xs font-mono text-text-muted">{m.statusMessage}</p>
              )}

              <RunFailures migration={m} running={running} />

              {running && (
                <Button variant="danger" onClick={() => cancel.mutate(state.migrationId!)}>
                  cancel
                </Button>
              )}
            </>
          )}

          <ErrorNote error={cancel.error} />
          <ErrorNote error={migration.error} />

          {/* Shown for any started run, finished or not, so a failed or
              cancelled one is not a dead end: Step 6 says for itself what it can
              do with what it finds. Urged only once nothing is moving here —
              while the run goes, watching it is the thing to do. */}
          <div className="flex pt-1">
            <Button
              variant={running ? 'ghost' : 'primary'}
              className="ml-auto"
              onClick={onDone}
              title={
                running
                  ? 'Validation runs on a completed migration — the step opens either way'
                  : 'Open Step 6 and validate this migration'
              }
            >
              next: validate →
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
