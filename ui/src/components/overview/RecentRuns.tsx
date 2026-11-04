'use client';

import React from 'react';
import { Panel, EmptyState, MigrationDuration } from '@/components/ui/primitives';
import { CategoryBadge, StatusBadge, categoryOfSummary } from '@/components/ui/status';
import { timeAgo } from '@/lib/format';
import type { MigrationSummary } from '@/types/target';

export function RecentRuns({
  items,
  now,
  onOpen,
}: {
  items: MigrationSummary[];
  now: number;
  onOpen: (id: string) => void;
}) {
  const recent = React.useMemo(
    () =>
      [...items]
        .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
        .slice(0, 8),
    [items],
  );

  if (recent.length === 0) {
    return <EmptyState>No migration was created in the last 7 days.</EmptyState>;
  }

  return (
    <Panel className="p-0 overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-2xs uppercase tracking-wider text-text-muted border-b border-border-main">
            <th className="text-left font-bold px-4 py-2">name</th>
            <th className="text-left font-bold px-4 py-2">category</th>
            <th className="text-left font-bold px-4 py-2">status</th>
            <th className="text-right font-bold px-4 py-2">duration</th>
            <th className="text-right font-bold px-4 py-2">when</th>
          </tr>
        </thead>
        <tbody>
          {recent.map((m) => (
            <tr
              key={m.id}
              onClick={() => onOpen(m.id)}
              className="border-b border-border-main last:border-0 hover:bg-bg-input/60 cursor-pointer"
            >
              <td className="px-4 py-2 font-mono truncate max-w-[24rem]">{m.name}</td>
              <td className="px-4 py-2">
                <CategoryBadge category={categoryOfSummary(m)} />
              </td>
              <td className="px-4 py-2">
                <StatusBadge status={m.status} />
              </td>
              {/* Bare: the header names it, and a running one is told apart by
                  its status badge and by the figure moving. */}
              <td className="px-4 py-2 text-right text-xs font-mono">
                <MigrationDuration migration={m} bare />
              </td>
              <td
                className="px-4 py-2 text-right text-xs font-mono text-text-muted"
                title={new Date(m.createdAt).toLocaleString()}
              >
                {timeAgo(m.createdAt, now)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}
