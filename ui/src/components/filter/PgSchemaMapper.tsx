'use client';

// PostgreSQL only. An empty list migrates every schema under its own name; a
// non-empty one is a selection as much as a rename (dmdl TargetMapping).

import React from 'react';
import { Button, TextInput } from '@/components/ui/primitives';
import type { PgSchemaMapping } from '@/types/common';

export function PgSchemaMapper({
  mappings,
  onChange,
}: {
  mappings: PgSchemaMapping[];
  onChange: (m: PgSchemaMapping[]) => void;
}) {
  const patch = (i: number, p: Partial<PgSchemaMapping>) =>
    onChange(mappings.map((m, idx) => (idx === i ? { ...m, ...p } : m)));

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <h5 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          PostgreSQL schemas
        </h5>
        <div className="flex-1 h-px bg-border-main" />
        <span className="text-xs text-text-muted">
          empty = every schema under its own name
        </span>
      </div>

      <ul className="space-y-2">
        {mappings.map((m, i) => (
          <li key={i} className="flex items-center gap-2 text-xs font-mono">
            <TextInput
              value={m.srcName}
              onChange={(e) => patch(i, { srcName: e.target.value })}
              placeholder="ex) public"
              className="w-48 !py-1"
            />
            <span className="text-text-muted">→</span>
            <TextInput
              value={m.dstName ?? ''}
              onChange={(e) => patch(i, { dstName: e.target.value })}
              placeholder="(same name)"
              className="w-48 !py-1"
            />
            <Button
              variant="danger"
              onClick={() => onChange(mappings.filter((_, idx) => idx !== i))}
            >
              remove
            </Button>
          </li>
        ))}
      </ul>

      <Button onClick={() => onChange([...mappings, { srcName: '', dstName: '' }])}>
        + add schema mapping
      </Button>
    </div>
  );
}
