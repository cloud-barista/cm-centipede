'use client';

// Status vocabulary shared by Overview, Runs and the wizard.

import React from 'react';
import { Badge } from './primitives';
import type { MigrationStatus, MigrationSummary, ValidationStatus } from '@/types/target';
import type { Category } from '@/lib/connectionCategory';

const STATUS_TONE: Record<string, 'ok' | 'warn' | 'bad' | 'info' | 'neutral'> = {
  completed: 'ok',
  running: 'info',
  pending: 'info',
  failed: 'bad',
  cancelled: 'neutral',
};

export function StatusBadge({ status }: { status: MigrationStatus | string }) {
  return <Badge tone={STATUS_TONE[status] ?? 'neutral'}>{status}</Badge>;
}

const VALIDATION_TONE: Record<string, 'ok' | 'warn' | 'bad' | 'neutral'> = {
  passed: 'ok',
  failed: 'bad',
  running: 'warn',
  not_run: 'neutral',
};

export function ValidationBadge({ status }: { status?: ValidationStatus }) {
  const s = status ?? 'not_run';
  return <Badge tone={VALIDATION_TONE[s] ?? 'neutral'}>validation {s}</Badge>;
}

/**
 * Which category a run belongs to, judged from planSummary — the list responses
 * carry no plan, so that the credentials inside one never reach a list.
 */
export function categoryOfSummary(m: MigrationSummary): Category | null {
  const p = m.planSummary;
  if (!p) return null;
  if (p.fileSystems > 0) return 'filesystem';
  if (p.objectStorages > 0) return 'objectstorage';
  if (p.databases > 0) return 'database';
  return null;
}

export function CategoryBadge({ category }: { category: Category | null }) {
  if (!category) return <Badge tone="neutral">unknown</Badge>;
  const cls: Record<Category, string> = {
    filesystem: 'bg-sky-500/10 text-sky-500 dark:text-sky-400 border-sky-500/25',
    objectstorage: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/25',
    database: 'bg-violet-500/10 text-violet-500 dark:text-violet-400 border-violet-500/25',
  };
  return (
    <span
      className={`inline-flex items-center px-2 py-0.5 rounded-md border text-xs font-mono font-bold ${cls[category]}`}
    >
      {category}
    </span>
  );
}

/**
 * A connection or agent check outcome — ran and passed, ran and failed, or has
 * not settled. There is no "not applicable" state here.
 *
 * It used to have one, keyed to a `not_applicable` status this backend has
 * never sent: cm-honeybee reports a check that does not apply as `failed` with
 * an explaining message, so the branch was unreachable and the badge showed
 * agent FAILED on healthy direct connections. Whether a check applies is a
 * question about the connection's shape, not about its result, so the caller
 * settles it (`agentApplies`) and renders nothing where the answer is no.
 */
export function HealthBadge({ label, status }: { label: string; status?: string }) {
  switch (status) {
    case 'success':
      return <Badge tone="ok">{label} OK</Badge>;
    case 'failed':
      return <Badge tone="bad">{label} FAILED</Badge>;
    default:
      return <Badge tone="warn">{label} …</Badge>;
  }
}
