'use client';

// A JSON document as a collapsible tree.
//
// Lazy, and deliberately so: a node's children exist only while it is open, so
// a collapsed multi-megabyte document costs almost nothing to draw. A database
// inspect with columns and indexes on runs to megabytes, so nothing is built
// that nobody has opened, and no depth cap is needed.
//
// Read-only. Nothing here edits, and no cycle guard is needed: the input is
// always a response axios has already parsed, and JSON cannot carry a cycle.

import React from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';

/**
 * How deep to open on first render.
 *
 * Three levels is what it takes to reach the payload of a refined scan —
 * sourceDataMigrationModel, then fileSystems, then its one element — so the
 * fields a reader came for are on screen without a click, and the listings
 * under them are not.
 */
const AUTO_OPEN_DEPTH = 3;

/** A container this big stays closed however shallow it sits. */
const AUTO_OPEN_MAX_ENTRIES = 20;

/**
 * Children drawn per container. A folder listing runs to thousands of entries,
 * and the rest are named rather than silently dropped.
 */
const CHILD_CAP = 100;

/** One indent step, in px. */
const INDENT = 14;

type Entries = Array<[string, unknown]>;

/**
 * The children of a container, or null for anything that has none.
 *
 * `undefined` is handled exactly as JSON.stringify handles it, so this view and
 * the raw one describe the same document: a property whose value is undefined
 * is dropped, while an undefined array element becomes null, because an array
 * cannot lose an element without renumbering the ones after it.
 *
 * It is not a hypothetical. A response reaches here after normalizeRefined,
 * which writes all three resource lists back with `p.fileSystems?.map(...)` —
 * an absent list becomes an own property set to undefined, so a filesystem scan
 * carried `objectStorages` and `databases` keys that the raw view, quite
 * rightly, never showed.
 */
function entriesOf(value: unknown): Entries | null {
  if (Array.isArray(value)) {
    return value.map((v, i) => [String(i), v === undefined ? null : v]);
  }
  // typeof null is "object", so the null check has to come first.
  if (value !== null && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).filter(
      ([, v]) => v !== undefined,
    );
  }
  return null;
}

/** What a closed container says about itself: how much is inside, and of which shape. */
function summaryOf(value: unknown, count: number): string {
  return Array.isArray(value) ? `[${count}]` : `{${count}}`;
}

/**
 * Values are coloured by type so a scan down the column separates them without
 * reading. Each is a light/dark pair: a bare 400 fails on the light panel and a
 * bare 600 is muddy on the dark one.
 */
function Primitive({ value }: { value: unknown }) {
  if (value === null) return <span className="text-text-muted italic">null</span>;
  switch (typeof value) {
    case 'string':
      return <span className="text-emerald-600 dark:text-emerald-400">&quot;{value}&quot;</span>;
    case 'number':
      return <span className="text-sky-600 dark:text-sky-400">{String(value)}</span>;
    case 'boolean':
      return <span className="text-violet-600 dark:text-violet-400">{String(value)}</span>;
    default:
      return <span className="text-text-muted">{String(value)}</span>;
  }
}

function Node({ name, value, depth }: { name: string; value: unknown; depth: number }) {
  const entries = entriesOf(value);
  // Computed once, on mount: reopening the view starts from the same place
  // every time rather than from wherever the last reader left it.
  const [open, setOpen] = React.useState(
    () =>
      entries !== null && depth < AUTO_OPEN_DEPTH && entries.length <= AUTO_OPEN_MAX_ENTRIES,
  );

  const indent = { paddingLeft: depth * INDENT + 4 };

  if (entries === null) {
    return (
      <div className="flex items-start gap-2 py-px" style={indent}>
        {/* Holds the column the chevrons occupy, so keys line up whether or
            not their siblings can be opened. */}
        <span className="w-3 shrink-0" />
        <span className="text-text-main shrink-0">{name}</span>
        <Primitive value={value} />
      </div>
    );
  }

  const shown = entries.slice(0, CHILD_CAP);
  const hidden = entries.length - shown.length;

  return (
    <>
      {/* The whole row toggles, not just the chevron — the same target every
          other accordion in this UI offers. */}
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 py-px text-left cursor-pointer rounded hover:bg-bg-panel"
        style={indent}
      >
        {open ? (
          <ChevronDown className="w-3 h-3 shrink-0 text-text-muted" />
        ) : (
          <ChevronRight className="w-3 h-3 shrink-0 text-text-muted" />
        )}
        <span className="text-text-main">{name}</span>
        <span className="text-text-muted">{summaryOf(value, entries.length)}</span>
      </button>

      {open &&
        shown.map(([k, v]) => <Node key={k} name={k} value={v} depth={depth + 1} />)}

      {open && hidden > 0 && (
        <div
          className="flex items-start gap-2 py-px text-text-muted"
          style={{ paddingLeft: (depth + 1) * INDENT + 4 }}
        >
          <span className="w-3 shrink-0" />
          <span>
            … {hidden} more — the <code>raw</code> view has all of them
          </span>
        </div>
      )}
    </>
  );
}

export function JsonTree({ value }: { value: unknown }) {
  const entries = entriesOf(value);

  return (
    <div className="max-h-96 overflow-auto rounded-lg bg-bg-input border border-border-input p-3 text-xs font-mono leading-relaxed">
      {/* The document's own top level is the first row: wrapping it in a
          synthetic "root" would cost a level of indent and say nothing. */}
      {entries === null ? (
        <Node name="value" value={value} depth={0} />
      ) : (
        entries
          .slice(0, CHILD_CAP)
          .map(([k, v]) => <Node key={k} name={k} value={v} depth={0} />)
      )}
    </div>
  );
}
