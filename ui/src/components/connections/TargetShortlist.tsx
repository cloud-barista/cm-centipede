'use client';

// The set-aside targets, drawn two ways for the two jobs they do.
//
//   TargetShortlist  on the Connections screen: a list being managed. It says
//                    how many there are, how fresh the check on them is, and
//                    lets entries be taken off.
//   TargetChoice     in the wizard's Step 2: one field holding one answer. The
//                    counting, the freshness and the removing all belong to the
//                    screen that owns the list, not to the step that reads it.
//
// Both live here because both are readings of one store, under the one label
// format lib/targetLabel.ts defines.

import React from 'react';
import { Badge, Button, Field, LivePill, Note, Select, UpdatedStamp } from '@/components/ui/primitives';
import { useTargetPresence } from '@/queries/beetle';
import { cx } from '@/lib/format';
import { useTargetShortlist, type TargetSelection } from '@/store/targetShortlistStore';
import { locatorOf, type TargetKind, type TargetPresence } from '@/lib/targetPresence';
import { splitTargetLabel } from '@/lib/targetLabel';

/** What cm-beetle says about a row, or nothing yet. */
export function presenceOf(
  check: ReturnType<typeof useTargetPresence>,
  label: string,
): TargetPresence {
  return check.presence.get(label) ?? { state: 'unknown' };
}

/**
 * The kind marker and the path, struck through when cm-beetle no longer lists
 * the target. Struck rather than hidden: the entry is still the reader's.
 */
function RowName({ label, gone }: { label: string; gone: boolean }) {
  const [kind, path] = splitTargetLabel(label);
  return (
    <>
      <span className="text-text-muted shrink-0">{kind}</span>
      <span className={cx('font-bold truncate', gone && 'line-through text-text-muted')}>
        {path}
      </span>
      {gone && (
        <Badge tone="bad" title="cm-beetle no longer lists this target.">
          gone
        </Badge>
      )}
    </>
  );
}

// ── The managed list, on Connections ────────────────────────────────────────

export function TargetShortlist() {
  const items = useTargetShortlist((s) => s.items);
  const remove = useTargetShortlist((s) => s.remove);

  // The list is persisted and outlives the cm-beetle resources it points at, so
  // it is checked against cm-beetle rather than trusted.
  const check = useTargetPresence(items.map((i) => ({ key: i.label, ref: i.ref })));

  return (
    <div className="panel panel-inset p-4">
      <div className="flex items-center gap-2 mb-3">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Selected ({items.length})
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        {items.length > 0 && (
          <span className="flex items-center gap-2">
            {check.updatedAt !== null && (
              <UpdatedStamp at={check.updatedAt} label="checked" small />
            )}
            <LivePill intervalMs={check.intervalMs} />
          </span>
        )}
      </div>

      {/* Said once, at the top, rather than as a marker on every row: when
          cm-beetle cannot be read NOTHING here is verified, and a row-by-row
          "unknown" would read as a list full of problems instead of one
          problem with the list. */}
      {check.unreadable && (
        <div className="mb-3">
          <Note tone="warn">
            cm-beetle could not be read, so these could not be checked. They are shown as saved —
            none of them is known to be missing.
          </Note>
        </div>
      )}

      {items.length === 0 ? (
        <p className="text-xs text-text-muted">
          Nothing set aside yet. “+ select” on a row below adds it here, and it stays while you
          change namespace.
        </p>
      ) : (
        <ul className="space-y-1.5">
          {items.map((item) => {
            const presence = presenceOf(check, item.label);
            return (
              <li
                key={item.label}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs font-mono"
              >
                <RowName label={item.label} gone={presence.state === 'gone'} />
                {/* cm-beetle's own word, passed through. Only its infra states
                    are a defined vocabulary; object storage and rdbms carry
                    whatever cb-tumblebug reports, so none of it is toned. */}
                {presence.state !== 'gone' && presence.status && (
                  <span className="text-text-muted">{presence.status}</span>
                )}
                {/* Removing the entry a wizard is using takes it off this list
                    only. That destination belongs to the wizard's draft. */}
                <Button variant="danger" className="ml-auto" onClick={() => remove(item.label)}>
                  remove
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

// ── The chooser, in wizard Step 2 ───────────────────────────────────────────

/**
 * What each kind is called in a sentence — singular and lower case, unlike the
 * browser's tab labels, which are titles ("VM Nodes", "Object Storage").
 */
const KIND_NOUN: Record<TargetKind, string> = {
  infra: 'VM node',
  objectStorage: 'object storage',
  rdbms: 'RDBMS',
};

/**
 * "RDBMS " for one kind, "VM node or object storage " for two — with the
 * trailing space, so the sentence reads without one when no kind is known.
 */
export function neededNoun(kinds: TargetKind[]): string {
  const nouns = kinds.map((k) => KIND_NOUN[k]).filter(Boolean);
  if (nouns.length === 0) return '';
  const list =
    nouns.length === 1
      ? nouns[0]
      : `${nouns.slice(0, -1).join(', ')} or ${nouns[nouns.length - 1]}`;
  return `${list} `;
}

/** What one entry reads as in the list: its label, plus whatever cm-beetle
 *  last said about it. An option is text and nothing else, so a state that is
 *  a badge and a strikethrough on the Connections screen has to be a word
 *  here. */
export function optionText(label: string, presence: TargetPresence): string {
  if (presence.state === 'gone') return `${label} — gone`;
  return presence.status ? `${label} — ${presence.status}` : label;
}

export function TargetChoice({
  kinds,
  onUse,
}: {
  /**
   * The kinds that can be a destination here. Without it the list would offer
   * an rdbms as the destination of a filesystem migration — a plan that cannot
   * be built.
   */
  kinds: TargetKind[];
  onUse: (sel: TargetSelection) => void;
}) {
  const stored = useTargetShortlist((s) => s.items);

  // An entry whose kind cannot be read is kept rather than hidden: the picker
  // writes only cm-beetle references, so this cannot arise from it, and dropping
  // someone's entry without saying so is the worse failure.
  const items = React.useMemo(
    () =>
      stored.filter((i) => {
        const kind = locatorOf(i.ref)?.kind;
        return kind === undefined || kinds.includes(kind);
      }),
    [stored, kinds],
  );

  const check = useTargetPresence(items.map((i) => ({ key: i.label, ref: i.ref })));

  if (items.length === 0) {
    // What is missing, named by kind. "None of the 2 set-aside targets can be a
    // destination" told the reader that their list did not fit without saying
    // what would, which left them to work out the rule from the empty screen.
    return <p className="text-xs text-text-muted">No {neededNoun(kinds)}target set aside.</p>;
  }

  return (
    <div className="space-y-3">
      {check.unreadable && (
        <Note tone="warn">
          cm-beetle could not be read, so these could not be checked. None of them is known to be
          missing.
        </Note>
      )}

      {/* Held at "select…", never at the answer. What was chosen is written
          under the field, which can say it in full — the destination may also
          have come from the inline mode, or from an entry since taken off this
          list, and a select can only ever show its own options. */}
      <Field label="target" required>
        <Select
          value=""
          onChange={(e) => {
            const sel = items.find((i) => i.label === e.target.value);
            // A gone entry still commits. One read that failed, or a resource
            // rebuilt under the same id, must not lock the reader out of their
            // own choice — the warning below the field says so instead.
            if (sel) onUse(sel);
          }}
        >
          <option value="">select…</option>
          {items.map((item) => (
            <option key={item.label} value={item.label}>
              {optionText(item.label, presenceOf(check, item.label))}
            </option>
          ))}
        </Select>
      </Field>
    </div>
  );
}
