'use client';

// The plan of one run, read from GET /migration/{id} (the list responses carry
// none). Secrets are masked before the raw view renders.
//
// The plan itself is drawn by components/plan/PlanTables, the same component the
// wizard's Step 4 uses: the plan a run started from and the plan that was just
// built are one object, and a reader comparing a finished run against the step
// that produced it should not have to read two layouts.

import React from 'react';
import { Button, ErrorNote, Loading, Note } from '@/components/ui/primitives';
import { PlanTables } from '@/components/plan/PlanTables';
import { maskedJson } from '@/lib/maskSecrets';
import { planIsEmpty } from '@/lib/planEnvelope';
import type { Migration } from '@/types/target';

export function PlanSummaryView({
  detail,
  pending,
  error,
}: {
  detail: Migration | null;
  pending: boolean;
  error: unknown;
}) {
  const [raw, setRaw] = React.useState(false);

  if (pending) return <Loading label="loading plan…" />;
  if (error) return <ErrorNote error={error} />;
  if (!detail) return null;

  return (
    <div>
      <div className="flex items-center gap-2 mb-2">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Plan
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        <Button onClick={() => setRaw((r) => !r)}>
          {raw ? 'hide raw JSON' : 'view raw JSON'}
        </Button>
      </div>

      {/* A run cannot be started from an empty plan, so this is a record that
          was written some other way — said rather than drawn as a blank. */}
      {planIsEmpty(detail.plan) ? (
        <Note tone="warn">This run carries no resource model — there is nothing to show.</Note>
      ) : (
        <PlanTables plan={detail.plan} />
      )}

      {raw && (
        <>
          <pre className="mt-3 max-h-80 overflow-auto rounded-lg bg-bg-input border border-border-input p-3 text-2xs leading-relaxed">
            {maskedJson(detail.plan)}
          </pre>
          <p className="text-xs text-text-muted mt-1">secrets are masked in this view</p>
        </>
      )}
    </div>
  );
}
