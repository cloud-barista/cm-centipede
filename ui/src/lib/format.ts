// Presentation helpers shared across screens.

import type { Category } from '@/lib/connectionCategory';

export function formatBytes(bytes: number | undefined | null, digits = 1): string {
  if (bytes === undefined || bytes === null) return '-';
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.min(Math.floor(Math.log(Math.abs(bytes)) / Math.log(1024)), units.length - 1);
  const v = bytes / Math.pow(1024, i);
  return `${i === 0 ? v : v.toFixed(digits)} ${units[i]}`;
}

export function formatCount(n: number | undefined | null): string {
  if (n === undefined || n === null) return '-';
  return n.toLocaleString();
}

export function formatDuration(ms: number | undefined | null): string {
  if (!ms && ms !== 0) return '-';
  if (ms < 1000) return `${ms}ms`;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

/**
 * "2h ago" — coarse on purpose; the exact stamp is in the tooltip.
 *
 * Takes an API timestamp or epoch milliseconds, the latter because TanStack
 * Query reports dataUpdatedAt that way and every caller was converting it to an
 * ISO string for this function to parse straight back. 0 is "never", which is
 * what that field holds before the first fetch.
 */
export function timeAgo(when: string | number | undefined | null, now = Date.now()): string {
  if (!when) return '-';
  const t = typeof when === 'number' ? when : new Date(when).getTime();
  if (Number.isNaN(t)) return '-';
  const sec = Math.max(0, Math.floor((now - t) / 1000));
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  return new Date(t).toLocaleDateString();
}

/**
 * "staging (1/2)" / "uploading (2/2)" for a relayed transfer's step, undefined
 * for a single-step one, which has no leg to name. A step the server names —
 * a database's "dumping"/"restoring" — keeps that name, and what the server
 * says the step is doing beyond it follows the count: "restoring (2/2) ·
 * indexes deferred: 1 table". The count goes with the name, so it cannot be
 * read as a count of the tables beside it.
 *
 * The transferred figure restarts at zero with each step, and this is what says
 * so: without it a count that falls back reads as progress lost.
 */
export function transferStepLabel(
  step: number | undefined,
  steps: number | undefined,
  name?: string,
  detail?: string,
): string | undefined {
  if (!step || !steps || steps < 2) return undefined;
  const label = name || (step === 1 ? 'staging' : step === steps ? 'uploading' : 'step');
  return detail ? `${label} (${step}/${steps}) · ${detail}` : `${label} (${step}/${steps})`;
}

/**
 * What a database item in flight has done in its current step, as the parts of
 * a progress line: "37/120 tables", "1,234,567 rows". Only what the server could
 * count — a restore through a CLI over SSH reports bytes alone.
 */
export function dbStepFigures(m: {
  currentUnits?: number;
  currentUnitsTotal?: number;
  currentRows?: number;
}): string[] {
  const parts: string[] = [];
  if (m.currentUnitsTotal) {
    parts.push(`${formatCount(m.currentUnits ?? 0)}/${formatCount(m.currentUnitsTotal)} tables`);
  }
  if (m.currentRows) parts.push(`${formatCount(m.currentRows)} rows`);
  return parts;
}

/**
 * What names the item in flight (currentObject): "processing file", "processing
 * object", "processing table". By the plan's own category, so a cross-storage
 * run keeps one word for the thing it carries across both steps. A database
 * plan of MongoDB alone moves collections, not tables.
 */
/**
 * The line under an item that says what it ran at once. The server's note when
 * there is one — why the figure fell short of the request ("single rsync: 54
 * files") — else what the figure counts in the item's category, so an item that
 * ran as asked reads as plainly as one that did not. Null when the item did not
 * report a figure.
 */
export function concurrencyLine(
  applied: number | undefined,
  note: string | undefined,
  category: Category | null,
): string | null {
  if (note) return note;
  if (!applied) return null;
  switch (category) {
    case 'filesystem':
      return applied === 1 ? '1 rsync process' : `${applied} rsync processes`;
    case 'objectstorage':
      return applied === 1 ? '1 object at a time' : `${applied} objects at once`;
    case 'database':
      return applied === 1 ? '1 restore connection' : `${applied} restore connections`;
    default:
      return null;
  }
}

export function currentObjectLabel(category: Category | null, mongoOnly = false): string {
  switch (category) {
    case 'filesystem':
      return 'processing file';
    case 'objectstorage':
      return 'processing object';
    case 'database':
      return mongoOnly ? 'processing collection' : 'processing table';
    default:
      return 'processing';
  }
}

/**
 * How long a migration has run: live while it runs, fixed once it has finished.
 * Undefined before it starts, and for a finished one with no finish stamp.
 *
 * A running one is measured to now even when it carries a completedAt — the
 * server clears that on retry, but a record written before it did keeps the
 * previous run's, which would freeze a running retry at a negative time. The
 * browser's clock is not the server's, so a live figure is floored at zero.
 */
export function migrationElapsed(
  m: { status: string; startedAt?: string; completedAt?: string },
  now: number,
): { ms: number; live: boolean } | undefined {
  if (!m.startedAt) return undefined;
  const start = new Date(m.startedAt).getTime();
  if (Number.isNaN(start)) return undefined;
  if (m.status === 'running' || m.status === 'pending') {
    return { ms: Math.max(0, now - start), live: true };
  }
  if (!m.completedAt) return undefined;
  const end = new Date(m.completedAt).getTime();
  if (Number.isNaN(end)) return undefined;
  return { ms: Math.max(0, end - start), live: false };
}

export function percent(done: number, total: number): number {
  if (!total || total <= 0) return 0;
  return Math.min(100, Math.round((done / total) * 100));
}

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}
