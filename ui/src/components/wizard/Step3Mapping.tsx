'use client';

// Two editors behind one step. The path domains get an ordered include/exclude
// list over a single unit; the DBMS domain gets an unordered exclude-only list
// per database. Unifying them would teach the wrong model for one of the two.
//
// One card per source connection Step 2 kept in the plan, each against its own
// destination. A single connection is the same step with one card, drawn open
// and without a header.

import React from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { Button, Loading, Note } from '@/components/ui/primitives';
import { Step3PathMapping, crossTargetProblem } from './Step3PathMapping';
import { Step3DbmsMapping, seedDbEntries } from './Step3DbmsMapping';
import { mappingConflicts } from './buildPlanRequest';
import { dstKey } from '@/lib/refKey';
import { beetleDstPath, beetleOsIdOf } from '@/lib/bucketPath';
import { ADAPTERS, entryOf } from '@/components/domains/adapters';
import { useConnections, useRefinedScope } from '@/queries/honeybee';
import type { Category } from '@/lib/connectionCategory';
import { mappingOf, routeOf, useWizardStore, type MappingDraft } from '@/store/wizardStore';
import type { SourceDBModel } from '@/types/source';

export function Step3Mapping({ category, onDone }: { category: Category; onDone: () => void }) {
  const adapter = ADAPTERS[category];
  const state = useWizardStore((s) => s.byCategory[category]);
  const edit = useWizardStore((s) => s.edit);

  const sgId = state.source?.sgId ?? '';
  const connId = state.source?.connId ?? null;
  const groupMode = !connId;

  const conns = useConnections(sgId, !!sgId);
  const refined = useRefinedScope(category, sgId || null, connId, !!sgId);

  /** The connections Step 2 kept, each with what its card needs. */
  const cards = React.useMemo(() => {
    // Step 1's scope, as in Step 2.
    return (conns.data?.connection_info ?? [])
      .filter((c) => state.source?.connIds.includes(c.id) && routeOf(state, c.id).selected)
      .map((c) => {
        const entry = entryOf(adapter, refined.data, c.id);
        const units = adapter.unitsOf(entry);
        const route = routeOf(state, c.id);
        return {
          connId: c.id,
          name: c.name,
          dbType: c.db_type,
          target: route.target,
          targetLabel: route.targetLabel,
          entry,
          units,
          mapping: mappingOf(
            state,
            c.id,
            category === 'database'
              ? { dbEntries: seedDbEntries(units) }
              : { srcName: adapter.srcNameOf(entry) },
          ),
        };
      });
  }, [conns.data, state, adapter, refined.data, category]);

  const conflicts = mappingConflicts(category, cards);

  // Who else writes to each card's target — what the target field's example
  // and hint are written against.
  const sharing = (card: (typeof cards)[number]) => {
    if (!card.target) return [];
    const key = dstKey(card.target);
    return cards
      .filter((o) => o.connId !== card.connId && o.target && dstKey(o.target) === key)
      .map((o) => o.name);
  };

  /** A Step 3 edit to one card. The first write stores the seeded mapping too. */
  const change = (card: (typeof cards)[number], patch: Partial<MappingDraft>) =>
    edit(category, 3, { entries: { ...state.entries, [card.connId]: { ...card.mapping, ...patch } } });

  const [closed, setClosed] = React.useState<Set<string>>(() => new Set());
  const toggle = (id: string) =>
    setClosed((cur) => {
      const next = new Set(cur);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  const complete = (card: (typeof cards)[number]) =>
    category === 'database'
      ? card.mapping.dbEntries.some((d) => d.selected)
      : !!card.mapping.srcName &&
        !crossTargetProblem(category, card.target, card.mapping.dstPath);
  const ready =
    cards.length > 0 && cards.every(complete) && Object.keys(conflicts).length === 0;

  const editor = (card: (typeof cards)[number]) =>
    category === 'database' ? (
      <Step3DbmsMapping
        server={card.entry as SourceDBModel | undefined}
        dbType={card.dbType}
        units={card.units}
        mapping={card.mapping}
        onChange={(p) => change(card, p)}
      />
    ) : (
      <Step3PathMapping
        category={category}
        connId={card.connId}
        connName={card.name}
        units={card.units}
        target={card.target}
        mapping={card.mapping}
        shared={sharing(card)}
        onChange={(p) => change(card, p)}
      />
    );

  /** What a closed card still says: the move and its rule count. */
  const summary = (card: (typeof cards)[number]) => {
    const m = card.mapping;
    if (category === 'database') {
      return `${m.dbEntries.filter((d) => d.selected).length} database(s) selected`;
    }
    const osId = beetleOsIdOf(card.target);
    const dst = osId ? beetleDstPath(osId, m.dstPath) : m.dstPath || '(same path)';
    return `${m.srcName || '(none)'} → ${dst} · ${m.pathRules.length} rule(s)`;
  };

  if (refined.isPending || conns.isPending) return <Loading label="reading scan…" />;

  return (
    <div className="space-y-5">
      {!groupMode && cards[0] ? (
        <>
          {editor(cards[0])}
          {conflicts[cards[0].connId] && <Note tone="warn">{conflicts[cards[0].connId]}</Note>}
        </>
      ) : (
        <ul className="space-y-3">
          {cards.map((card) => {
            const open = !closed.has(card.connId);
            const conflict = conflicts[card.connId];
            return (
              <li key={card.connId} className="panel p-4 space-y-3">
                <button
                  type="button"
                  aria-expanded={open}
                  onClick={() => toggle(card.connId)}
                  className="w-full flex flex-wrap items-center gap-x-2 gap-y-1 text-left text-xs font-mono cursor-pointer"
                >
                  {open ? (
                    <ChevronDown className="w-3 h-3 shrink-0 text-text-muted" />
                  ) : (
                    <ChevronRight className="w-3 h-3 shrink-0 text-text-muted" />
                  )}
                  <span className="font-bold">{card.name}</span>
                  <span className="text-text-muted">→ {card.targetLabel || '(no target)'}</span>
                  {!open && <span className="text-text-muted">{summary(card)}</span>}
                </button>
                {/* Said on the card even when it is closed: a collision is
                    what keeps the step from finishing. */}
                {conflict && <Note tone="warn">{conflict}</Note>}
                {open && editor(card)}
              </li>
            );
          })}
        </ul>
      )}

      {groupMode && cards.length === 0 && (
        <Note tone="warn">No connection is in the plan. Check at least one in Step 2.</Note>
      )}

      <div className="flex justify-end border-t border-border-main pt-4">
        <Button variant="primary" disabled={!ready} onClick={onDone}>
          next: plan →
        </Button>
      </div>
    </div>
  );
}
