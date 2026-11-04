'use client';

import React from 'react';
import { AccordionRow, Badge, Button, ErrorNote, Loading } from '@/components/ui/primitives';
import { CategoryBadge } from '@/components/ui/status';
import { useConnections, useDeleteSourceGroup } from '@/queries/honeybee';
import { categoryOfGroup } from '@/lib/connectionCategory';
import { ConnectionBlock } from './ConnectionBlock';
import { NewConnectionForm } from './NewConnectionForm';
import { StatusRollup } from './StatusRollup';
import type { SourceGroupRes } from '@/types/honeybee';

export function GroupCard({
  group,
  open,
  onToggle,
}: {
  group: SourceGroupRes;
  open: boolean;
  onToggle: () => void;
}) {
  const category = categoryOfGroup(group);
  const conns = useConnections(group.id, open);
  const del = useDeleteSourceGroup();
  const [adding, setAdding] = React.useState(false);

  // The per-group counts come from the group listing itself — expanding a group
  // is never what makes the header numbers correct.
  const c = group.connection_info_status_count;

  return (
    <AccordionRow
      open={open}
      onToggle={onToggle}
      summary={
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-bold text-sm truncate">{group.name}</span>
            <Badge>{group.type}</Badge>
            {group.provider_name && <Badge>{group.provider_name}</Badge>}
            {group.region_name && <Badge>{group.region_name}</Badge>}
            <CategoryBadge category={category} />
          </div>
          {/* Same roll-up as the page header, so a group row and the total above
              it are read the same way. */}
          <div className="text-xs mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
            <StatusRollup count={c} />
            {group.description && <span className="text-text-muted">{group.description}</span>}
          </div>
        </div>
      }
      right={
        <Button
          variant="danger"
          onClick={() => {
            if (confirm(`Delete source group "${group.name}" and all its connections?`)) {
              del.mutate(group.id);
            }
          }}
          disabled={del.isPending}
        >
          delete
        </Button>
      }
    >
      <ErrorNote error={del.error} />

      <div className="flex items-center gap-2 mb-3">
        <h4 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Connections ({conns.data?.connection_info.length ?? 0})
        </h4>
        <div className="flex-1 h-px bg-border-main" />
        <Button onClick={() => setAdding((a) => !a)}>{adding ? 'close' : '+ Add'}</Button>
      </div>

      {adding && category && (
        <div className="mb-4">
          <NewConnectionForm
            group={group}
            category={category}
            onDone={() => setAdding(false)}
          />
        </div>
      )}

      <ErrorNote error={conns.error} />

      {conns.isPending ? (
        <Loading label="loading connections…" />
      ) : (conns.data?.connection_info.length ?? 0) === 0 ? (
        <p className="text-xs text-text-muted py-2">
          This group has no connection yet.
        </p>
      ) : (
        <ul className="divide-y divide-border-main">
          {conns.data!.connection_info.map((conn) => (
            <li key={conn.id} className="py-3 first:pt-0 last:pb-0">
              <ConnectionBlock connection={conn} group={group} />
            </li>
          ))}
        </ul>
      )}
    </AccordionRow>
  );
}
