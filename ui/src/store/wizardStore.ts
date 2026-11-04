'use client';

// Wizard drafts. NOT persisted: a DB inspect result and a plan both carry
// credentials and can run to megabytes (constraints 16, 20), so this state dies
// with the tab.

import { create } from 'zustand';
import type { Category } from '@/lib/connectionCategory';
import type {
  ConnectionRef,
  DBMSFilterRule,
  PathFilterRule,
  PgSchemaMapping,
  Strategy,
} from '@/types/common';
import type { DBMSOnFailure, TargetDataMigrationModel } from '@/types/target';
import type { EngineId } from '@/lib/runner/types';

export interface SourceSelection {
  sgId: string;
  /** null = every connection of the source group. */
  connId: string | null;
  /**
   * The connections Step 1 scanned — the one picked, or the group's as they
   * were then. The steps after it work over these; one added to the group
   * since is caught by planBlockers rather than slipped into the plan.
   */
  connIds: string[];
  /** What Step 1's folded line says: "fs-group-01 · 4 connections", or the connection. */
  label: string;
  /**
   * Read out of the refined response — never typed by a person.
   * Empty for a whole group, where each connection has a scan root of its own.
   */
  srcName: string;
  /**
   * Step 1's gate: every connection in scope has a usable scan, found stored or
   * run here. One unscanned connection keeps the whole group closed.
   */
  inspected: boolean;
}

/**
 * Where one source connection goes, and whether it goes at all.
 *
 * The beetleDb password is deliberately absent — it lives in
 * secretStore.wizardSecrets under targetPasswordKey(category, connId).
 */
export interface Route {
  /** In the plan. Unchecking keeps the target, so checking again restores it. */
  selected: boolean;
  target: ConnectionRef | null;
  targetLabel: string;
}

/** A connection Step 2 has not touched yet: in the plan, with no target. */
const NEW_ROUTE: Route = { selected: true, target: null, targetLabel: '' };

/** The route of `connId`, as Step 2 would show it. */
export function routeOf(state: WizardState, connId: string): Route {
  return state.routes[connId] ?? NEW_ROUTE;
}

/**
 * Step 3 for one source connection: which scanned unit moves, where to, and
 * what is filtered out on the way.
 */
export interface MappingDraft {
  /** The unit picked from this connection's scan — its scan root until changed. */
  srcName: string;
  // path domains
  dstPath: string;
  strategy: Strategy;
  pathRules: PathFilterRule[];
  // dbms domain
  dbEntries: DbEntryDraft[];
}

/**
 * The mapping of `connId`, or — before anyone has touched it — a fresh one
 * built from `seed`: the connection's scan root, and for a database source one
 * unselected entry per database the scan found.
 */
export function mappingOf(
  state: WizardState,
  connId: string,
  seed: Partial<MappingDraft> = {},
): MappingDraft {
  return (
    state.entries[connId] ?? {
      srcName: '',
      dstPath: '',
      strategy: 'direct',
      pathRules: [],
      dbEntries: [],
      ...seed,
    }
  );
}

/** One database's mapping plus its exclude rules (dbms category only). */
export interface DbEntryDraft {
  srcName: string;
  dstName: string;
  selected: boolean;
  rules: DBMSFilterRule[];
  pgSchemas: PgSchemaMapping[];
}

/**
 * What the last edit threw away.
 *
 * An edit in an early step silently empties the later ones, which is right but
 * invisible: the reader typed one character in Step 3 and Steps 4-6 quietly went
 * blank. This is what the screen says about it afterwards.
 */
export interface Discarded {
  /** The step that was edited. */
  step: number;
  plan: boolean;
  /** The run this wizard was pointing at, kept so the notice can name it. */
  migrationId: string | null;
}

export interface WizardState {
  step: number;
  /** The step currently expanded in the accordion. */
  openStep: number;

  source: SourceSelection | null;

  /**
   * Step 2, per source connection id. Survives a re-inspect — where each host
   * goes does not depend on what its scan found — and is cleared only when the
   * source group or connection changes (Step 1's changeSource).
   */
  routes: Record<string, Route>;

  /**
   * Step 3, per source connection id. Kept for a connection Step 2 unchecks,
   * so checking it again brings its mapping back.
   */
  entries: Record<string, MappingDraft>;

  // Step 4
  plan: TargetDataMigrationModel | null;
  targetModelId: string | null;
  engine: EngineId;

  // Step 5
  runName: string;
  runDescription: string;
  dbmsOnFailure: DBMSOnFailure;
  /** null: not set, the server default applies. */
  concurrency: number | null;
  migrationId: string | null;

  discarded: Discarded | null;
}

const emptyWizard = (): WizardState => ({
  step: 1,
  openStep: 1,
  source: null,
  routes: {},
  entries: {},
  plan: null,
  targetModelId: null,
  engine: 'centipede',
  runName: '',
  runDescription: '',
  dbmsOnFailure: 'cleanup',
  concurrency: null,
  migrationId: null,
  discarded: null,
});

/**
 * What a change in step `n` makes untrue.
 *
 * Opening a step is not a change. Expanding a finished step to check what was
 * chosen there used to run this and take the plan and the run with it, so the
 * only way to re-read Step 1 was to lose Steps 4-6 — `patch` is for a write that
 * derives nothing, `edit` is for a person changing an input, and only `edit`
 * comes here.
 *
 * Each block names what the edited input feeds. Nothing is discarded because it
 * merely sits later in the list: the destination in Step 2 does not depend on
 * the scan in Step 1, so re-inspecting keeps it.
 */
function downstreamOf(n: number): Partial<WizardState> {
  const next: Partial<WizardState> = {};

  // The mapping names folders, buckets or databases read out of the scan, so a
  // different connection or a fresh inspect leaves it pointing at nothing.
  if (n <= 1) next.entries = {};

  // The plan is computed from steps 1-3. A step-4 edit IS the plan, which is
  // why this stops at 3.
  if (n <= 3) next.plan = null;

  if (n <= 4) {
    next.targetModelId = null;
    next.engine = 'centipede';
    // Step 5 refuses to start a second run while one is referenced, so keeping
    // the reference would make the new plan unrunnable. Only the reference
    // goes: the migration belongs to the server and stays on the Runs screen.
    next.migrationId = null;
  }

  return next;
}

interface WizardStoreState {
  byCategory: Record<Category, WizardState>;
  get(category: Category): WizardState;
  /** A write that derives nothing — navigation, and results read back from a query. */
  patch(category: Category, patch: Partial<WizardState>): void;
  /**
   * A change the person made in step `n`. Applies it, discards what was derived
   * from it, and relocks the steps that produced it.
   *
   * `n` is passed rather than inferred from the patched fields: a Step 2
   * target change also writes the mapping of its row (a different kind of
   * destination makes its path and strategy meaningless), and must still
   * cascade as Step 2. The patch may be omitted where the input lives in
   * component state (Step 1's selects).
   */
  edit(category: Category, n: number, patch?: Partial<WizardState>): void;
  dismissDiscarded(category: Category): void;
  reset(category: Category): void;
}

export const useWizardStore = create<WizardStoreState>((set, get) => ({
  byCategory: {
    filesystem: emptyWizard(),
    objectstorage: emptyWizard(),
    database: emptyWizard(),
  },

  get: (category) => get().byCategory[category],

  patch: (category, patch) =>
    set((s) => ({
      byCategory: { ...s.byCategory, [category]: { ...s.byCategory[category], ...patch } },
    })),

  edit: (category, n, patch = {}) =>
    set((s) => {
      const cur = s.byCategory[category];
      const down = downstreamOf(n);

      // What this particular edit took. The second keystroke in a field takes
      // nothing — the plan went with the first — and must not replace the notice
      // the first one raised with an empty one.
      const lostPlan = 'plan' in down && cur.plan !== null && !('plan' in patch);
      const lostRun =
        'migrationId' in down && cur.migrationId !== null && !('migrationId' in patch);
      // A new plan answers whatever the standing notice was about.
      const answered = 'plan' in patch;

      const next: WizardState = {
        ...cur,
        ...down,
        // The name goes with the run reference, and only with it. Migration.Name
        // is unique across every migration ever made, so the name a run has
        // already taken can never start another one: keeping it in the field
        // meant a re-plan led to Step 5 holding a name the server would refuse.
        // Step 5 fills the empty field with a fresh suggestion when it opens.
        //
        // A name no run has used is still free, so it survives — including one
        // someone typed, which is the whole reason this is not in downstreamOf.
        ...(lostRun ? { runName: '' } : {}),
        // After the cascade, so an edit that sets a field this step owns wins.
        ...patch,
        // The steps after this one no longer hold what they claimed to, so they
        // go back to locked. openStep is left alone: the person is reading the
        // step they just typed in.
        step: Math.min(cur.step, n),
        discarded:
          lostPlan || lostRun
            ? { step: n, plan: lostPlan, migrationId: lostRun ? cur.migrationId : null }
            : answered
              ? null
              : cur.discarded,
      };
      return { byCategory: { ...s.byCategory, [category]: next } };
    }),

  dismissDiscarded: (category) =>
    set((s) => ({
      byCategory: {
        ...s.byCategory,
        [category]: { ...s.byCategory[category], discarded: null },
      },
    })),

  reset: (category) =>
    set((s) => ({ byCategory: { ...s.byCategory, [category]: emptyWizard() } })),
}));
