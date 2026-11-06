'use client';

// The scan's units as the hierarchy they already are.
//
// cm-honeybee reports them flat — a list of absolute paths for a filesystem,
// of "bucket/prefix/" keys for object storage — but both are trees that were
// only ever flattened for transport. Drawn flat they cost a reader a scroll
// through hundreds of rows to find one branch, and they mislead: a folder's
// fileCount and fileSize are DIRECT, non-recursive (data-mig's FSEntry says
// so), which reads as wrong when a scan root showing "1 files" sits in the same
// column as a subfolder showing 412. Nested, "direct" is self-evident.
//
// Database names have no hierarchy, so there the tree is one level deep and
// draws what the flat table used to.

import React from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import type { SourceUnit } from '@/components/domains/adapters';

/** Levels opened on first render, counting the scan's own roots as 0. */
const AUTO_OPEN_DEPTH = 2;

/** A node with more children than this stays closed however shallow it sits. */
const AUTO_OPEN_MAX_CHILDREN = 20;

/** Children drawn per node. The rest are named, not drawn. */
const CHILD_CAP = 100;

/** One indent step, in px. */
const INDENT = 16;

export interface UnitNode {
  /** The full name the scan reported — what Step 3 selects by. */
  name: string;
  /** What the row shows: this name with its parent's part removed. */
  label: string;
  detail: string;
  children: UnitNode[];
}

/**
 * The nearest listed ancestor of `name`, walking its own path upwards.
 *
 * Both separators are tried at each step because object storage keys carry a
 * trailing slash and filesystem paths do not.
 */
function parentOf(name: string, has: (n: string) => boolean): string | null {
  let from = name.endsWith('/') ? name.length - 2 : name.length - 1;
  let cut = name.lastIndexOf('/', from);
  while (cut > 0) {
    if (has(name.slice(0, cut))) return name.slice(0, cut);
    if (has(name.slice(0, cut + 1))) return name.slice(0, cut + 1);
    from = cut - 1;
    cut = name.lastIndexOf('/', from);
  }
  // "/" is a parent of everything below it and has no slash of its own to find.
  return name !== '/' && has('/') ? '/' : null;
}

/**
 * Builds the tree from the flat listing.
 *
 * A unit whose parent is not in the listing — max_depth or a filter can leave
 * that gap — attaches to its nearest listed ancestor instead, under a label
 * carrying the missing segments ("logs/2024"). The alternative, inventing the
 * absent folder as an empty row, would put something on this screen that the
 * scan never reported, which is the one thing a scan result must not do.
 */
export function buildUnitTree(units: SourceUnit[]): UnitNode[] {
  const byName = new Map<string, UnitNode>();
  for (const u of units) {
    byName.set(u.name, { name: u.name, label: u.name, detail: u.detail, children: [] });
  }
  const has = (n: string) => byName.has(n);

  const roots: UnitNode[] = [];
  // Insertion order is the listing's order, which is the order to draw in.
  for (const u of units) {
    const node = byName.get(u.name)!;

    // A listing that states its own nesting is taken at its word. Only a
    // listing of paths has its parents derived, because only there is the name
    // a path — a database or table name can contain a slash of its own, and
    // parsing one would move the row somewhere it does not belong.
    if (u.parent !== undefined) {
      if (u.label) node.label = u.label;
      const parent = byName.get(u.parent);
      if (parent === undefined) {
        roots.push(node);
        continue;
      }
      parent.children.push(node);
      continue;
    }

    const parent = parentOf(u.name, has);
    if (parent === null) {
      roots.push(node);
      continue;
    }
    node.label = u.name.slice(parent.endsWith('/') ? parent.length : parent.length + 1);
    byName.get(parent)!.children.push(node);
  }
  return roots;
}

/**
 * One connection's part of the tree: a root row, and the scan's units under it.
 *
 * A source group is scanned one connection at a time and every host can hold
 * a "/data" of its own, so the units are never merged across connections —
 * each hangs under the connection it was read from.
 */
export interface UnitGroup {
  connId: string;
  /** The connection's name. */
  label: string;
  /** Address and scan root, beside the name. */
  meta?: string;
  /** The scan-wide totals, in the detail column. */
  detail: string;
  /** Whether this connection's scan can be used, and why not. */
  status?: { tone: 'ok' | 'warn' | 'bad' | 'neutral'; text: string; title?: string };
  /** A control for the row — inspect, retry. */
  action?: React.ReactNode;
  units: SourceUnit[];
}

/**
 * The key a node is opened and drawn under.
 *
 * The unit name alone is not unique once there are several connections: two
 * hosts each with a "/data" would open and close together.
 */
const nodeKey = (connId: string, name: string) => `${connId}\0${name}`;

/** A connection's own row, opened and closed like any other node. */
const rootKey = (connId: string) => nodeKey(connId, '');

function initialOpen(
  connId: string,
  nodes: UnitNode[],
  maxDepth: number,
  depth = 0,
  acc = new Set<string>(),
): Set<string> {
  for (const n of nodes) {
    if (n.children.length > 0 && depth < maxDepth && n.children.length <= AUTO_OPEN_MAX_CHILDREN) {
      acc.add(nodeKey(connId, n.name));
      initialOpen(connId, n.children, maxDepth, depth + 1, acc);
    }
  }
  return acc;
}

type Row =
  | { kind: 'group'; group: UnitGroup; count: number }
  | { kind: 'node'; key: string; node: UnitNode; depth: number }
  | { kind: 'more'; key: string; hidden: number; depth: number };

/**
 * The rows currently on screen.
 *
 * Only open nodes are walked into, so a collapsed tree of thousands of folders
 * produces a handful of rows — which is what replaces the flat table's row cap.
 * A single node with thousands of direct children still needs one, hence the
 * "more" row.
 */
function visibleRows(
  connId: string,
  nodes: UnitNode[],
  open: Set<string>,
  depth: number,
  out: Row[],
): void {
  for (const node of nodes) {
    const key = nodeKey(connId, node.name);
    out.push({ kind: 'node', key, node, depth });
    if (node.children.length === 0 || !open.has(key)) continue;
    visibleRows(connId, node.children.slice(0, CHILD_CAP), open, depth + 1, out);
    if (node.children.length > CHILD_CAP) {
      out.push({
        kind: 'more',
        key: `more:${key}`,
        hidden: node.children.length - CHILD_CAP,
        depth: depth + 1,
      });
    }
  }
}

const STATUS_TONE = {
  ok: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  bad: 'text-rose-600 dark:text-rose-400',
  neutral: 'text-text-muted',
} as const;

function Chevron({ open }: { open: boolean }) {
  return open ? (
    <ChevronDown className="w-3 h-3 shrink-0 text-text-muted" />
  ) : (
    <ChevronRight className="w-3 h-3 shrink-0 text-text-muted" />
  );
}

// Built once per units array, not per render: the caller hands a fresh list of
// groups on every render, while a connection's units change only when its scan
// does. Module-level rather than a ref, so render never touches ref.current;
// buildUnitTree is pure, so instances can share it.
const BUILT = new WeakMap<SourceUnit[], UnitNode[]>();

export function UnitTree({ groups, unitLabel }: { groups: UnitGroup[]; unitLabel: string }) {
  const trees = React.useMemo(
    () =>
      groups.map((g) => {
        let roots = BUILT.get(g.units);
        if (!roots) {
          roots = buildUnitTree(g.units);
          BUILT.set(g.units, roots);
        }
        return { group: g, roots };
      }),
    [groups],
  );

  // One connection opens as the tree always has. Several open their own rows
  // and one level less below them, so a group of hosts starts as a list of
  // hosts rather than a page of folders.
  const folderDepth = groups.length > 1 ? AUTO_OPEN_DEPTH - 1 : AUTO_OPEN_DEPTH;

  const [open, setOpen] = React.useState(() => new Set<string>());
  // A connection's scan can arrive after the tree is first drawn — a group is
  // inspected one connection at a time — so the initial opening is applied to
  // each connection the first time it has units, not once for the whole tree.
  // After that the reader's own opening and closing is left alone.
  const seeded = React.useRef(new Set<string>());
  React.useEffect(() => {
    const fresh = trees.filter((t) => t.roots.length > 0 && !seeded.current.has(t.group.connId));
    if (fresh.length === 0) return;
    setOpen((cur) => {
      const next = new Set(cur);
      for (const t of fresh) {
        seeded.current.add(t.group.connId);
        next.add(rootKey(t.group.connId));
        initialOpen(t.group.connId, t.roots, folderDepth, 0, next);
      }
      return next;
    });
  }, [trees, folderDepth]);

  const toggle = (key: string) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const rows: Row[] = [];
  for (const { group, roots } of trees) {
    rows.push({ kind: 'group', group, count: roots.length });
    if (open.has(rootKey(group.connId))) visibleRows(group.connId, roots, open, 1, rows);
  }

  return (
    // A table rather than nested divs: the detail column has to line up across
    // depths, which only a shared column width gives.
    <div className="max-h-64 overflow-auto">
      <table className="w-full text-xs font-mono">
        <thead>
          <tr className="text-text-muted border-b border-border-main text-left">
            <th className="font-bold py-1">{unitLabel}</th>
            <th className="font-bold py-1 text-right pl-4 pr-3">detail</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            if (row.kind === 'group') {
              const g = row.group;
              const key = rootKey(g.connId);
              return (
                <tr key={key} className="border-b border-border-main/50 last:border-0">
                  <td className="py-1">
                    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
                      {row.count > 0 ? (
                        <button
                          type="button"
                          aria-expanded={open.has(key)}
                          onClick={() => toggle(key)}
                          className="inline-flex items-center gap-1 text-left font-bold cursor-pointer hover:text-indigo-600 dark:hover:text-indigo-400"
                        >
                          <Chevron open={open.has(key)} />
                          {g.label}
                        </button>
                      ) : (
                        <span className="inline-flex items-center gap-1 font-bold">
                          <span className="w-3 shrink-0" />
                          {g.label}
                        </span>
                      )}
                      {g.meta && <span className="text-text-muted">{g.meta}</span>}
                      {g.status && (
                        <span className={STATUS_TONE[g.status.tone]} title={g.status.title}>
                          {g.status.text}
                        </span>
                      )}
                      {g.action}
                    </span>
                  </td>
                  <td className="py-1 text-right text-text-muted whitespace-nowrap pl-4 pr-3 align-top">
                    {/* Recursive over the whole scan, unlike the rows below. */}
                    <span title="Counted over the whole scan, recursively. A row below counts only what sits directly in it.">
                      {g.detail}
                    </span>
                  </td>
                </tr>
              );
            }
            if (row.kind === 'more') {
              return (
                <tr key={row.key}>
                  <td className="py-1 text-text-muted" style={{ paddingLeft: row.depth * INDENT }}>
                    … {row.hidden} more — the <code>raw</code> view has all of them
                  </td>
                  <td />
                </tr>
              );
            }
            return (
              <tr key={row.key} className="border-b border-border-main/50 last:border-0">
                <td className="py-1" style={{ paddingLeft: row.depth * INDENT }}>
                  {row.node.children.length > 0 ? (
                    <button
                      type="button"
                      aria-expanded={open.has(row.key)}
                      onClick={() => toggle(row.key)}
                      title={row.node.name}
                      className="inline-flex items-center gap-1 text-left cursor-pointer hover:text-indigo-600 dark:hover:text-indigo-400"
                    >
                      <Chevron open={open.has(row.key)} />
                      {row.node.label}
                      <span className="text-text-muted">({row.node.children.length})</span>
                    </button>
                  ) : (
                    // Holds the column the chevrons occupy, so leaves line up
                    // with the branches beside them.
                    <span className="inline-flex items-center gap-1" title={row.node.name}>
                      <span className="w-3 shrink-0" />
                      {row.node.label}
                    </span>
                  )}
                </td>
                <td className="py-1 text-right text-text-muted whitespace-nowrap pl-4 pr-3">
                  {row.node.detail || '—'}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
