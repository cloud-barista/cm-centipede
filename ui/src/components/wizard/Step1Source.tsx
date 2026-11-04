'use client';

// Picks a cm-honeybee source group — all of its connections, or one of them —
// runs the inspect and reads the scan back. srcName is taken out of the refined
// response and never typed by a person: object storage normalizes a root to
// "bucket/" with a trailing slash, and a mismatch makes the plan fail quietly.

import React from 'react';
import {
  Button,
  Checkbox,
  ErrorNote,
  Field,
  Loading,
  Note,
  Select,
  TextInput,
} from '@/components/ui/primitives';
import { HealthBadge } from '@/components/ui/status';
import {
  useConnections,
  useRefinedEach,
  useRefinedScope,
  useRunImports,
  useSourceGroups,
} from '@/queries/honeybee';
import { ADAPTERS, type SourceEntry, type SourceUnit } from '@/components/domains/adapters';
import {
  agentApplies,
  categoryOfGroup,
  connectionAddress,
  isSupportedGroup,
  scanTargetOf,
  type Category,
} from '@/lib/connectionCategory';
import { isStaleScanError, scanErrorMessage } from '@/api/honeybee';
import { InspectResult, ViewSwitch } from './InspectResult';
import type { UnitGroup } from './UnitTree';
import { useWizardStore } from '@/store/wizardStore';
import { useUiStore } from '@/store/uiStore';
import type { ConnectionInfo } from '@/types/honeybee';

// Kept stable per entry: the tree rebuilds a connection's folders whenever the
// array it is handed changes. Module-level rather than a ref, so render never
// touches ref.current; an entry belongs to one category's query, so one map
// serves every adapter.
const UNITS = new WeakMap<SourceEntry, SourceUnit[]>();

/**
 * "conn ok 4 / failed 1" — the exceptions only where there are any.
 *
 * Counted per connection rather than read off the group listing's counts: the
 * listing calls an agent that does not apply a failed one (see StatusRollup),
 * which `agentApplies` can tell apart and the listing cannot.
 */
function tally(label: string, statuses: Array<string | undefined>): string | null {
  if (statuses.length === 0) return null;
  const ok = statuses.filter((s) => s === 'success').length;
  const failed = statuses.filter((s) => s === 'failed').length;
  const pending = statuses.length - ok - failed;
  return [
    `${label} ok ${ok}`,
    failed > 0 ? `failed ${failed}` : null,
    pending > 0 ? `pending ${pending}` : null,
  ]
    .filter(Boolean)
    .join(' / ');
}

export function Step1Source({ category, onDone }: { category: Category; onDone: () => void }) {
  const adapter = ADAPTERS[category];
  const state = useWizardStore((s) => s.byCategory[category]);
  const patch = useWizardStore((s) => s.patch);
  const edit = useWizardStore((s) => s.edit);
  const setActiveTab = useUiStore((s) => s.setActiveTab);
  const resultView = useUiStore((s) => s.inspectResultView);
  const setResultView = useUiStore((s) => s.setInspectResultView);

  const [sgId, setSgId] = React.useState(state.source?.sgId ?? '');
  // '' is the whole group — the select's first option, and its default.
  const [connId, setConnId] = React.useState(state.source?.connId ?? '');
  const [params, setParams] = React.useState(adapter.defaultInspect);

  const groups = useSourceGroups();
  // Only groups of this category's type are offered — and never a csp group.
  const usable = (groups.data?.source_group ?? []).filter(
    (g) => isSupportedGroup(g) && categoryOfGroup(g) === category,
  );
  const conns = useConnections(sgId, !!sgId);
  const allConns = React.useMemo(() => conns.data?.connection_info ?? [], [conns.data]);
  /** The connection picked, when one is. */
  const connection = connId ? allConns.find((c) => c.id === connId) : undefined;
  /** Every connection this step has to scan. */
  const scope = React.useMemo(
    () => (connId ? allConns.filter((c) => c.id === connId) : allConns),
    [allConns, connId],
  );
  const groupMode = !connId;

  // The group's refined response, or the one connection's. Both are the
  // document Step 4 hands to the plan call.
  const refined = useRefinedScope(category, sgId || null, connId || null, !!sgId);
  const imports = useRunImports();

  const byConn = React.useMemo(() => {
    const m = new Map<string, SourceEntry>();
    for (const e of adapter.entriesOf(refined.data)) m.set(e.connId, e.entry);
    return m;
  }, [adapter, refined.data]);

  const unitsOf = (entry: SourceEntry | undefined) => {
    if (!entry) return [];
    let units = UNITS.get(entry);
    if (!units) {
      units = adapter.unitsOf(entry);
      UNITS.set(entry, units);
    }
    return units;
  };

  // The group read leaves a connection out without saying why, so each one it
  // left out is asked on its own: not scanned, or stale. Only once the group
  // read has answered — before that every connection would look missing.
  // A single connection's read already is that connection's own read, under the
  // same query key: asking it again would mount an observer that refetches the
  // errored query, flip refined.isLoading, unmount itself, and loop forever.
  const missing = React.useMemo(
    () =>
      !groupMode || refined.isLoading
        ? []
        : scope.filter((c) => !byConn.has(c.id)).map((c) => c.id),
    [groupMode, refined.isLoading, scope, byConn],
  );
  const missingReads = useRefinedEach(category, sgId || null, missing, !!sgId);
  const readOf = (id: string) => {
    if (!groupMode) return refined;
    const i = missing.indexOf(id);
    return i < 0 ? undefined : missingReads[i];
  };

  // What the inspect is going to read. Not a field on this form: cm-honeybee
  // keeps the scan target on the connection, and the import request says only
  // how detailed the collection should be.
  const scanTarget = connection ? scanTargetOf(connection, category) : null;

  // The engine the metric list is drawn for. A group can mix engines; then
  // every engine's metrics are offered, each marked with the engines that have
  // it, and each connection's request still carries only what its own engine
  // knows (buildImportReq). Offering only the common ones left a mixed group
  // no way to ask a PostgreSQL host for its sequences or a MySQL one for its
  // events.
  const engines = [...new Set(scope.map((c) => c.db_type).filter(Boolean))] as string[];
  const dbType = engines.length === 1 ? engines[0] : undefined;
  const metricRows: Array<{ key: string; only?: string }> =
    engines.length > 1
      ? [...new Set(engines.flatMap((e) => adapter.metricKeys(e)))].map((key) => {
          const having = engines.filter((e) => adapter.metricKeys(e).includes(key));
          return { key, only: having.length < engines.length ? having.join(', ') : undefined };
        })
      : adapter.metricKeys(dbType).map((key) => ({ key }));

  /**
   * Why this metric list and not a longer one.
   *
   * Half of cm-honeybee's database metrics are engine-specific, so the list is
   * short until a connection names an engine. Without this the reader sees four
   * checkboxes, no reason for there being four, and no sign that picking a
   * connection changes it.
   */
  const metricHint =
    category !== 'database'
      ? 'each metric costs a full walk of the source'
      : dbType
        ? `${dbType} · each metric costs a full walk of the source`
        : engines.length > 1
          ? `${engines.join(', ')} \u00b7 a metric marked with engines is sent only to those connections`
          : 'common to every engine — pick a source group above for its engine’s full list';

  /** Whether a connection's scan can be used, and what to do when it cannot. */
  const rowStatus = (c: ConnectionInfo): Pick<UnitGroup, 'status' | 'action'> & { usable: boolean } => {
    const run = imports.runs[c.id];
    const inspectOne = (label: string) => (
      <Button variant="ghost" disabled={imports.running} onClick={() => inspect([c])}>
        {label}
      </Button>
    );

    if (run?.state === 'running') {
      return { usable: false, status: { tone: 'neutral', text: '⟳ inspecting…' } };
    }
    if (run?.state === 'failed') {
      return {
        usable: false,
        status: { tone: 'bad', text: `✗ ${scanErrorMessage(run.error)}` },
        action: inspectOne('retry'),
      };
    }
    if (byConn.has(c.id)) {
      // cm-honeybee keeps exactly one scan per connection and hands it back on
      // read, so a connection inspected at any point in the past shows a result
      // without anyone having pressed the button — which, unmarked, reads as the
      // output of a run that never happened. How OLD it is cannot be shown:
      // SavedFSInfo carries a SavedTime but the refined model does not.
      return run?.state === 'done'
        ? { usable: true, status: { tone: 'ok', text: '✓', title: 'Inspected just now.' } }
        : {
            usable: true,
            status: {
              tone: 'warn',
              text: '⚠ stored, not run now',
              title:
                'Already stored for this connection, not run just now. cm-honeybee keeps one scan per connection and reports no date for it — run the inspect if the source may have changed since.',
            },
          };
    }
    const read = readOf(c.id);
    // A finished import is read back by the refetch it triggers; until that
    // lands the row has not lost its scan, it is waiting for it.
    const awaitingReadBack = run?.state === 'done' && (refined.isFetching || !!read?.isFetching);
    if (refined.isLoading || !read || read.isLoading || awaitingReadBack) {
      return { usable: false, status: { tone: 'neutral', text: 'reading scan…' } };
    }
    if (isStaleScanError(read.error)) {
      // Not "no scan yet": there is one, held back because the connection's
      // scan target moved under it. Running the inspect is still the fix, but
      // for a different reason.
      return {
        usable: false,
        status: {
          tone: 'warn',
          text: '⚠ stale — scan target changed',
          title: scanErrorMessage(read.error),
        },
        action: inspectOne('inspect'),
      };
    }
    return {
      usable: false,
      status: { tone: 'neutral', text: '— not scanned' },
      action: inspectOne('inspect'),
    };
  };

  const rows = scope.map((c) => ({ conn: c, ...rowStatus(c) }));
  const scannedCount = rows.filter((r) => r.usable).length;
  const scannedAll = scope.length > 0 && scannedCount === scope.length;
  const unscanned = rows.filter((r) => !r.usable).map((r) => r.conn.name);

  const srcName = connection ? adapter.srcNameOf(byConn.get(connection.id)) : '';

  /**
   * The result tree, one connection row each.
   *
   * Whether the scan was run here or was already stored is said on the row
   * itself: with several connections in one tree a single note under it could
   * not say which of them it meant.
   */
  const resultGroups: UnitGroup[] = rows.map(({ conn, status, action }) => {
    const entry = byConn.get(conn.id);
    return {
      connId: conn.id,
      label: conn.name,
      meta: `${connectionAddress(conn, category)} · ${
        entry ? adapter.srcNameOf(entry) || '(none)' : (scanTargetOf(conn, category) ?? '—')
      }`,
      detail: adapter.totalsOf(entry),
      status,
      action,
      units: unitsOf(entry),
    };
  });

  // Keep the wizard's own record of the selection in step with the scan.
  //
  // patch, not edit: this mirrors what the query returned and runs on every
  // refetch, so cascading from here would let a background refresh discard the
  // plan. The controls below are where a person actually changes the source,
  // and they cascade for themselves.
  const scopeIds = scope.map((c) => c.id).join('\0');
  const groupName = usable.find((g) => g.id === sgId)?.name ?? sgId;
  const label = connection
    ? `${srcName || '(scan root)'} \u00b7 ${connection.name}`
    : `${groupName} \u00b7 ${scope.length} connection${scope.length === 1 ? '' : 's'}`;
  React.useEffect(() => {
    if (sgId) {
      patch(category, {
        source: {
          sgId,
          connId: connId || null,
          connIds: scopeIds ? scopeIds.split('\0') : [],
          label,
          srcName,
          inspected: scannedAll,
        },
      });
    }
  }, [category, sgId, connId, scopeIds, label, srcName, scannedAll, patch]);

  /**
   * Point the wizard at a different scan, and leave nothing of the last one
   * behind.
   *
   * The result itself needs no clearing — the refined reads are keyed by
   * (category, sgId, connId), so a different selection is a different query
   * and an unvisited one has no data. What does survive is everything around it:
   *
   *  - the imports. Their outcomes are drawn on the result rows, so a failure
   *    against the previous selection stayed on screen under the new one, and a
   *    run still in flight would mark a row it was never started on.
   *  - the inspect parameters. Half of cm-honeybee's database metrics are
   *    engine-specific, so a selection made for MySQL is not a selection for
   *    PostgreSQL; max_depth is likewise chosen for the hosts that were picked.
   *
   * `source: null` rather than letting the effect catch up, because a
   * half-made selection would otherwise leave the old one in place, still
   * marked inspected. `routes` goes too: they are keyed by connections of the
   * old selection. A re-inspect keeps them, which is why downstreamOf does not
   * clear them.
   */
  const changeSource = (apply: () => void) => {
    apply();
    imports.reset();
    setParams(adapter.defaultInspect);
    edit(category, 1, { source: null, routes: {} });
  };

  /**
   * Inspects `targets`, one import per connection, a few at a time. A re-scan
   * replaces the units the mapping in Step 3 names, so it invalidates the same
   * things a different selection does.
   */
  function inspect(targets: ConnectionInfo[]) {
    if (!sgId || targets.length === 0) return;
    edit(category, 1);
    void imports.run(
      category,
      sgId,
      targets.map((c) => ({ connId: c.id, req: adapter.buildImportReq(params, c.db_type) })),
    );
  }

  // Inspect failures are drawn on their rows; the button shows only progress.
  const failedCount = rows.filter((r) => imports.runs[r.conn.id]?.state === 'failed').length;

  return (
    <div className="space-y-5">
      <section className="space-y-3">
        <div className="flex items-center gap-2">
          <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
            Source connection
          </h4>
          <div className="flex-1 h-px bg-border-main" />
        </div>

        <ErrorNote error={groups.error} />

        <div className="grid md:grid-cols-2 gap-3">
          <Field label="source_group" required>
            <Select
              value={sgId}
              onChange={(e) =>
                changeSource(() => {
                  setSgId(e.target.value);
                  setConnId('');
                })
              }
            >
              <option value="">select…</option>
              {usable.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name} {g.provider_name ? `· ${g.provider_name}` : ''}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="connection">
            <Select
              value={connId}
              onChange={(e) => changeSource(() => setConnId(e.target.value))}
              disabled={!sgId || conns.isPending}
            >
              <option value="">(all connections · {allConns.length})</option>
              {allConns.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </Field>
        </div>

        <ErrorNote error={conns.error} />

        {usable.length === 0 && !groups.isPending && (
          <Note>
            No source group of this category yet.{' '}
            <button
              type="button"
              onClick={() => setActiveTab('connections')}
              className="underline cursor-pointer"
            >
              Create one in Connections
            </button>
            .
          </Note>
        )}

        {connection ? (
          <div className="flex flex-wrap items-center gap-2">
            <HealthBadge label="conn" status={connection.connection_status} />
            {agentApplies(connection, category) && (
              <HealthBadge label="agent" status={connection.agent_status} />
            )}
          </div>
        ) : (
          sgId &&
          conns.isSuccess && (
            // A badge pair per connection would be a wall of them; one line
            // says how many there are and which checks have not passed.
            <p className="text-xs font-mono text-text-muted">
              {[
                `${allConns.length} connection${allConns.length === 1 ? '' : 's'}`,
                tally(
                  'conn',
                  allConns.map((c) => c.connection_status),
                ),
                tally(
                  'agent',
                  allConns.filter((c) => agentApplies(c, category)).map((c) => c.agent_status),
                ),
              ]
                .filter(Boolean)
                .join(' · ')}
            </p>
          )
        )}
      </section>

      <section className="space-y-3 border-t border-border-main pt-4">
        <div className="flex items-center gap-2">
          <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
            Inspect
          </h4>
          <div className="flex-1 h-px bg-border-main" />
        </div>

        {/* Read-only, and the same sentence for all three categories: the
            target belongs to the connection in every one of them. Offering to
            edit it here would mean this step writing to a connection the
            Connections screen owns, and would mark the scan already collected
            under the old target stale. */}
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
          <span className="text-text-muted">scan target</span>
          {groupMode ? (
            <span className="text-text-muted">
              {sgId
                ? 'set on each connection — shown on its row in the result below'
                : 'select a source group above'}
            </span>
          ) : (
            <>
              <span className="font-mono font-bold">{scanTarget ?? '—'}</span>
              <span className="text-text-muted">set on the connection</span>
            </>
          )}
        </div>

        {category === 'filesystem' && (
          <div className="grid md:grid-cols-3 gap-3">
            <Field label="max_depth" hint="0 = whole tree">
              <TextInput
                type="number"
                value={params.maxDepth}
                onChange={(e) => setParams((p) => ({ ...p, maxDepth: Number(e.target.value) }))}
              />
            </Field>
          </div>
        )}

        <Field label="metrics" hint={metricHint}>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-2 pt-1">
            {metricRows.map(({ key, only }) => (
              <Checkbox
                key={key}
                checked={!!params.metrics[key]}
                onChange={(v) =>
                  setParams((p) => ({ ...p, metrics: { ...p.metrics, [key]: v } }))
                }
                label={key}
                hint={only}
              />
            ))}
          </div>
        </Field>

        {/* Every metric starts on, so this is guidance rather than an alarm:
            it names the expensive three and where the cost lands, and leaves
            the judgement to the reader. */}
        {category === 'database' &&
          (params.metrics.columns || params.metrics.indexes || params.metrics.row_count_exact) && (
            <Note>
              <code>columns</code> and <code>indexes</code> can each make the response several
              megabytes, and <code>row_count_exact</code> counts every row of every table. Turn
              them off for a quick look at a large server.
            </Note>
          )}

        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            disabled={!sgId || scope.length === 0 || imports.running}
            onClick={() => inspect(scope)}
          >
            {imports.running
              ? 'inspecting…'
              : groupMode
                ? `run inspect (${scope.length} connection${scope.length === 1 ? '' : 's'})`
                : 'run inspect'}
          </Button>
          {failedCount > 0 && !imports.running && (
            <span className="text-xs text-rose-600 dark:text-rose-400">
              {failedCount} failed — see the rows below
            </span>
          )}
        </div>
      </section>

      <section className="space-y-3 border-t border-border-main pt-4">
        <div className="flex items-center gap-2">
          <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
            Result
          </h4>
          <div className="flex-1 h-px bg-border-main" />
          {/* Only once there is a result to read one way or the other. */}
          {byConn.size > 0 && (
            <ViewSwitch
              view={resultView}
              unitLabel={adapter.unitLabel}
              onChange={setResultView}
            />
          )}
        </div>

        {!sgId ? (
          <p className="text-xs text-text-muted">Select a source group above.</p>
        ) : conns.isSuccess && allConns.length === 0 ? (
          <p className="text-xs text-text-muted">This source group has no connection yet.</p>
        ) : refined.isLoading || conns.isPending ? (
          <Loading label="reading scan…" />
        ) : (
          <>
            {scope.length > 1 && (
              <p className="text-xs">
                <span className="font-mono font-bold">
                  {scannedCount} / {scope.length}
                </span>{' '}
                <span className="text-text-muted">
                  scanned
                  {!scannedAll && ' — every connection has to be scanned to continue'}
                </span>
              </p>
            )}

            <InspectResult
              view={resultView}
              groups={resultGroups}
              unitLabel={adapter.unitLabel}
              model={refined.data}
            />

            {/* A connection that cannot be scanned keeps the whole group from
                continuing. The two ways past it are named, so the reader is
                not left retrying a host that is down. */}
            {groupMode && !scannedAll && !imports.running && scannedCount > 0 && (
              <Note>
                Not scanned: {unscanned.join(', ')}. If one of them cannot be scanned, pick the
                others one at a time in the connection select above, or remove it from the group
                in{' '}
                <button
                  type="button"
                  onClick={() => setActiveTab('connections')}
                  className="underline cursor-pointer"
                >
                  Connections
                </button>
                .
              </Note>
            )}

            <div className="flex justify-end">
              <Button variant="primary" disabled={!scannedAll} onClick={onDone}>
                next: target →
              </Button>
            </div>
          </>
        )}
      </section>
    </div>
  );
}
