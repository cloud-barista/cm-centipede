'use client';

import React from 'react';
import { Panel, CATEGORY_ACCENT } from '@/components/ui/primitives';
import { categoryOfSummary } from '@/components/ui/status';
import { CATEGORIES, CATEGORY_LABEL } from '@/lib/connectionCategory';
import type { MigrationSummary } from '@/types/target';

export function CategoryBars({ items }: { items: MigrationSummary[] }) {
  const counts = React.useMemo(() => {
    const c = { filesystem: 0, objectstorage: 0, database: 0 } as Record<string, number>;
    for (const m of items) {
      const cat = categoryOfSummary(m);
      if (cat) c[cat] += 1;
    }
    return c;
  }, [items]);

  const max = Math.max(1, ...Object.values(counts));

  return (
    <Panel>
      <ul className="space-y-3">
        {CATEGORIES.map((cat) => (
          <li key={cat} className="flex items-center gap-4">
            <span className="w-32 text-xs font-bold text-text-muted shrink-0">
              {CATEGORY_LABEL[cat]}
            </span>
            <div className="flex-1 h-3 rounded-full bg-bg-input overflow-hidden">
              <div
                className={`h-full rounded-full transition-all duration-500 ${CATEGORY_ACCENT[cat].bar}`}
                style={{ width: `${(counts[cat] / max) * 100}%` }}
              />
            </div>
            <span className="w-10 text-right text-sm font-mono font-bold">{counts[cat]}</span>
          </li>
        ))}
      </ul>
    </Panel>
  );
}
