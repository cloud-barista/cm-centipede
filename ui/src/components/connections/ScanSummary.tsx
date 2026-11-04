'use client';

// What the last inspect left behind, per domain.
//
// Every metric is optional: cm-honeybee only fills the ones the inspect asked
// for, because each costs a full walk of the source. A missing
// metric is therefore a fact about how the scan was run, not an error — the
// entry is simply left out rather than shown as a blank or a zero.

import React from 'react';
import { formatBytes, formatCount } from '@/lib/format';
import type { Category } from '@/lib/connectionCategory';
import type { SourceDataMigrationModel } from '@/types/source';

/** One label/value pair. The label is muted, the value is not (7.3 rule 4). */
function Fact({
  label,
  children,
  title,
}: {
  label?: string;
  children: React.ReactNode;
  title?: string;
}) {
  return (
    <span className="whitespace-nowrap" title={title}>
      {label && <span className="text-text-muted">{label} </span>}
      <span className="font-mono">{children}</span>
    </span>
  );
}

// Both domains count their scan root in this list, so the two read the same way.
const FS_FOLDERS_TITLE =
  'Directories found under the scan root, the scan root itself included.';
const OS_FOLDERS_TITLE =
  'Folder-like key prefixes found in the bucket, the scan root itself included. ' +
  'Object storage has no real directories — these are derived from object keys, ' +
  'so a nested layout yields more of them than there are objects.';

/** The busiest extensions, as "ext count" pairs. */
function topExtensions(counts: Record<string, number> | undefined, limit = 3): string | null {
  if (!counts) return null;
  const entries = Object.entries(counts).sort((a, b) => b[1] - a[1]).slice(0, limit);
  if (entries.length === 0) return null;
  return entries.map(([ext, n]) => `${ext} ${formatCount(n)}`).join(' · ');
}

export function ScanSummary({
  category,
  model,
}: {
  category: Category;
  model: SourceDataMigrationModel | undefined;
}) {
  const facts = buildFacts(category, model);

  return (
    <div className="text-xs flex flex-wrap items-baseline gap-x-4 gap-y-1">
      <span className="text-text-muted">last scan</span>
      {facts.length === 0 ? (
        <span className="text-text-muted">never scanned</span>
      ) : (
        facts
      )}
    </div>
  );
}

function buildFacts(
  category: Category,
  model: SourceDataMigrationModel | undefined,
): React.ReactNode[] {
  const p = model?.sourceDataMigrationModel;
  if (!p) return [];
  const out: React.ReactNode[] = [];

  if (category === 'filesystem' && p.fileSystems?.length) {
    const fs = p.fileSystems[0];
    out.push(<Fact key="root">{fs.path}</Fact>);
    if (fs.fileCount !== undefined) {
      out.push(<Fact key="files" label="files">{formatCount(fs.fileCount)}</Fact>);
    }
    if (fs.totalSize !== undefined) {
      out.push(<Fact key="size" label="size">{formatBytes(fs.totalSize)}</Fact>);
    }
    if (fs.folders?.length) {
      out.push(
        <Fact key="folders" label="folders" title={FS_FOLDERS_TITLE}>
          {formatCount(fs.folders.length)}
        </Fact>,
      );
    }
    const ext = topExtensions(fs.extensionCounts);
    if (ext) out.push(<Fact key="types" label="types">{ext}</Fact>);
    return out;
  }

  if (category === 'objectstorage' && p.objectStorages?.length) {
    const os = p.objectStorages[0];
    out.push(<Fact key="root">{os.path}</Fact>);
    if (os.objectCount !== undefined) {
      out.push(<Fact key="objects" label="objects">{formatCount(os.objectCount)}</Fact>);
    }
    if (os.totalSize !== undefined) {
      out.push(<Fact key="size" label="size">{formatBytes(os.totalSize)}</Fact>);
    }
    if (os.folders?.length) {
      out.push(
        <Fact key="folders" label="folders" title={OS_FOLDERS_TITLE}>
          {formatCount(os.folders.length)}
        </Fact>,
      );
    }
    const ext = topExtensions(os.extensionCounts);
    if (ext) out.push(<Fact key="types" label="types">{ext}</Fact>);
    return out;
  }

  if (category === 'database' && p.databases?.length) {
    const server = p.databases[0];
    const dbs = server.databases ?? [];
    if (dbs.length === 0) return out;

    // One server version for the whole connection — it is a property of the
    // server, not of each database.
    if (dbs[0].serverVersion) {
      out.push(<Fact key="server" label="server">{dbs[0].serverVersion}</Fact>);
    }
    out.push(
      <Fact key="count" label="databases">
        {formatCount(dbs.length)}
      </Fact>,
    );

    const shown = dbs.slice(0, 4);
    for (const d of shown) {
      const detail = [
        d.tableCount ? `${formatCount(d.tableCount)} tables` : null,
        d.totalSize ? formatBytes(d.totalSize) : null,
      ]
        .filter(Boolean)
        .join(', ');
      out.push(
        <Fact key={d.database}>
          {d.database}
          {detail && <span className="text-text-muted"> ({detail})</span>}
        </Fact>,
      );
    }
    if (dbs.length > shown.length) {
      out.push(
        <span key="more" className="text-text-muted">
          +{dbs.length - shown.length} more
        </span>,
      );
    }
    return out;
  }

  return out;
}
