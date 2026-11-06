'use client';

// The shared shell. Steps 4–6 are entirely domain-independent; steps 1–3 defer
// to the adapter for the category.

import React from 'react';
import { Button, Note, PageHeader } from '@/components/ui/primitives';
import { StepSection, type StepState } from './StepSection';
import { useAnchoredAccordion } from '@/hooks/useAnchoredAccordion';
import { Step1Source } from './Step1Source';
import { Step2Target } from './Step2Target';
import { Step3Mapping } from './Step3Mapping';
import { Step4Plan } from './Step4Plan';
import { Step5Run } from './Step5Run';
import { Step6Validate } from './Step6Validate';
import { ADAPTERS } from '@/components/domains/adapters';
import { CATEGORY_LABEL, type Category } from '@/lib/connectionCategory';
import { beetleDstPath, beetleOsIdOf } from '@/lib/bucketPath';
import {
  mappingOf,
  routeOf,
  useWizardStore,
  type Discarded,
  type WizardState,
} from '@/store/wizardStore';
import { useSecretStore } from '@/store/secretStore';

const TITLES = ['Source', 'Target', 'Mapping and filter', 'Plan', 'Run', 'Validate'];

/** Step 3's folded line: the one mapping, or how many there are. */
function mappingSummary(category: Category, state: WizardState): string | undefined {
  const connId = state.source?.connId;
  if (!state.source) return undefined;
  if (!connId) {
    // Untouched cards count too: their mapping is the seeded one.
    const maps = state.source.connIds
      .filter((id) => routeOf(state, id).selected)
      .map((id) => mappingOf(state, id));
    if (maps.length === 0) return undefined;
    const n = maps.length;
    const detail =
      category === 'database'
        ? `${maps.reduce((a, m) => a + m.dbEntries.filter((d) => d.selected).length, 0)} database(s)`
        : `${maps.reduce((a, m) => a + m.pathRules.length, 0)} rule(s)`;
    return `${n} mapping${n === 1 ? '' : 's'} \u00b7 ${detail}`;
  }
  const m = mappingOf(state, connId, { srcName: state.source.srcName });
  const osId = beetleOsIdOf(routeOf(state, connId).target);
  const dst = osId ? beetleDstPath(osId, m.dstPath) : m.dstPath || '(same path)';
  return category === 'database'
    ? `${m.dbEntries.filter((d) => d.selected).length} database(s) selected`
    : `${m.srcName} → ${dst} · ${m.pathRules.length} rule(s)`;
}

/** Step 2's folded line: the one destination, or how many rows have one. */
function targetSummary(state: WizardState): string | undefined {
  const connId = state.source?.connId;
  if (connId) return routeOf(state, connId).targetLabel || undefined;
  // Every connection Step 1 scanned, touched in Step 2 or not.
  const routes = (state.source?.connIds ?? [])
    .map((id) => routeOf(state, id))
    .filter((r) => r.selected);
  if (routes.length === 0) return undefined;
  const set = routes.filter((r) => r.target).length;
  return `${routes.length} source${routes.length === 1 ? '' : 's'} \u2192 ${set} target${set === 1 ? '' : 's'}`;
}

export function MigrationWizard({ category }: { category: Category }) {
  const adapter = ADAPTERS[category];
  const state = useWizardStore((s) => s.byCategory[category]);
  const patch = useWizardStore((s) => s.patch);
  const reset = useWizardStore((s) => s.reset);
  const dismissDiscarded = useWizardStore((s) => s.dismissDiscarded);
  const clearWizardSecret = useSecretStore((s) => s.clearWizardSecret);

  const stateOf = (n: number): StepState => {
    if (n < state.step) return 'done';
    if (n === state.step) return 'current';
    return 'locked';
  };

  // Step 1 in particular is tall — the inspect form plus its result — so
  // collapsing it to open a later step would yank that step up the page.
  const { register, anchorTo } = useAnchoredAccordion(String(state.openStep));

  /**
   * Opening a step only opens it.
   *
   * It used to call invalidateFromStep for any completed step, so clicking Step
   * 1 to re-read which connection was scanned discarded the plan and the
   * reference to a run that was already going. Reading and editing were the
   * same gesture, and only one of them was ever intended. The steps invalidate
   * from their own inputs now (wizardStore's `edit`).
   */
  const openStep = (n: number) => {
    anchorTo(String(n));
    patch(category, { openStep: n === state.openStep ? 0 : n });
  };

  /** Called by a step when its own input is complete. */
  const advance = (from: number) =>
    patch(category, { step: Math.max(state.step, from + 1), openStep: from + 1 });

  const summaries: Array<React.ReactNode> = [
    state.source?.label || undefined,
    targetSummary(state),
    mappingSummary(category, state),
    state.plan ? 'plan ready' : undefined,
    state.migrationId ? `migrationId ${state.migrationId}` : undefined,
    undefined,
  ];

  return (
    <div className="space-y-4">
      <PageHeader
        title={`${CATEGORY_LABEL[category]} Migration`}
        subtitle={adapter.summary}
        right={
          <Button
            onClick={() => {
              if (confirm('Reset this wizard? Everything entered here is discarded.')) {
                reset(category);
                // "Everything" includes the beetleDb password Step 2 parked.
                clearWizardSecret(category);
              }
            }}
          >
            reset wizard
          </Button>
        }
      />

      {state.discarded && (
        <DiscardedNote
          discarded={state.discarded}
          title={TITLES[state.discarded.step - 1]}
          onDismiss={() => dismissDiscarded(category)}
        />
      )}

      <div className="space-y-3">
        <div ref={register('1')}>
          <StepSection
            n={1}
            title={TITLES[0]}
            state={stateOf(1)}
            open={state.openStep === 1}
            summary={summaries[0]}
            onToggle={() => openStep(1)}
          >
            <Step1Source category={category} onDone={() => advance(1)} />
          </StepSection>
        </div>

        <div ref={register('2')}>
          <StepSection
            n={2}
            title={TITLES[1]}
            state={stateOf(2)}
            open={state.openStep === 2}
            summary={summaries[1]}
            onToggle={() => openStep(2)}
          >
            <Step2Target category={category} onDone={() => advance(2)} />
          </StepSection>
        </div>

        <div ref={register('3')}>
          <StepSection
            n={3}
            title={TITLES[2]}
            state={stateOf(3)}
            open={state.openStep === 3}
            summary={summaries[2]}
            onToggle={() => openStep(3)}
          >
            <Step3Mapping category={category} onDone={() => advance(3)} />
          </StepSection>
        </div>

        <div ref={register('4')}>
          <StepSection
            n={4}
            title={TITLES[3]}
            state={stateOf(4)}
            open={state.openStep === 4}
            summary={summaries[3]}
            onToggle={() => openStep(4)}
          >
            <Step4Plan category={category} onDone={() => advance(4)} />
          </StepSection>
        </div>

        <div ref={register('5')}>
          <StepSection
            n={5}
            title={TITLES[4]}
            state={stateOf(5)}
            open={state.openStep === 5}
            summary={summaries[4]}
            onToggle={() => openStep(5)}
          >
            <Step5Run category={category} onDone={() => advance(5)} />
          </StepSection>
        </div>

        <div ref={register('6')}>
          <StepSection
            n={6}
            title={TITLES[5]}
            state={stateOf(6)}
            open={state.openStep === 6}
            onToggle={() => openStep(6)}
          >
            <Step6Validate category={category} />
          </StepSection>
        </div>
      </div>
    </div>
  );
}

/**
 * Says what an edit took with it.
 *
 * The emptying itself is invisible — a later step is collapsed, and its contents
 * are simply not there the next time it is opened. Where that costs a run the
 * person started, the id is worth stating: the migration is still on the server
 * and still on the Runs screen, and only this wizard's pointer to it is gone.
 */
function DiscardedNote({
  discarded,
  title,
  onDismiss,
}: {
  discarded: Discarded;
  title: string;
  onDismiss: () => void;
}) {
  const lost = [discarded.plan && 'the plan', discarded.migrationId && 'this wizard’s run reference']
    .filter(Boolean)
    .join(' and ');

  return (
    <Note tone="warn">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span>
          Editing{' '}
          <strong>
            Step {discarded.step} · {title}
          </strong>{' '}
          discarded {lost}.
          {/* Only where the plan went: a re-plan raises this notice too, and
              telling that reader to build the plan is telling them to redo
              what they just did. */}
          {discarded.plan && ' Build the plan again to continue.'}
          {discarded.migrationId && (
            <>
              {' '}
              Migration <code className="font-mono">{discarded.migrationId}</code> itself is
              untouched — it is still listed under Runs.
            </>
          )}
        </span>
        <Button onClick={onDismiss}>dismiss</Button>
      </div>
    </Note>
  );
}
