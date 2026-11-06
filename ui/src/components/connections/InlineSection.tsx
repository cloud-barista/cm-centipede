'use client';

// Inline targets are sealed into this browser's localStorage under a secret key the
// user types and nothing keeps. They never reach a server as inline targets, and no
// secret is ever shown in the list. A row is drawn from the
// inline target's clear part, so it shows whether or not the vault is unlocked.

import React from 'react';
import { Badge, Button, EmptyState, Note, Panel, SectionBand } from '@/components/ui/primitives';
import { InlineTargetForm } from './InlineTargetForm';
import { VaultKeyBar } from './VaultKeyBar';
import {
  INLINE_TARGET_STATUS_LABEL,
  describeInlineTarget,
  inlineTargetKindLabel,
  usableAs,
} from './inlineTarget';
import {
  bootVault,
  useSecretStore,
  type InlineTargetStatus,
  type InlineTarget,
} from '@/store/secretStore';

const STATUS_TONE: Record<InlineTargetStatus, 'neutral' | 'warn' | 'bad'> = {
  unlocked: 'neutral',
  locked: 'neutral',
  'key-mismatch': 'warn',
  tampered: 'bad',
  corrupt: 'bad',
};

export function InlineSection() {
  const rows = useSecretStore((s) => s.rows);
  const targets = useSecretStore((s) => s.targets);
  const unlocked = useSecretStore((s) => s.unlocked);
  const remove = useSecretStore((s) => s.remove);
  const [editing, setEditing] = React.useState<InlineTarget | null>(null);
  const [creating, setCreating] = React.useState(false);
  const formRef = React.useRef<HTMLDivElement>(null);
  React.useEffect(bootVault, []);

  // The form sits above the list, so with a long list an edit clicked far down would
  // open off-screen. Bring it into view whenever it opens or switches target.
  React.useEffect(() => {
    if (creating || editing) formRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, [creating, editing]);

  // Locking takes the decrypted inline targets away; an open edit form would keep one.
  // Dropped during render, not in an effect, so the form is never drawn locked.
  if (!unlocked && editing) setEditing(null);

  const list = Object.values(rows);

  return (
    <>
      {/* The band lives here rather than in ConnectionsPage so that "+ New Inline Target"
          can sit in it, the way "+ New Source" sits in the Sources band. */}
      <SectionBand
        title="3. Inline"
        hint="encrypted in this browser"
        right={
          <Button
            variant="primary"
            onClick={() => {
              // Starting a new inline target ends any edit in progress: the form below
              // is one form, and it cannot be both at once.
              setEditing(null);
              setCreating(true);
            }}
          >
            + New Inline Target
          </Button>
        }
      />

      <div className="space-y-3">
        <Note tone="warn">
          Inline targets are stored in this browser, their credentials encrypted under your secret
          key. The key itself is never stored — enter it again after every reload. Nothing is sent
          to any server until an inline target is used in a migration.
        </Note>

        <VaultKeyBar />

        {(creating || editing) && (
          // scroll-mt clears the sticky h-16 app header.
          <div ref={formRef} className="scroll-mt-20">
            <InlineTargetForm
              initial={editing ?? undefined}
              onClose={() => {
                setCreating(false);
                setEditing(null);
              }}
            />
          </div>
        )}

        {list.length === 0 ? (
          <EmptyState>
            No inline target. Use one when the destination is not registered in cm-honeybee or
            cm-beetle.
          </EmptyState>
        ) : (
          <Panel className="p-0">
            {/* The rule behind the last column, said once. It used to be a
                parenthetical on the ssh rows only, which made the same reach
                look like something only ssh had. */}
            <p className="px-4 py-3 text-xs text-text-muted border-b border-border-main">
              An ssh or minio inline target can be the destination of either storage migration — a
              file system source may land on object storage and vice versa (cross-storage). A db
              inline target is a Database destination only.
            </p>
            <ul className="divide-y divide-border-main">
              {list.map((r) => {
                const target = targets[r.id];
                return (
                  <li key={r.id} className="px-4 py-3 flex flex-wrap items-center gap-3">
                    <span className="font-bold text-sm">
                      {r.label || '(unnamed inline target)'}
                    </span>
                    {r.preview && <Badge tone="info">{inlineTargetKindLabel(r.preview)}</Badge>}
                    {r.status !== 'unlocked' && (
                      <Badge tone={STATUS_TONE[r.status]} title={r.reason}>
                        {INLINE_TARGET_STATUS_LABEL[r.status]}
                      </Badge>
                    )}
                    {r.preview && (
                      <span className="text-xs font-mono text-text-muted">
                        {describeInlineTarget(r.preview)}
                      </span>
                    )}
                    {r.source && (
                      <span className="text-xs flex items-baseline gap-1.5">
                        <span className="text-text-muted">destination for</span>
                        <span>{usableAs(r.source)}</span>
                      </span>
                    )}
                    <div className="ml-auto flex gap-2">
                      <Button onClick={() => target && setEditing(target)} disabled={!target}>
                        edit
                      </Button>
                      <Button variant="danger" onClick={() => void remove(r.id)}>
                        delete
                      </Button>
                    </div>
                  </li>
                );
              })}
            </ul>
          </Panel>
        )}
      </div>
    </>
  );
}
