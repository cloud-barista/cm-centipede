// Migration.Name carries a unique index. A repeated name fails on insert and the
// controller returns 500 with the driver's own text, so the UI generates a
// timestamped name and pre-checks it.

import { centipedeApi } from '@/api/centipede';

/**
 * `<base>-YYYYMMDDHHmmss` — local time, matching what the user sees on the clock.
 *
 * Seconds, not minutes. A re-plan and the restart after it happen well inside
 * one minute, so a minute stamp handed back the very name the discarded run had
 * already taken, and the pre-check refused it — a fresh name that was not fresh.
 *
 * The strip accepts either length so a name already carrying a minute stamp is
 * still recognised as stamped rather than kept as part of the base.
 */
export function timestampedName(base: string, now = new Date()): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const stamp =
    `${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}` +
    `${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}`;
  const trimmed = base.trim().replace(/-\d{12}(\d{2})?$/, '');
  return `${trimmed || 'migration'}-${stamp}`;
}

export function suggestName(category: string, now = new Date()): string {
  return timestampedName(`${category}-migration`, now);
}

/**
 * True when the name is already taken.
 *
 * lastDays: 0 is required. /migration/all returns only the last 7 days when no
 * date parameter is given, so without it a collision with an older name goes
 * unseen and the user meets the 500 after submitting.
 */
export async function isNameTaken(name: string): Promise<boolean> {
  const all = await centipedeApi.listAllMigrations({ lastDays: 0 });
  const needle = name.trim().toLowerCase();
  // The index is `collate nocase`, so the comparison is case-insensitive too.
  return all.some((m) => m.name.toLowerCase() === needle);
}

/** Turns the driver's UNIQUE-constraint text into something a user can act on. */
export function translateCreateError(message: string): string {
  if (/unique/i.test(message)) {
    return 'That migration name is already in use. Names are unique across all migrations — pick another.';
  }
  return message;
}
