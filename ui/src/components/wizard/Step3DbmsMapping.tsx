'use client';

// Listing any database narrows the run to the ones listed, so selection and
// mapping are the same control here (dmdl DBMSFilter).

import React from 'react';
import { Checkbox, Note, TextInput } from '@/components/ui/primitives';
import { DbmsRuleEditor } from '@/components/filter/DbmsRuleEditor';
import { PgSchemaMapper } from '@/components/filter/PgSchemaMapper';
import type { SourceUnit } from '@/components/domains/adapters';
import type { SourceDBModel } from '@/types/source';
import type { DbEntryDraft, MappingDraft } from '@/store/wizardStore';

/**
 * One unselected entry per database the scan found — what a connection's
 * mapping starts from before anyone has chosen.
 *
 * Databases only. The listing also carries their schemas and tables — Step 1
 * draws them as the rows under each database — and a migration is composed of
 * databases, so an entry per table would put names in the plan that the
 * dbmsFilter has no field for.
 */
export function seedDbEntries(units: SourceUnit[]): DbEntryDraft[] {
  return units
    .filter((u) => u.parent === undefined)
    .map((u) => ({ srcName: u.name, dstName: u.name, selected: false, rules: [], pgSchemas: [] }));
}

/** One source connection's databases: which move, under what name, minus what. */
export function Step3DbmsMapping({
  server,
  dbType,
  units,
  mapping,
  onChange,
}: {
  server: SourceDBModel | undefined;
  /** The engine cm-honeybee records on the connection (db_type). */
  dbType?: string;
  units: SourceUnit[];
  mapping: MappingDraft;
  onChange: (patch: Partial<MappingDraft>) => void;
}) {
  const dbEntries = mapping.dbEntries;

  // Schema mapping is PostgreSQL-only. The engine is read from the source
  // connection where it is stated, and otherwise inferred from the scan: only
  // PostgreSQL reports a pgSchema list.
  const isPostgres =
    server?.connection.db?.dbmsType === 'postgresql' ||
    !!server?.databases?.some((d) => (d.pgSchema?.length ?? 0) > 0);
  // MongoDB takes a row_exclude match as a query document instead of SQL. A
  // honeybee-backed source carries no connection.db, so the engine comes from
  // the honeybee connection's db_type.
  const isMongo = (server?.connection.db?.dbmsType ?? dbType) === 'mongodb';

  const patchEntry = (srcName: string, p: Partial<DbEntryDraft>) =>
    onChange({ dbEntries: dbEntries.map((e) => (e.srcName === srcName ? { ...e, ...p } : e)) });

  if (dbEntries.length === 0) {
    return <Note tone="warn">The scan reported no database. Run the inspect in Step 1 first.</Note>;
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-2">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Databases
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        <span className="text-xs text-text-muted">
          listing any database narrows the run
        </span>
      </div>

      <ul className="space-y-4">
        {dbEntries.map((entry) => {
          const unit = units.find((u) => u.name === entry.srcName);
          return (
            <li key={entry.srcName} className="panel p-4">
              <div className="flex flex-wrap items-center gap-3">
                <Checkbox
                  checked={entry.selected}
                  onChange={(v) => patchEntry(entry.srcName, { selected: v })}
                  label={entry.srcName}
                  hint={unit?.detail}
                  // Shares a centred row with the arrow and the rename box, so
                  // a hint on its own line would leave this column taller than
                  // the rest of the row. The detail is short — "12 tables ·
                  // 3.4 MB · 8.0.35".
                  inlineHint
                />
                <span className="text-text-muted">→</span>
                <TextInput
                  value={entry.dstName}
                  onChange={(e) => patchEntry(entry.srcName, { dstName: e.target.value })}
                  className="w-56 !py-1"
                  placeholder="(same name)"
                  disabled={!entry.selected}
                />
              </div>

              {entry.selected && (
                <div className="mt-4 space-y-4 border-t border-border-main pt-4">
                  <DbmsRuleEditor
                    rules={entry.rules}
                    onChange={(rules) => patchEntry(entry.srcName, { rules })}
                    isMongo={isMongo}
                  />
                  {isPostgres && (
                    <PgSchemaMapper
                      mappings={entry.pgSchemas}
                      onChange={(pgSchemas) => patchEntry(entry.srcName, { pgSchemas })}
                    />
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>

      <Note>
        A <code>row_exclude</code> rule needs direct access; its match is a SQL predicate, or a
        query document on MongoDB. Schema
        mappings are PostgreSQL-only — the plan layer rejects them elsewhere.
      </Note>
    </div>
  );
}
