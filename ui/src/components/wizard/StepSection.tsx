'use client';

// One step of the wizard accordion. A finished step collapses to a one-line
// summary; clicking that summary reopens it to be re-read, and costs nothing —
// only an edit inside a step relocks what came after it (see
// wizardStore's `edit`).

import React from 'react';
import { ChevronDown, ChevronRight, Lock } from 'lucide-react';
import { Badge } from '@/components/ui/primitives';
import { cx } from '@/lib/format';

export type StepState = 'done' | 'current' | 'locked';

export function StepSection({
  n,
  title,
  state,
  open,
  summary,
  onToggle,
  children,
}: {
  n: number;
  title: string;
  state: StepState;
  open: boolean;
  summary?: React.ReactNode;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  const locked = state === 'locked';
  // The summary line renders only on a collapsed step that has one. When it is
  // absent the header is a single line, and the icon belongs on its centre
  // rather than pinned to a top the row no longer has.
  const twoLine = !open && !!summary;

  return (
    <div
      className={cx(
        'panel overflow-hidden',
        // Keyed to open, not to state: a current step that the reader has
        // collapsed is not the panel they are working in, and it used to keep
        // its highlight anyway.
        open && !locked && 'panel-open',
        locked && 'opacity-60',
      )}
    >
      <button
        type="button"
        onClick={locked ? undefined : onToggle}
        disabled={locked}
        className={cx(
          'w-full flex gap-3 px-4 py-3 text-left',
          twoLine ? 'items-start' : 'items-center',
          locked ? 'cursor-not-allowed' : 'cursor-pointer',
        )}
      >
        <span className={cx('text-text-muted', twoLine && 'mt-0.5')}>
          {locked ? (
            <Lock className="w-4 h-4" />
          ) : open ? (
            <ChevronDown className="w-4 h-4" />
          ) : (
            <ChevronRight className="w-4 h-4" />
          )}
        </span>

        <span className="flex-1 min-w-0">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-mono font-extrabold text-indigo-600 dark:text-indigo-400">STEP {n}</span>
            <span className="font-bold text-sm">{title}</span>
            {state === 'done' && <Badge tone="ok">done</Badge>}
            {state === 'current' && <Badge tone="info">current</Badge>}
            {locked && <Badge>locked</Badge>}
          </span>
          {!open && summary && (
            <span className="block text-xs font-mono text-text-muted mt-1 truncate">{summary}</span>
          )}
        </span>
      </button>

      {open && !locked && (
        <div className="border-t border-border-main px-4 py-4 animate-fade-in">{children}</div>
      )}
    </div>
  );
}
