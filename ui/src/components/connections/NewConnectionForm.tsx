'use client';

// Adding a connection to a group that already exists. The group's own type and
// provider are fixed, so only the connection field set is shown.

import React from 'react';
import { Button, ErrorNote, Note, Panel } from '@/components/ui/primitives';
import { ConnectionFields } from './ConnectionFields';
import {
  emptyConnectionDraft,
  toCreateConnectionReq,
  validateConnectionDraft,
  type ConnectionDraft,
} from './connectionDraft';
import { useCreateConnection } from '@/queries/honeybee';
import type { Category } from '@/lib/connectionCategory';
import type { SourceGroupRes } from '@/types/honeybee';

export function NewConnectionForm({
  group,
  category,
  onDone,
}: {
  group: SourceGroupRes;
  category: Category;
  onDone: () => void;
}) {
  const [draft, setDraft] = React.useState<ConnectionDraft>(() => emptyConnectionDraft(category));
  // An empty form is not a form full of mistakes. Nothing is reported until the
  // user has actually tried to submit one.
  const [attempted, setAttempted] = React.useState(false);
  const create = useCreateConnection(group.id);

  const problems = validateConnectionDraft(category, draft, group.provider_name);

  function submit() {
    setAttempted(true);
    if (problems.length > 0) return;
    create.mutate(toCreateConnectionReq(draft), { onSuccess: onDone });
  }

  const installsAgent =
    category === 'filesystem' ||
    (draft.os_access_type ?? draft.db_access_type) === 'ssh-tunnel';

  return (
    <Panel className="panel-open">
      <ConnectionFields
        category={category}
        provider={group.provider_name}
        draft={draft}
        onChange={(patch) => setDraft((d) => ({ ...d, ...patch }))}
      />

      {installsAgent && (
        <div className="mt-4">
          <Note tone="warn">
            This installs the agent on the host and takes tens of seconds. The result is read from
            the connection’s status fields, not from the HTTP code.
          </Note>
        </div>
      )}

      {attempted && problems.length > 0 && (
        <ul className="text-xs text-amber-500 dark:text-amber-400 font-mono space-y-0.5 mt-3">
          {problems.map((p) => (
            <li key={p}>· {p}</li>
          ))}
        </ul>
      )}

      <ErrorNote error={create.error} />

      <div className="flex justify-end gap-2 mt-4">
        <Button onClick={onDone}>cancel</Button>
        {/* Enabled even while the draft is incomplete: with the problem list
            hidden until now, a disabled button would refuse to say what it is
            waiting for. Pressing it is what asks. */}
        <Button variant="primary" disabled={create.isPending} onClick={submit}>
          {create.isPending ? 'creating…' : 'add connection'}
        </Button>
      </div>
    </Panel>
  );
}
