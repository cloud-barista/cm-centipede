'use client';

// File system and object storage share the data-mig pipeline: an ORDERED
// include/exclude list where the first match decides and anything matched by no
// rule is kept. Order matters, so the rows can be moved.
//
// This is deliberately a different component from DbmsRuleEditor: drawing both
// with one widget would teach the wrong model for one of them.

import React from 'react';
import { Button, Select, TextInput } from '@/components/ui/primitives';
import { cx } from '@/lib/format';
import type { PathFilterRule, SizeOp } from '@/types/common';

// A pattern is matched against the path relative to the migrated folder or key
// prefix, by rsync's own rules (dmdl PathFilterRule, data-mig filter/glob.go).
// Two of these were written for the matcher that came before:
//
//   "**/*.log"    the "**/" says nothing "*.log" does not — a pattern with no
//                 "/" is matched against the base name at any depth — and it
//                 left every button labelled differently from the rule it added.
//   "**/.git/**"  matched the CONTENTS of .git and not the directory, so rsync
//                 descended into it and created it, empty, at the destination.
//                 ".git/" drops the directory, and a dropped directory takes
//                 everything under it.
//
// Verified against the matcher itself, not read off the patterns.
const PRESETS: Array<{ label: string; rule: PathFilterRule }> = [
  { label: '*.log', rule: { action: 'exclude', type: 'glob', pattern: '*.log' } },
  { label: '*.tmp', rule: { action: 'exclude', type: 'glob', pattern: '*.tmp' } },
  { label: '.git/', rule: { action: 'exclude', type: 'glob', pattern: '.git/' } },
  // ">=", not ">": 100MB exactly is excluded, and the label said otherwise.
  { label: '>= 100MB', rule: { action: 'exclude', type: 'size', op: '>=', value: 104857600 } },
];

const SIZE_OPS: SizeOp[] = ['>', '>=', '<', '<=', '=='];

export function PathRuleEditor({
  rules,
  onChange,
}: {
  rules: PathFilterRule[];
  onChange: (rules: PathFilterRule[]) => void;
}) {
  const patch = (i: number, p: Partial<PathFilterRule>) =>
    onChange(rules.map((r, idx) => (idx === i ? { ...r, ...p } : r)));

  const move = (i: number, delta: number) => {
    const j = i + delta;
    if (j < 0 || j >= rules.length) return;
    const next = [...rules];
    [next[i], next[j]] = [next[j], next[i]];
    onChange(next);
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Rules
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        <span className="text-xs text-text-muted">first match wins</span>
      </div>

      {rules.length === 0 && (
        <p className="text-xs text-text-muted">
          No rule — everything under the selected {`{unit}`} is migrated.
        </p>
      )}

      <ul className="space-y-2">
        {rules.map((rule, i) => (
          <li
            key={i}
            className={cx(
              'flex flex-wrap items-center gap-2 text-xs font-mono px-3 py-2 rounded-lg border',
              rule.action === 'exclude'
                ? 'border-rose-500/25 bg-rose-500/5'
                : 'border-emerald-500/25 bg-emerald-500/5',
            )}
          >
            <span className="w-5 text-text-muted">{i + 1}</span>

            <span className="flex flex-col">
              <button
                type="button"
                onClick={() => move(i, -1)}
                disabled={i === 0}
                className="leading-none text-text-muted hover:text-indigo-600 dark:hover:text-indigo-400 disabled:opacity-30 cursor-pointer"
                title="Move up — order changes the outcome"
              >
                ▲
              </button>
              <button
                type="button"
                onClick={() => move(i, 1)}
                disabled={i === rules.length - 1}
                className="leading-none text-text-muted hover:text-indigo-600 dark:hover:text-indigo-400 disabled:opacity-30 cursor-pointer"
                title="Move down"
              >
                ▼
              </button>
            </span>

            <Select
              value={rule.action}
              onChange={(e) => patch(i, { action: e.target.value as 'include' | 'exclude' })}
              className="w-28 !py-1"
            >
              <option value="exclude">exclude</option>
              <option value="include">include</option>
            </Select>

            <Select
              value={rule.type}
              onChange={(e) =>
                patch(i, {
                  type: e.target.value as 'glob' | 'size',
                  ...(e.target.value === 'glob'
                    ? { op: undefined, value: undefined, pattern: rule.pattern ?? '' }
                    : { pattern: undefined, op: '>=' as SizeOp, value: rule.value ?? 0 }),
                })
              }
              className="w-24 !py-1"
            >
              <option value="glob">glob</option>
              <option value="size">size</option>
            </Select>

            {rule.type === 'glob' ? (
              <TextInput
                value={rule.pattern ?? ''}
                onChange={(e) => patch(i, { pattern: e.target.value })}
                placeholder="ex) *.log"
                className="flex-1 min-w-[12rem] !py-1"
              />
            ) : (
              <>
                <Select
                  value={rule.op ?? '>='}
                  onChange={(e) => patch(i, { op: e.target.value as SizeOp })}
                  className="w-20 !py-1"
                >
                  {SIZE_OPS.map((op) => (
                    <option key={op} value={op}>
                      {op}
                    </option>
                  ))}
                </Select>
                {/* Takes the leftover width the way the glob row's pattern
                    box does, rather than a fixed w-40. Three fixed boxes plus
                    the unit overran the row, and "remove" — the one control
                    every row has — was the item that wrapped away from it. */}
                <TextInput
                  type="number"
                  value={rule.value ?? 0}
                  onChange={(e) => patch(i, { value: Number(e.target.value) })}
                  className="flex-1 min-w-[6rem] !py-1"
                  // No placeholder: the value falls back to 0, so the box is
                  // never empty and a placeholder could never show. The unit is
                  // printed beside the box instead.
                />
                <span className="text-text-muted shrink-0">bytes</span>
              </>
            )}

            <Button
              variant="danger"
              className="ml-auto shrink-0"
              onClick={() => onChange(rules.filter((_, idx) => idx !== i))}
            >
              remove
            </Button>
          </li>
        ))}
      </ul>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          onClick={() =>
            onChange([...rules, { action: 'exclude', type: 'glob', pattern: '' }])
          }
        >
          + add rule
        </Button>
        {/* Behind a rule and under a label of its own. These add a rule too,
            so they belong on this row — but "presets" set in the smallest
            step, between the buttons it names, read as a third button rather
            than as the word for the four after it. */}
        <span className="flex flex-wrap items-center gap-2 ml-1 pl-3 border-l border-border-main">
          <span className="text-xs font-bold font-mono text-text-muted">presets</span>
          {PRESETS.map((p) => (
            <Button key={p.label} onClick={() => onChange([...rules, { ...p.rule }])}>
              {p.label}
            </Button>
          ))}
        </span>
      </div>
    </div>
  );
}
