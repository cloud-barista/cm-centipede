'use client';

import React from 'react';
import { Button, EmptyState, SectionBand } from '@/components/ui/primitives';
import { GroupCard } from './GroupCard';
import { NewSourceForm } from './NewSourceForm';
import { isSupportedGroup } from '@/lib/connectionCategory';
import { useAnchoredAccordion } from '@/hooks/useAnchoredAccordion';
import type { SourceGroupRes } from '@/types/honeybee';

/**
 * Only three group types reach this list. type=csp is not created, not listed
 * and not offered as a wizard source.
 */
export function SourceSection({ groups }: { groups: SourceGroupRes[] }) {
  const supported = groups.filter(isSupportedGroup);
  const [creating, setCreating] = React.useState(false);

  // One group open at a time, as everywhere else in this UI. It also settles
  // the polling question: an expanded group polls its
  // connections, so exclusivity holds that at exactly one group's worth
  // instead of growing with every card the reader opens.
  const [expandedId, setExpandedId] = React.useState<string | null>(null);

  // Opening a card below the open one collapses that one, which would otherwise
  // pull the clicked card up and out from under the cursor.
  const { register, anchorTo } = useAnchoredAccordion(expandedId);

  // The add form and an expanded group are the same thing to a reader — both
  // wear .panel-open, both are "the panel you are working in" — so the list
  // holds one of them, not one of each.
  const startCreating = () => {
    setCreating(true);
    setExpandedId(null);
  };

  const toggle = (id: string) => {
    anchorTo(id);
    setCreating(false);
    setExpandedId((cur) => (cur === id ? null : id));
  };

  return (
    <>
      <SectionBand
        title="1. Sources"
        hint="honeybee"
        right={
          <Button variant="primary" onClick={startCreating}>
            + New Source
          </Button>
        }
      />

      {creating && <NewSourceForm onClose={() => setCreating(false)} />}

      {supported.length === 0 ? (
        <EmptyState>
          No source group yet. “+ New Source” creates the group and its first connection in one
          call.
        </EmptyState>
      ) : (
        <div className="space-y-3">
          {supported.map((g) => (
            <div key={g.id} ref={register(g.id)}>
              <GroupCard
                group={g}
                open={expandedId === g.id}
                onToggle={() => toggle(g.id)}
              />
            </div>
          ))}
        </div>
      )}
    </>
  );
}
