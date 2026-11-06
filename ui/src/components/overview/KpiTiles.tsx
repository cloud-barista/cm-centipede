'use client';

import React from 'react';
import { Panel } from '@/components/ui/primitives';
import type { MigrationSummary } from '@/types/target';

const TILES: Array<{ key: string; label: string; cls: string }> = [
  { key: 'running', label: 'RUNNING', cls: 'text-indigo-600 dark:text-indigo-400' },
  { key: 'completed', label: 'COMPLETED', cls: 'text-emerald-500 dark:text-emerald-400' },
  { key: 'failed', label: 'FAILED', cls: 'text-rose-500 dark:text-rose-400' },
  { key: 'cancelled', label: 'CANCELLED', cls: 'text-text-muted' },
];

export function KpiTiles({ items }: { items: MigrationSummary[] }) {
  const counts = React.useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of items) c[m.status] = (c[m.status] ?? 0) + 1;
    // pending is folded into running: both mean "not finished yet" to a reader.
    c.running = (c.running ?? 0) + (c.pending ?? 0);
    return c;
  }, [items]);

  return (
    <Panel className="py-4">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        {TILES.map((t) => (
          <div key={t.key} className="text-center">
            <div className={`text-3xl font-extrabold font-mono ${t.cls}`}>{counts[t.key] ?? 0}</div>
            <div className="text-2xs font-bold tracking-label text-text-muted mt-1">
              {t.label}
            </div>
          </div>
        ))}
      </div>
    </Panel>
  );
}
