'use client';

// Form controls. Split out of primitives.tsx, which was over the 400-line cap;
// primitives.tsx re-exports everything here, so the import path is unchanged.

import React from 'react';
import { cx } from '@/lib/format';

export function Field({
  label,
  hint,
  required,
  children,
  className,
}: {
  label: string;
  hint?: React.ReactNode;
  required?: boolean;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={cx('block', className)}>
      <span className="flex items-baseline gap-2 mb-1">
        <span className="text-xs font-bold text-text-main font-mono">{label}</span>
        {required && <span className="text-2xs font-mono text-indigo-600 dark:text-indigo-400">required</span>}
        {/* A hint carries the backend contract — "empty = every user database on
            the server", "camelCase, NOT honeybee's ssh-tunnel". It is the most
            instructive text on the form, so it does not sit at the smallest step
            in a monospace face. */}
        {hint && <span className="text-xs text-text-muted">{hint}</span>}
      </span>
      {children}
    </label>
  );
}

export const inputClass =
  'w-full rounded-lg px-3 py-1.5 text-sm font-mono bg-bg-input border border-border-input';

export function TextInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx(inputClass, props.className)} />;
}

export function Select(props: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cx(inputClass, 'cursor-pointer', props.className)} />;
}

export function TextArea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx(inputClass, 'font-mono', props.className)} />;
}

/**
 * Puts the hint on the label's own line instead of under it.
 *
 * For a side-by-side row where only some options carry a hint: a hint under the
 * label makes that one option a line taller than its neighbours, and the
 * controls then sit at visibly different heights. Inline keeps every option one
 * line. Only worth it for a hint short enough not to wrap — a long one below the
 * control costs nothing, because the control is centred on its label either way.
 */
interface ChoiceHint {
  hint?: React.ReactNode;
  inlineHint?: boolean;
}

/**
 * The shape both a radio and a checkbox take: the box is centred on the label,
 * always, on one line of its own. A hint goes under that line, outside the
 * label, indented to start where the label does.
 *
 * The box used to align to the top of a label-plus-hint block, which put it
 * above the centre of the very text it belongs to. Centring on the whole block
 * is no better — it floats the box between the two lines. So the two lines are
 * separated instead, and each is aligned on its own terms.
 */
function Choice({
  input,
  label,
  hint,
  inlineHint,
}: ChoiceHint & { input: React.ReactNode; label: React.ReactNode }) {
  const row = (
    <label className="inline-flex items-center gap-2 cursor-pointer text-sm">
      {input}
      <span className="font-semibold">{label}</span>
      {hint && inlineHint && <span className="text-xs text-text-muted">{hint}</span>}
    </label>
  );
  if (!hint || inlineHint) return row;
  return (
    <div>
      {row}
      {/* pl-5 ≈ the box plus the gap, so the hint starts under the label rather
          than under the box. */}
      <div className="text-xs text-text-muted pl-5">{hint}</div>
    </div>
  );
}

export function Radio({
  name,
  value,
  checked,
  onChange,
  label,
  hint,
  inlineHint,
}: ChoiceHint & {
  name: string;
  value: string;
  checked: boolean;
  onChange: (v: string) => void;
  label: React.ReactNode;
}) {
  return (
    <Choice
      label={label}
      hint={hint}
      inlineHint={inlineHint}
      input={
        <input
          type="radio"
          name={name}
          value={value}
          checked={checked}
          onChange={() => onChange(value)}
          className="accent-indigo-500 cursor-pointer"
        />
      }
    />
  );
}

export function Checkbox({
  checked,
  onChange,
  label,
  hint,
  inlineHint,
}: ChoiceHint & {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: React.ReactNode;
}) {
  return (
    <Choice
      label={label}
      hint={hint}
      inlineHint={inlineHint}
      input={
        <input
          type="checkbox"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
          className="accent-indigo-500 cursor-pointer"
        />
      }
    />
  );
}
