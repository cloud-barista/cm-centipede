'use client';

// One reading of a plan, drawn the same way wherever a plan is shown: the
// wizard's Step 4, where it has just been built, and a run card, where it is
// what a run was started from.
//
// These were two components. Step 4 drew a table per resource model; the run
// card drew one line per model with every mapping comma-joined into it, so the
// same plan looked like two different objects, and the run card's line could not
// say which mapping carried filter rules — the one thing that decides whether
// what was migrated is what the source holds.
//
// Each domain names its own columns. A folder path, a bucket and a database name
// are not the same kind of name, and one "source"/"target" pair over all three
// said less than the header had room for.

import React from 'react';
import { Panel } from '@/components/ui/primitives';
import { unwrapPlan } from '@/lib/planEnvelope';
import type { ConnectionRef, PgSchemaMapping } from '@/types/common';
import { dstKey } from '@/lib/refKey';
import type { TargetDataMigrationModel } from '@/types/target';

interface Row {
  order: number;
  src: string;
  dst: string;
  /** The one domain-specific cell, if that domain has one. */
  extra?: string;
  rules: number;
}

/**
 * Which connection an entry reads from or writes to. A plan over a source group
 * holds one entry per host, and "honeybee → beetleSsh" on each of them said
 * nothing about which was which: the source is named where the caller knows the
 * name, and every ref is otherwise spelled out by its identity.
 */
function refText(ref: ConnectionRef, names?: Record<string, string>): string {
  const id = ref.honeybee?.connectionId;
  return id && names?.[id] ? `honeybee:${names[id]}` : dstKey(ref);
}

export function PlanTables({
  plan,
  names,
}: {
  plan: TargetDataMigrationModel;
  /** Source connection names by honeybee connection id, where the caller has them. */
  names?: Record<string, string>;
}) {
  const p = unwrapPlan(plan);

  return (
    <div className="space-y-3">
      {(p.fileSystems ?? []).map((m, i) => (
        <Panel key={`fs-${i}`} className="panel-inset text-xs font-mono">
          <PlanHeader
            title={`File systems (${m.folders?.length ?? 0})`}
            src={refText(m.srcConnection, names)}
            dst={refText(m.dstConnection, names)}
            extra={[m.dstType && `dstType ${m.dstType}`, m.strategy && `strategy ${m.strategy}`]}
          />
          <PlanRows
            srcLabel="source path"
            dstLabel="target path"
            rows={(m.folders ?? []).map((f) => ({
              order: f.order,
              src: f.srcPath,
              dst: f.dstPath,
              rules: f.rules?.length ?? 0,
            }))}
          />
        </Panel>
      ))}

      {(p.objectStorages ?? []).map((m, i) => (
        <Panel key={`os-${i}`} className="panel-inset text-xs font-mono">
          <PlanHeader
            title={`Object storages (${m.buckets?.length ?? 0})`}
            src={refText(m.srcConnection, names)}
            dst={refText(m.dstConnection, names)}
            extra={[m.dstType && `dstType ${m.dstType}`, m.strategy && `strategy ${m.strategy}`]}
          />
          <PlanRows
            srcLabel="source bucket"
            dstLabel="target bucket"
            rows={(m.buckets ?? []).map((b) => ({
              order: b.order,
              src: b.srcPath,
              dst: b.dstPath,
              rules: b.rules?.length ?? 0,
            }))}
          />
        </Panel>
      ))}

      {(p.databases ?? []).map((m, i) => {
        const rows: Row[] = m.databases.map((d) => ({
          order: d.order,
          src: d.srcName,
          dst: d.dstName,
          extra: schemaText(d.pgSchemas),
          rules: d.rules?.length ?? 0,
        }));
        return (
          <Panel key={`db-${i}`} className="panel-inset text-xs font-mono">
            <PlanHeader
              title={`Databases (${m.databases.length})`}
              src={refText(m.srcConnection, names)}
              dst={refText(m.dstConnection, names)}
              extra={[`engine ${m.dbType}`]}
            />
            {/* The schemas column appears only where there are schemas to put in
                it: PostgreSQL is the one engine with a level between the database
                and the table, and an always-present empty column on MySQL would
                claim otherwise. */}
            <PlanRows
              srcLabel="source database"
              dstLabel="target database"
              extraLabel={rows.some((r) => r.extra) ? 'schemas' : undefined}
              rows={rows}
            />
          </Panel>
        );
      })}
    </div>
  );
}

/**
 * The schema mappings of one database, on one line. A schema that keeps its name
 * is written once rather than as `public → public`: the arrow is what says a
 * rename happened, so spending it on a pair that is not one hides the ones that
 * are.
 */
function schemaText(pgSchemas?: PgSchemaMapping[]): string {
  if (!pgSchemas?.length) return '';
  return pgSchemas
    .map((s) => (s.dstName && s.dstName !== s.srcName ? `${s.srcName} → ${s.dstName}` : s.srcName))
    .join(', ');
}

function PlanHeader({
  title,
  src,
  dst,
  extra,
}: {
  title: string;
  src: string;
  dst: string;
  extra: Array<string | false | undefined>;
}) {
  return (
    <div className="mb-2">
      <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted mb-1">
        {title}
      </h4>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-text-muted">
        <span>srcConnection {src}</span>
        <span>dstConnection {dst}</span>
        {extra.filter(Boolean).map((e) => (
          <span key={String(e)}>{e}</span>
        ))}
      </div>
    </div>
  );
}

function PlanRows({
  srcLabel,
  dstLabel,
  extraLabel,
  rows,
}: {
  srcLabel: string;
  dstLabel: string;
  extraLabel?: string;
  rows: Row[];
}) {
  return (
    <table className="w-full table-fixed">
      <thead>
        <tr className="text-text-muted border-b border-border-main">
          <th className="text-left font-bold py-1 w-14">order</th>
          <th className="text-left font-bold py-1">{srcLabel}</th>
          <th className="text-left font-bold py-1">{dstLabel}</th>
          {extraLabel && <th className="text-left font-bold py-1 w-1/4">{extraLabel}</th>}
          <th className="text-right font-bold py-1 w-16">rules</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i} className="border-b border-border-main/50 last:border-0">
            <td className="py-1">{r.order}</td>
            {/* title, because a truncated path is unreadable and these are the
                two cells that decide what a run moved where. */}
            <td className="py-1 truncate" title={r.src}>
              {r.src}
            </td>
            <td className="py-1 truncate" title={r.dst}>
              {r.dst}
            </td>
            {extraLabel && (
              <td className="py-1 truncate text-text-muted" title={r.extra}>
                {r.extra}
              </td>
            )}
            <td className="py-1 text-right">{r.rules}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
