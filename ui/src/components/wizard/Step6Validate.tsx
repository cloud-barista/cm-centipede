'use client';

// Identical whichever engine ran the migration: validation always goes through
// cm-centipede's own API.

import React from 'react';
import { Loading, Note } from '@/components/ui/primitives';
import { ValidationPanel } from '@/components/runs/ValidationPanel';
import { useMigration } from '@/queries/centipede';
import { useWizardStore } from '@/store/wizardStore';
import type { Category } from '@/lib/connectionCategory';
import type { MigrationSummary } from '@/types/target';

export function Step6Validate({ category }: { category: Category }) {
  const state = useWizardStore((s) => s.byCategory[category]);
  const migration = useMigration(state.migrationId, !!state.migrationId);

  if (!state.migrationId) {
    return <Note>Nothing to validate yet — start the migration in Step 5 first.</Note>;
  }
  if (migration.isPending) return <Loading />;
  if (!migration.data) return <Note tone="bad">This migration could not be read.</Note>;

  // ValidationPanel works off the summary fields, which the full record also
  // carries — all but planSummary, which it reads to decide whether the
  // quick/checksum choice applies. The record has the plan itself instead, so
  // the counts are taken from that; without them the choice never showed here.
  const p = migration.data.plan?.targetDataMigrationModel;
  const summary: MigrationSummary = {
    ...migration.data,
    planSummary: {
      fileSystems: p?.fileSystems?.length ?? 0,
      objectStorages: p?.objectStorages?.length ?? 0,
      databases: p?.databases?.length ?? 0,
      crossStorage:
        (p?.fileSystems ?? []).filter((f) => f.dstType === 'objectstorage').length +
        (p?.objectStorages ?? []).filter((o) => o.dstType === 'filesystem').length,
    },
  };

  // startOnly: the step's own title says Validate, and until a validation has
  // been asked for there is one thing here to do.
  return <ValidationPanel migration={summary} startOnly />;
}
