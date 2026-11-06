'use client';

// POST /plans/target. The planner's errors[] goes at the very top: all three
// resource models carry that field, and folding it away lets a run start on a
// plan nobody read.

import React from 'react';
import { Button, ErrorNote, Loading, Note } from '@/components/ui/primitives';
import { centipedeApi } from '@/api/centipede';
import { PlanTables } from '@/components/plan/PlanTables';
import { useConnections, useRefinedScope } from '@/queries/honeybee';
import { maskedJson } from '@/lib/maskSecrets';
import { planErrors, planIsEmpty } from '@/lib/planEnvelope';
import { targetModelStore } from '@/lib/targetModelStore';
import { targetPasswordKey, useSecretStore } from '@/store/secretStore';
import { useWizardStore } from '@/store/wizardStore';
import { buildPlanRequest, planBlockers, type PlanContext } from './buildPlanRequest';
import { SaveTargetModelBlock } from './SaveTargetModelBlock';
import type { Category } from '@/lib/connectionCategory';

export function Step4Plan({ category, onDone }: { category: Category; onDone: () => void }) {
  const state = useWizardStore((s) => s.byCategory[category]);
  const edit = useWizardStore((s) => s.edit);
  const secrets = useSecretStore((s) => s.wizardSecrets);

  const sgId = state.source?.sgId ?? null;
  // The same document Step 1 read: the group's refined response, or the one
  // connection's. It goes to the planner untouched.
  const refined = useRefinedScope(category, sgId, state.source?.connId ?? null, !!state.source);
  const conns = useConnections(sgId ?? '', !!sgId);

  const ctx: PlanContext = {
    source: refined.data,
    connections: conns.data?.connection_info ?? [],
    secretOf: (connId) => secrets[targetPasswordKey(category, connId)],
  };

  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [raw, setRaw] = React.useState(false);

  const blockers = planBlockers(category, state, ctx);

  async function requestPlan() {
    if (!refined.data) return;
    setPending(true);
    setError(null);
    try {
      const req = buildPlanRequest(category, state, ctx);
      const plan = await centipedeApi.getTargetPlan(req);
      // A re-plan drops the previous run reference, which is what lets Step 5
      // start again — its button is disabled while one is held.
      edit(category, 4, { plan });
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
    }
  }

  const errors = planErrors(state.plan);

  return (
    <div className="space-y-4">
      {blockers.length > 0 ? (
        <ul className="text-xs text-amber-500 dark:text-amber-400 font-mono space-y-0.5">
          {blockers.map((b) => (
            <li key={b}>· {b}</li>
          ))}
        </ul>
      ) : (
        <Button variant="primary" onClick={requestPlan} disabled={pending}>
          {pending ? 'planning…' : state.plan ? 're-plan' : 'build plan'}
        </Button>
      )}

      <ErrorNote error={error} />
      {pending && <Loading label="asking the planner…" />}

      {errors.length > 0 && (
        <Note tone="warn">
          <strong>
            The planner reported {errors.length} issue{errors.length === 1 ? '' : 's'}:
          </strong>
          <ul className="mt-1 space-y-0.5 font-mono">
            {errors.map((e, i) => (
              <li key={i}>· {e}</li>
            ))}
          </ul>
        </Note>
      )}

      {state.plan && (
        <>
          {planIsEmpty(state.plan) ? (
            <Note tone="warn">
              The plan is empty — nothing matched. Check the mapping in Step 3 against the scan
              result in Step 1.
            </Note>
          ) : (
            <PlanTables plan={state.plan} names={Object.fromEntries(ctx.connections.map((c) => [c.id, c.name]))} />
          )}

          <div className="flex items-center gap-3">
            <Button onClick={() => setRaw((r) => !r)}>
              {raw ? 'hide raw JSON' : 'view raw JSON'}
            </Button>
            <span className="text-xs text-text-muted">
              secrets are masked in this view
            </span>
          </div>

          {raw && (
            <pre className="max-h-96 overflow-auto rounded-lg bg-bg-input border border-border-input p-3 text-2xs leading-relaxed">
              {maskedJson(state.plan)}
            </pre>
          )}

          {targetModelStore !== null && <SaveTargetModelBlock category={category} />}

          <div className="flex justify-end border-t border-border-main pt-4">
            <Button variant="primary" onClick={onDone} disabled={planIsEmpty(state.plan)}>
              next: run →
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
