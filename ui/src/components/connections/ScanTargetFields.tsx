'use client';

// What a connection collects, as opposed to how it is reached.
//
// The scan target is a property of the connection rather than of the import
// request: one path or one bucket per connection, matching the single saved
// result a connection holds. A group-level import therefore walks each
// connection's own target instead of applying one target to every host.
//
// Split out of ConnectionFields.tsx when that file went over the 400-line cap.
// The seam is the one the fields already had: everything left there says how
// to reach the source, everything here says what to read from it.

import React from 'react';
import { Field, TextInput } from '@/components/ui/primitives';
import { isVisible, type FieldMap } from '@/lib/sourceFormSchema';
import type { ConnectionDraft } from './connectionDraft';
import type { Category } from '@/lib/connectionCategory';

interface Props {
  category: Category;
  provider?: string;
  rules: FieldMap;
  draft: ConnectionDraft;
  onChange: (patch: Partial<ConnectionDraft>) => void;
}

/**
 * Renders nothing for the database category. A db connection has a scan target
 * too — db_name, and db_pg_schema under it — but those already sit among the db
 * fields, where a reader meets them next to the server they apply to.
 */
export function ScanTargetFields({ category, provider, rules, draft, onChange }: Props) {
  if (!isVisible(rules.fs_scan_path) && !isVisible(rules.os_scan_bucket)) return null;

  return (
    <div className="space-y-3 border-t border-border-main pt-4">
      <h5 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
        Scan target
      </h5>

      {category === 'filesystem' && (
        <Field
          label="fs_scan_path"
          // The field is optional on the wire: honeybee fills an empty one with
          // "/home". The hint says which path that is rather than the
          // placeholder pre-filling it — a default stated as a value in the box
          // reads as a choice someone made for this connection.
          hint={'absolute path · empty registers "/home"'}
        >
          <TextInput
            value={draft.fs_scan_path ?? ''}
            onChange={(e) => onChange({ fs_scan_path: e.target.value })}
            placeholder="ex) /testdata"
          />
        </Field>
      )}

      {category === 'objectstorage' && (
        <div className="grid md:grid-cols-2 gap-3">
          <Field
            label="os_scan_bucket"
            required
            hint={
              provider === 'tencent'
                ? 'tencent — include the AppId suffix'
                : 'one bucket per connection'
            }
          >
            <TextInput
              value={draft.os_scan_bucket ?? ''}
              onChange={(e) => onChange({ os_scan_bucket: e.target.value })}
              placeholder={provider === 'tencent' ? 'ex) images-1250000000' : 'ex) images'}
            />
          </Field>
          <Field label="os_scan_prefix" hint="empty = the whole bucket">
            <TextInput
              value={draft.os_scan_prefix ?? ''}
              onChange={(e) => onChange({ os_scan_prefix: e.target.value })}
              placeholder="ex) photos/2024/"
            />
          </Field>
        </div>
      )}
    </div>
  );
}
