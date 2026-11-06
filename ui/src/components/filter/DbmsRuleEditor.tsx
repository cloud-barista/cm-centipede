'use client';

// The dbmsx pipeline is exclude-only and unordered: no include action, no move
// buttons, and a different set of rule types. It looks different from the path
// editor on purpose.

import React from 'react';
import { Button, Select, TextArea, TextInput } from '@/components/ui/primitives';
import type { DBMSFilterRule, DBMSRuleType } from '@/types/common';

const RULE_TYPES: Array<{ id: DBMSRuleType; label: string; hint: string }> = [
  { id: 'table_column', label: 'table_column', hint: 'empty columns = the whole table' },
  { id: 'row_exclude', label: 'row_exclude', hint: 'direct access only' },
  { id: 'object_exclude', label: 'object_exclude', hint: 'view / index / function / …' },
];

const OBJECT_KINDS = ['view', 'index', 'function', 'procedure', 'trigger', 'sequence', 'type'];

export function DbmsRuleEditor({
  rules,
  onChange,
  isMongo = false,
}: {
  rules: DBMSFilterRule[];
  onChange: (rules: DBMSFilterRule[]) => void;
  /** MongoDB takes a row_exclude match as a query document rather than SQL. */
  isMongo?: boolean;
}) {
  const patch = (i: number, p: Partial<DBMSFilterRule>) =>
    onChange(rules.map((r, idx) => (idx === i ? { ...r, ...p } : r)));

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <h5 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Exclude rules
        </h5>
        <div className="flex-1 h-px bg-border-main" />
        <span className="text-xs text-text-muted">exclude-only · order is irrelevant</span>
      </div>

      <ul className="space-y-2">
        {rules.map((rule, i) => (
          <li
            key={i}
            className="flex flex-wrap items-center gap-2 text-xs font-mono px-3 py-2 rounded-lg border border-rose-500/25 bg-rose-500/5"
          >
            <span className="w-5 text-text-muted">{i + 1}</span>

            <Select
              value={rule.type}
              onChange={(e) =>
                patch(i, {
                  type: e.target.value as DBMSRuleType,
                  table: undefined,
                  columns: undefined,
                  match: undefined,
                  kind: e.target.value === 'object_exclude' ? 'view' : undefined,
                  name: undefined,
                })
              }
              className="w-40 !py-1"
            >
              {RULE_TYPES.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.label}
                </option>
              ))}
            </Select>

            {rule.type === 'table_column' && (
              <>
                <TextInput
                  value={rule.table ?? ''}
                  onChange={(e) => patch(i, { table: e.target.value })}
                  placeholder="table (schema-qualified on postgresql)"
                  className="w-64 !py-1"
                />
                <TextInput
                  value={(rule.columns ?? []).join(', ')}
                  onChange={(e) =>
                    patch(i, {
                      columns: e.target.value
                        .split(',')
                        .map((s) => s.trim())
                        .filter(Boolean),
                    })
                  }
                  placeholder="columns (empty = whole table)"
                  className="flex-1 min-w-[12rem] !py-1"
                />
              </>
            )}

            {rule.type === 'row_exclude' && (
              <>
                <TextInput
                  value={rule.table ?? ''}
                  onChange={(e) => patch(i, { table: e.target.value })}
                  placeholder="table"
                  className="w-56 !py-1"
                />
                {/* Sent as typed: the server checks the predicate against the engine. */}
                {isMongo ? (
                  <TextArea
                    value={rule.match ?? ''}
                    onChange={(e) => patch(i, { match: e.target.value })}
                    placeholder={'ex) {"status": "cancelled"}'}
                    rows={2}
                    className="flex-1 min-w-[14rem] !py-1"
                  />
                ) : (
                  <TextInput
                    value={rule.match ?? ''}
                    onChange={(e) => patch(i, { match: e.target.value })}
                    placeholder="ex) created_at < '2024-01-01'"
                    className="flex-1 min-w-[14rem] !py-1"
                  />
                )}
              </>
            )}

            {rule.type === 'object_exclude' && (
              <>
                <Select
                  value={rule.kind ?? 'view'}
                  onChange={(e) => patch(i, { kind: e.target.value })}
                  className="w-32 !py-1"
                >
                  {OBJECT_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </Select>
                <TextInput
                  value={rule.name ?? ''}
                  onChange={(e) => patch(i, { name: e.target.value })}
                  placeholder={
                    rule.kind === 'function' || rule.kind === 'procedure'
                      ? 'name · postgresql: schema.name, schema.name(args)'
                      : 'object name'
                  }
                  title={
                    rule.kind === 'function' || rule.kind === 'procedure'
                      ? 'name or schema.name excludes every overload; schema.name(args), as the inventory lists it, excludes that one routine (postgresql)'
                      : undefined
                  }
                  className="flex-1 min-w-[12rem] !py-1"
                />
                {rule.kind === 'index' && (
                  <TextInput
                    value={rule.table ?? ''}
                    onChange={(e) => patch(i, { table: e.target.value })}
                    placeholder="table (qualifier)"
                    className="w-48 !py-1"
                  />
                )}
              </>
            )}

            <Button
              variant="danger"
              className="ml-auto"
              onClick={() => onChange(rules.filter((_, idx) => idx !== i))}
            >
              remove
            </Button>
          </li>
        ))}
      </ul>

      <div className="flex flex-wrap items-center gap-2">
        {RULE_TYPES.map((t) => (
          <Button
            key={t.id}
            onClick={() =>
              onChange([
                ...rules,
                t.id === 'object_exclude' ? { type: t.id, kind: 'view', name: '' } : { type: t.id },
              ])
            }
            title={t.hint}
          >
            + {t.label}
          </Button>
        ))}
      </div>
    </div>
  );
}
