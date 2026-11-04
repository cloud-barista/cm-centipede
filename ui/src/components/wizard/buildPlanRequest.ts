// Turns the wizard's drafts into POST /plans/target's body.
//
// `source` is the refined response passed through untouched — the endpoint
// requires exactly that shape — and `plans` holds one entry per
// source connection Step 2 kept, each with its own destination and filter.
// Which entry is which is said with the ref the scan itself carries, never one
// rebuilt from the draft.

import type { PlanEntry, TargetPlanReq } from '@/api/centipede';
import type { SourceDataMigrationModel } from '@/types/source';
import type { ConnectionRef, DBMSFilterEntry } from '@/types/common';
import type { Category } from '@/lib/connectionCategory';
import { mappingOf, routeOf, type MappingDraft, type WizardState } from '@/store/wizardStore';
import { categoryOfRef } from '@/lib/connectionCategory';
import { dstKey } from '@/lib/refKey';
import { beetleDstPath, beetleOsIdOf } from '@/lib/bucketPath';
import { ADAPTERS } from '@/components/domains/adapters';
import type { WizardSecret } from '@/store/secretStore';

/**
 * Puts the session-held admin login back on a beetleDb reference. The draft in
 * the wizard store never carries it.
 */
export function withTargetSecret(
  target: ConnectionRef,
  secret: WizardSecret | undefined,
): ConnectionRef {
  if (target.source !== 'beetleDb' || !target.beetleDb) return target;
  return {
    ...target,
    beetleDb: {
      ...target.beetleDb,
      username: secret?.username.trim() || undefined,
      password: secret?.password ?? '',
    },
  };
}

/** What the plan is built from besides the wizard's own drafts. */
export interface PlanContext {
  /** Step 1's refined response — a group's or a single connection's. */
  source: SourceDataMigrationModel | undefined;
  /** The source group's connections as they are now, for names and late additions. */
  connections: Array<{ id: string; name: string }>;
  /** The beetleDb login Step 2 parked for a connection. */
  secretOf: (connId: string) => WizardSecret | undefined;
}

/** A source connection Step 2 kept in the plan, with everything its entry needs. */
interface PlanItem extends MappingCard {
  /** The scan's own ref for this connection — never one rebuilt from the draft. */
  srcConnection: ConnectionRef | undefined;
}

/**
 * The connections the plan covers: Step 1's scope, minus the ones Step 2 left
 * out, each with its scan entry, destination and mapping.
 *
 * The source ref is taken from the scan entry itself. The server matches a plan
 * entry to a source entry by honeybee.connectionId, and taking the ref from the
 * entry means the id compared is the one cm-honeybee wrote — a ref assembled
 * here would match only as long as the two spellings agreed.
 */
function planItems(category: Category, state: WizardState, ctx: PlanContext): PlanItem[] {
  const adapter = ADAPTERS[category];
  const scanned = new Map(adapter.entriesOf(ctx.source).map((e) => [e.connId, e.entry]));
  const nameOf = (id: string) => ctx.connections.find((c) => c.id === id)?.name ?? id;

  return (state.source?.connIds ?? [])
    .filter((id) => routeOf(state, id).selected)
    .map((id) => {
      const entry = scanned.get(id);
      return {
        connId: id,
        name: nameOf(id),
        target: routeOf(state, id).target,
        mapping: mappingOf(state, id, { srcName: adapter.srcNameOf(entry) }),
        srcConnection: entry?.connection,
      };
    });
}

/**
 * The plans[] entry for one connection: its own destination and its own
 * filter. A source entry no plans[] entry names is not migrated (the server's
 * BuildTargetDataMigrationModel), which is how a connection Step 2 left out
 * stays out of the run.
 */
function planEntryOf(category: Category, item: PlanItem, dstConnection: ConnectionRef): PlanEntry {
  const srcConnection = item.srcConnection as ConnectionRef;
  const m = item.mapping;
  const srcName = m.srcName;
  // A cm-beetle bucket always goes out under its osId: the first segment is
  // not the reader's to choose (lib/bucketPath).
  const osId = category !== 'database' ? beetleOsIdOf(dstConnection) : undefined;
  const dstName = osId ? beetleDstPath(osId, m.dstPath) : m.dstPath.trim();

  if (category === 'filesystem') {
    return {
      srcConnection,
      dstConnection,
      fileSystemStrategy: m.strategy,
      fileSystemFilter: {
        rules: m.pathRules,
        targetMapping: { srcName, ...(dstName ? { dstName } : {}) },
      },
    };
  }

  if (category === 'objectstorage') {
    return {
      srcConnection,
      dstConnection,
      objectStorageStrategy: m.strategy,
      objectStorageFilter: {
        rules: m.pathRules,
        targetMapping: { srcName, ...(dstName ? { dstName } : {}) },
      },
    };
  }

  const databases: DBMSFilterEntry[] = m.dbEntries
    .filter((e) => e.selected)
    .map((e) => ({
      targetMapping: {
        srcName: e.srcName,
        ...(e.dstName.trim() && e.dstName.trim() !== e.srcName ? { dstName: e.dstName.trim() } : {}),
        ...(e.pgSchemas.filter((p) => p.srcName.trim()).length > 0
          ? {
              pgSchemas: e.pgSchemas
                .filter((p) => p.srcName.trim())
                .map((p) => ({
                  srcName: p.srcName.trim(),
                  ...(p.dstName?.trim() ? { dstName: p.dstName.trim() } : {}),
                })),
            }
          : {}),
      },
      ...(e.rules.length > 0 ? { rules: e.rules } : {}),
    }));

  return { srcConnection, dstConnection, dbmsFilter: { databases } };
}

/**
 * The request body. Call only when planBlockers is empty — it is what makes
 * every item here have a scan entry and a destination.
 */
export function buildPlanRequest(
  category: Category,
  state: WizardState,
  ctx: PlanContext,
): TargetPlanReq {
  if (!ctx.source) throw new Error('Step 1 has no scan result yet.');
  const plans = planItems(category, state, ctx).map((item) =>
    planEntryOf(
      category,
      item,
      withTargetSecret(item.target as ConnectionRef, ctx.secretOf(item.connId)),
    ),
  );
  if (plans.length === 0) {
    // Caught by Step 4 and shown as the plan's error. The alternative is a
    // request with an empty plans array, which the server answers with the same
    // fact in its own words one round trip later.
    throw new Error('No source connection is in the plan, so there is nothing to plan.');
  }
  return { source: ctx.source, plans };
}

/** Reasons the plan cannot be requested yet. */
export function planBlockers(category: Category, state: WizardState, ctx: PlanContext): string[] {
  if (!state.source?.inspected) return ['Step 1 has no scan result yet.'];

  const errs: string[] = [];
  const names = (items: Array<{ name: string }>) => items.map((i) => i.name).join(', ');

  // A connection added to the group since Step 1 was neither scanned nor
  // routed. Planning without it would leave it out silently.
  if (!state.source.connId) {
    const scope = new Set(state.source.connIds);
    const added = ctx.connections.filter((c) => !scope.has(c.id));
    if (added.length > 0) {
      errs.push(`Added to the source group since Step 1: ${names(added)}. Go back to Step 1.`);
    }
  }

  const items = planItems(category, state, ctx);
  if (items.length === 0) {
    errs.push('No connection is selected in Step 2.');
    return errs;
  }

  const unscanned = items.filter((i) => !i.srcConnection);
  if (unscanned.length > 0) errs.push(`Not scanned in Step 1: ${names(unscanned)}.`);

  const noTarget = items.filter((i) => !i.target);
  if (noTarget.length > 0) errs.push(`No destination in Step 2 for ${names(noTarget)}.`);

  const noUsername = items.filter(
    (i) => i.target?.source === 'beetleDb' && !ctx.secretOf(i.connId)?.username.trim(),
  );
  if (noUsername.length > 0) errs.push(`No admin username in Step 2 for ${names(noUsername)}.`);

  const noPassword = items.filter(
    (i) => i.target?.source === 'beetleDb' && !ctx.secretOf(i.connId)?.password.trim(),
  );
  if (noPassword.length > 0) errs.push(`No admin password in Step 2 for ${names(noPassword)}.`);

  const unmapped = items.filter((i) =>
    category === 'database' ? !i.mapping.dbEntries.some((e) => e.selected) : !i.mapping.srcName,
  );
  if (unmapped.length > 0) {
    errs.push(
      category === 'database'
        ? `No database selected in Step 3 for ${names(unmapped)}.`
        : `No source path in Step 3 for ${names(unmapped)}.`,
    );
  }

  const conflicts = mappingConflicts(category, items);
  for (const i of items) {
    if (conflicts[i.connId]) errs.push(`${i.name}: ${conflicts[i.connId]}`);
  }
  return errs;
}

/** One Step 3 card: a selected source connection, its destination, its mapping. */
export interface MappingCard {
  connId: string;
  name: string;
  target: ConnectionRef | null;
  mapping: MappingDraft;
}

/**
 * The paths (or database names) a card writes to, as the server resolves them:
 * an empty destination falls back to the source name (selectMigrationPath in
 * pkg/core/plan/target.go), and a beetleObjectStorage destination is its osId
 * with the typed prefix after it, as planEntryOf sends it (lib/bucketPath).
 */
export function destinationsOf(category: Category, card: MappingCard): string[] {
  const m = card.mapping;
  if (category === 'database') {
    return m.dbEntries.filter((e) => e.selected).map((e) => e.dstName.trim() || e.srcName);
  }
  const osId = beetleOsIdOf(card.target);
  if (osId) return [beetleDstPath(osId, m.dstPath)];
  const dst = m.dstPath.trim();
  if (dst) return [dst];
  return m.srcName ? [m.srcName] : [];
}

const isRoot = (p: string) => p.trim() !== '' && p.trim().replace(/\/+$/, '') === '';

/**
 * Why each card would be refused, by connection id — the server's
 * DuplicateDestination and RootToRoot, checked before the round trip.
 *
 * Only cards sharing a target can collide, and the first card to claim a
 * destination keeps it: the message goes on the later one, naming the earlier.
 */
export function mappingConflicts(category: Category, cards: MappingCard[]): Record<string, string> {
  const out: Record<string, string> = {};
  const claimed = new Map<string, MappingCard>();
  const what = category === 'database' ? 'database' : 'path';

  for (const card of cards) {
    if (!card.target) continue;
    const key = dstKey(card.target);
    for (const dst of destinationsOf(category, card)) {
      const prev = claimed.get(`${key}|${dst}`);
      if (prev && prev.connId !== card.connId) {
        out[card.connId] ??= `Same target and ${what} as ${prev.name} (${dst}) — the server refuses two sources writing to one place.`;
      } else {
        claimed.set(`${key}|${dst}`, card);
      }
    }

    if (
      category === 'filesystem' &&
      categoryOfRef(card.target) === 'filesystem' &&
      isRoot(card.mapping.srcName) &&
      destinationsOf(category, card).some(isRoot)
    ) {
      out[card.connId] ??= 'Copies "/" over the target\'s "/" — pick a directory below the root on either side.';
    }
  }
  return out;
}
