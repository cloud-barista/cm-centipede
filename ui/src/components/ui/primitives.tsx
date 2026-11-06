'use client';

// Shared building blocks. Screens are vertical single columns of full-width
// panels separated by section bands, so these carry that shape.

import React from 'react';
import { ChevronDown, ChevronRight, RefreshCw, TriangleAlert } from 'lucide-react';
import { cx, formatDuration, migrationElapsed, timeAgo } from '@/lib/format';
import { useNow } from '@/hooks/useNow';
import type { Category } from '@/lib/connectionCategory';

export const CATEGORY_ACCENT: Record<Category, { text: string; bg: string; border: string; bar: string }> = {
  filesystem: {
    text: 'text-sky-500 dark:text-sky-400',
    bg: 'bg-sky-500/10',
    border: 'border-sky-500/30',
    bar: 'bg-sky-500',
  },
  objectstorage: {
    text: 'text-amber-500 dark:text-amber-400',
    bg: 'bg-amber-500/10',
    border: 'border-amber-500/30',
    bar: 'bg-amber-500',
  },
  database: {
    text: 'text-violet-500 dark:text-violet-400',
    bg: 'bg-violet-500/10',
    border: 'border-violet-500/30',
    bar: 'bg-violet-500',
  },
};

export function Panel({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return <section className={cx('panel p-5', className)}>{children}</section>;
}

/** A full-width band splitting the page into major regions. */
export function SectionBand({
  title,
  right,
  hint,
}: {
  title: string;
  right?: React.ReactNode;
  hint?: string;
}) {
  return (
    <div className="flex items-center gap-3 mt-8 mb-3">
      <h2 className="text-xs font-extrabold tracking-section uppercase text-text-muted whitespace-nowrap">
        {title}
      </h2>
      <div className="flex-1 h-px bg-border-main" />
      {hint && <span className="text-xs text-text-muted font-mono">{hint}</span>}
      {right}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  right,
}: {
  title: string;
  subtitle?: React.ReactNode;
  right?: React.ReactNode;
}) {
  return (
    <div className="panel px-5 py-4 flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 className="text-xl font-extrabold tracking-tight">{title}</h1>
        {subtitle && <div className="text-xs text-text-muted mt-1">{subtitle}</div>}
      </div>
      <div className="flex items-center gap-3">{right}</div>
    </div>
  );
}

/**
 * Shows the cadence the data behind it is arriving at. The screen never hides
 * how fresh its numbers are.
 */
export function LivePill({ intervalMs, label }: { intervalMs: number; label?: string }) {
  return (
    <span className="inline-flex items-center gap-2 text-xs text-text-muted font-mono px-2.5 py-1 rounded-full border border-border-main bg-bg-panel">
      <span className="pulse-dot" />
      {label ?? `live · ${Math.round(intervalMs / 1000)}s`}
    </span>
  );
}

/**
 * "updated 4s ago" — how old what is on screen is. The third of the header
 * cluster, beside the cadence and the manual refresh.
 *
 * Takes the query's dataUpdatedAt rather than a formatted string: the
 * formatting is the part that has to re-run every second, and a caller handing
 * over text can only produce the frozen stamp this replaced.
 */
export function UpdatedStamp({
  at,
  label = 'updated',
  small = false,
}: {
  /** Epoch ms, as TanStack Query reports it. 0 or undefined = never fetched. */
  at: number | undefined;
  label?: string;
  /** The chrome step, for a stamp inside a panel rather than in a page header. */
  small?: boolean;
}) {
  const now = useNow();
  return (
    <span className={cx('text-text-muted font-mono', small ? 'text-2xs' : 'text-xs')}>
      {label} {timeAgo(at, now)}
    </span>
  );
}

/**
 * How long a migration ran — "3m 12s elapsed" while it runs, ticking with the
 * app clock, and "12m 4s total" once it has finished. Nothing before it starts.
 * Value then unit, the way the progress figures beside it read. The exact start
 * and finish are in the tooltip.
 */
export function MigrationDuration({
  migration,
  bare = false,
  className,
}: {
  migration: { status: string; startedAt?: string; completedAt?: string };
  /** The figure alone, for a column whose header already names it. */
  bare?: boolean;
  className?: string;
}) {
  const now = useNow();
  const elapsed = migrationElapsed(migration, now);
  if (!elapsed) return null;
  const title = [
    `started ${new Date(migration.startedAt!).toLocaleString()}`,
    !elapsed.live && migration.completedAt
      ? `finished ${new Date(migration.completedAt).toLocaleString()}`
      : null,
  ]
    .filter(Boolean)
    .join(' · ');
  return (
    <span className={className} title={title}>
      {formatDuration(elapsed.ms)}
      {!bare && <span className="text-text-muted"> {elapsed.live ? 'elapsed' : 'total'}</span>}
    </span>
  );
}

export function RefreshButton({
  onClick,
  busy,
  title,
}: {
  onClick: () => void;
  busy?: boolean;
  title?: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title ?? 'Refresh now'}
      className="inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1.5 rounded-lg border border-border-main bg-bg-panel hover:border-indigo-500/40 cursor-pointer transition"
    >
      <RefreshCw className={cx('w-3.5 h-3.5', busy && 'animate-spin')} />
      refresh
    </button>
  );
}

type ButtonVariant = 'primary' | 'ghost' | 'danger';

export function Button({
  children,
  onClick,
  disabled,
  variant = 'ghost',
  type = 'button',
  title,
  className,
}: {
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  variant?: ButtonVariant;
  type?: 'button' | 'submit';
  title?: string;
  className?: string;
}) {
  const base =
    'inline-flex items-center justify-center gap-1.5 text-xs font-bold px-3 py-1.5 rounded-lg border transition cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed';
  const styles: Record<ButtonVariant, string> = {
    primary:
      'bg-indigo-500 border-indigo-500 text-white hover:bg-indigo-400 hover:border-indigo-400',
    ghost: 'bg-bg-panel border-border-main text-text-main hover:border-indigo-500/40',
    danger:
      'bg-rose-500/10 border-rose-500/30 text-rose-500 dark:text-rose-400 hover:bg-rose-500/20',
  };
  return (
    <button
      type={type}
      title={title}
      onClick={onClick}
      disabled={disabled}
      className={cx(base, styles[variant], className)}
    >
      {children}
    </button>
  );
}

export function Badge({
  children,
  tone = 'neutral',
  className,
  title,
}: {
  children: React.ReactNode;
  tone?: 'neutral' | 'ok' | 'warn' | 'bad' | 'info';
  className?: string;
  title?: string;
}) {
  const tones = {
    neutral: 'bg-bg-input text-text-muted border-border-main',
    ok: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/25',
    warn: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/25',
    bad: 'bg-rose-500/10 text-rose-600 dark:text-rose-400 border-rose-500/25',
    info: 'bg-indigo-500/10 text-indigo-600 dark:text-indigo-400 border-indigo-500/25',
  } as const;
  return (
    <span
      title={title}
      className={cx(
        // text-xs, not the meta step: a badge nearly always sits inline with a
        // text-sm name or title, and two steps down reads as fine print beside
        // it. text-2xs is for labels that stand on their own line.
        'inline-flex items-center px-2 py-0.5 rounded-md border text-xs font-mono font-bold whitespace-nowrap',
        tones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

export function ProgressBar({
  value,
  total,
  className,
  barClass,
}: {
  value: number;
  total: number;
  className?: string;
  barClass?: string;
}) {
  const pct = total > 0 ? Math.min(100, Math.round((value / total) * 100)) : 0;
  return (
    <div className={cx('h-2 rounded-full bg-bg-input overflow-hidden', className)}>
      <div
        className={cx('h-full rounded-full transition-all duration-500', barClass ?? 'bg-indigo-500')}
        style={{ width: `${pct}%` }}
      />
    </div>
  );
}

/** An accordion row: list items open downward, never into a side pane. */
export function AccordionRow({
  open,
  onToggle,
  summary,
  right,
  children,
  accent,
}: {
  open: boolean;
  onToggle: () => void;
  summary: React.ReactNode;
  right?: React.ReactNode;
  children?: React.ReactNode;
  accent?: string;
}) {
  return (
    <div className={cx('panel overflow-hidden', open && 'panel-open', accent)}>
      <div className="flex items-start gap-3 px-4 py-3">
        <button
          type="button"
          onClick={onToggle}
          className="mt-0.5 text-text-muted hover:text-indigo-600 dark:hover:text-indigo-400 cursor-pointer"
          aria-expanded={open}
        >
          {open ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
        </button>
        <button
          type="button"
          onClick={onToggle}
          className="flex-1 text-left cursor-pointer min-w-0"
        >
          {summary}
        </button>
        {right && <div className="flex items-center gap-2 shrink-0">{right}</div>}
      </div>
      {open && children && (
        <div className="border-t border-border-main px-4 py-4 animate-fade-in">{children}</div>
      )}
    </div>
  );
}

export function Note({
  children,
  tone = 'info',
}: {
  children: React.ReactNode;
  tone?: 'info' | 'warn' | 'bad';
}) {
  const tones = {
    info: 'border-indigo-500/25 bg-indigo-500/5 text-text-muted',
    warn: 'border-amber-500/30 bg-amber-500/5 text-amber-600 dark:text-amber-300',
    bad: 'border-rose-500/30 bg-rose-500/5 text-rose-600 dark:text-rose-300',
  } as const;
  return (
    <div className={cx('flex gap-2 text-xs rounded-lg border px-3 py-2 leading-relaxed', tones[tone])}>
      <TriangleAlert className="w-4 h-4 shrink-0 mt-0.5" />
      <div className="min-w-0">{children}</div>
    </div>
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : String(error);
  return <Note tone="bad">{message}</Note>;
}

export function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-center text-sm text-text-muted py-10 border border-dashed border-border-main rounded-xl">
      {children}
    </div>
  );
}

export function Loading({ label = 'loading…' }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 text-sm text-text-muted py-6 justify-center">
      <RefreshCw className="w-4 h-4 animate-spin" />
      {label}
    </div>
  );
}

// The form controls live in ./form.tsx — this file was over the 400-line cap.
// Re-exported here so every call site keeps importing from one place.
export * from './form';
