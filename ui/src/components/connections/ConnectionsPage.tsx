'use client';

// Three kinds of connection live here, and the difference decides the shape of
// each section:
//   1. Sources — cm-honeybee owns them: full CRUD.
//   2. Targets — cm-beetle owns them: a read-only picker, never a CRUD screen.
//   3. Inline  — nobody owns them: a session-only form, never a stored list.

import React from 'react';
import { useSourceGroups } from '@/queries/honeybee';
import { ErrorNote, Loading, PageHeader, SectionBand } from '@/components/ui/primitives';
import { rollUpSupported } from '@/lib/connectionCategory';
import { SourceSection } from './SourceSection';
import { StatusRollup } from './StatusRollup';
import { TargetSection } from './TargetSection';
import { InlineSection } from './InlineSection';

export function ConnectionsPage() {
  const groups = useSourceGroups();
  const all = groups.data?.source_group ?? [];

  // Rolled up over supported groups only. Using the response's own top-level
  // count would include the csp groups this UI hides, and the header would then
  // disagree with the list below it.
  const roll = rollUpSupported(all);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Connections"
        subtitle={groups.isPending ? 'loading…' : <StatusRollup count={roll} />}
      />

      <ErrorNote error={groups.error} />

      {groups.isPending ? <Loading /> : <SourceSection groups={all} />}

      <SectionBand title="2. Targets" hint="cm-beetle · read-only" />
      <TargetSection />

      <InlineSection />
    </div>
  );
}
