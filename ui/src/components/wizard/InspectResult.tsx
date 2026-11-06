'use client';

// Three ways to read a scan, none of which replaces the others.
//
//   folders  the units the next steps work in, as the hierarchy they already
//            are, with each one's own figures. The only view that knows this
//            domain; its switch label follows the unit — folders, buckets or
//            databases.
//   json     the response POST /plans/target receives untouched,
//            with its structure navigable rather than scrolled past.
//   raw      the same document as text: the one a reader can select, copy, and
//            search with the browser's own find, which neither tree can offer
//            for what is currently collapsed.
//
// So the choice is the reader's, rather than one of them being the "extra".

import React from 'react';
import { Button } from '@/components/ui/primitives';
import { JsonTree } from '@/components/ui/JsonTree';
import { UnitTree, type UnitGroup } from './UnitTree';
import type { SourceDataMigrationModel } from '@/types/source';
import type { InspectResultView } from '@/store/uiStore';

const VIEWS: InspectResultView[] = ['folders', 'json', 'raw'];

export function ViewSwitch({
  view,
  unitLabel,
  onChange,
}: {
  view: InspectResultView;
  /** Folder | Bucket | Database — what one unit is in this domain. */
  unitLabel: string;
  onChange: (v: InspectResultView) => void;
}) {
  return (
    <span className="flex items-center gap-1">
      {VIEWS.map((v) => (
        <Button
          key={v}
          variant={view === v ? 'primary' : 'ghost'}
          onClick={() => onChange(v)}
          aria-pressed={view === v}
        >
          {/* "folders" is the stored id; the button says what this domain's
              units actually are, so the Database wizard does not offer a
              button labelled folders. */}
          {v === 'folders' ? `${unitLabel.toLowerCase()}s` : v}
        </Button>
      ))}
    </span>
  );
}

export function InspectResult({
  view,
  groups,
  unitLabel,
  model,
}: {
  view: InspectResultView;
  /** One per connection in scope, each with its own units. */
  groups: UnitGroup[];
  /** What one row is in this domain — Folder, Bucket or Database. */
  unitLabel: string;
  model: SourceDataMigrationModel | undefined;
}) {
  // Both JSON views are verbatim, unlike Step 4's plan viewer, which masks.
  // The difference is in what the two responses carry: a plan is built against
  // a destination and brings its credentials back, while a refined scan fills
  // only connection.honeybee.connectionID and leaves every credential-bearing
  // arm of ConnectionRef omitempty (doGetFSInfoRefined and its two siblings in
  // cm-honeybee). There is nothing here to mask, and masking what is about to
  // be POSTed to /plans/target would make these a worse copy of the document
  // they exist to show.
  //
  // If a producer ever fills connection.ssh, .db or .minio on a refined
  // response, these are the lines that have to change.
  if (view === 'json') return <JsonTree value={model} />;

  if (view === 'raw') {
    return (
      <pre className="max-h-96 overflow-auto rounded-lg bg-bg-input border border-border-input p-3 text-2xs leading-relaxed">
        {JSON.stringify(model, null, 2)}
      </pre>
    );
  }

  // Anything else is the unit tree, which is also where an unrecognised stored
  // view lands.
  return <UnitTree groups={groups} unitLabel={unitLabel} />;
}
