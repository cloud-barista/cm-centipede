'use client';

// `full` drops the target before re-running, so the choice is made explicitly
// and the destructive option is confirmed.

import React from 'react';
import { useRetryMigration } from '@/queries/centipede';
import { Button, ErrorNote, Note, Radio } from '@/components/ui/primitives';
import type { MigrationSummary } from '@/types/target';

export function RetryModal({
  migration,
  onClose,
}: {
  migration: MigrationSummary;
  onClose: () => void;
}) {
  const [mode, setMode] = React.useState<'partial' | 'full'>('partial');
  const [confirmed, setConfirmed] = React.useState(false);
  const retry = useRetryMigration();

  const needsConfirm = mode === 'full' && !confirmed;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div className="panel w-full max-w-lg p-5 bg-bg-main">
        <h3 className="text-lg font-extrabold mb-1">Retry migration</h3>
        <p className="text-xs font-mono text-text-muted mb-4 truncate">{migration.name}</p>

        <div className="space-y-3">
          <Radio
            name="retry-mode"
            value="partial"
            checked={mode === 'partial'}
            onChange={(v) => setMode(v as 'partial')}
            label="Partial"
            hint="re-runs the failed items only"
          />
          <Radio
            name="retry-mode"
            value="full"
            checked={mode === 'full'}
            onChange={(v) => setMode(v as 'full')}
            label="Full"
            hint="drops the target first, then migrates everything again"
          />
        </div>

        {mode === 'full' && (
          <div className="mt-3 space-y-2">
            <Note tone="warn">
              A full retry drops the target before it starts. Everything currently at the
              destination is lost.
            </Note>
            <label className="inline-flex items-center gap-2 text-xs cursor-pointer">
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
                className="accent-rose-500 cursor-pointer"
              />
              I understand the target will be dropped.
            </label>
          </div>
        )}

        <ErrorNote error={retry.error} />

        <div className="flex justify-end gap-2 mt-5">
          <Button onClick={onClose}>cancel</Button>
          <Button
            variant={mode === 'full' ? 'danger' : 'primary'}
            disabled={needsConfirm || retry.isPending}
            onClick={() =>
              retry.mutate(
                { id: migration.id, mode },
                { onSuccess: onClose },
              )
            }
          >
            {retry.isPending ? 'starting…' : `retry ${mode}`}
          </Button>
        </div>
      </div>
    </div>
  );
}
