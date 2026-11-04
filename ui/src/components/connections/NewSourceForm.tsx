'use client';

// Three steps, not four: the category alone decides source_group.type, so there
// is no "how do we connect" question to ask. Group and connection
// are created in one POST, because CreateSourceGroupReq.connection_info is an
// array.

import React from 'react';
import {
  Button,
  ErrorNote,
  Field,
  Panel,
  Radio,
  Select,
  TextInput,
} from '@/components/ui/primitives';
import { ConnectionFields } from './ConnectionFields';
import {
  emptyConnectionDraft,
  toCreateConnectionReq,
  validateConnectionDraft,
  type ConnectionDraft,
} from './connectionDraft';
import { useCreateSourceGroup } from '@/queries/honeybee';
import { CATEGORIES, CATEGORY_LABEL, groupTypeOf, type Category } from '@/lib/connectionCategory';
import { PROVIDER_OPTIONS, groupFields, isVisible, regionRequired } from '@/lib/sourceFormSchema';

export function NewSourceForm({ onClose }: { onClose: () => void }) {
  const [category, setCategory] = React.useState<Category>('filesystem');
  const [name, setName] = React.useState('');
  const [description, setDescription] = React.useState('');
  const [provider, setProvider] = React.useState('');
  const [region, setRegion] = React.useState('');
  const [conns, setConns] = React.useState<ConnectionDraft[]>([
    emptyConnectionDraft('filesystem'),
  ]);

  // An empty form is not a form full of mistakes. Nothing is reported until the
  // user has actually tried to submit one.
  const [attempted, setAttempted] = React.useState(false);

  const create = useCreateSourceGroup();
  const gRules = groupFields(category, provider);

  function switchCategory(next: Category) {
    setCategory(next);
    setProvider('');
    setRegion('');
    setAttempted(false);
    // The field sets have nothing in common, so the drafts are rebuilt.
    setConns([emptyConnectionDraft(next)]);
  }

  function patchConn(i: number, patch: Partial<ConnectionDraft>) {
    setConns((cur) => cur.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));
  }

  const problems: string[] = [];
  if (!name.trim()) problems.push('group name is required.');
  if (gRules.provider_name === 'required' && !provider) problems.push('provider is required.');
  if (gRules.region_name === 'required' && !region.trim()) {
    problems.push(`region is required for the ${provider} provider.`);
  }
  conns.forEach((c, i) => {
    validateConnectionDraft(category, c, provider).forEach((e) =>
      problems.push(`connection ${i + 1}: ${e}`),
    );
  });

  const installsAgent = category === 'filesystem' || conns.some(
    (c) => (c.os_access_type ?? c.db_access_type) === 'ssh-tunnel',
  );

  function submit() {
    setAttempted(true);
    if (problems.length > 0) return;
    create.mutate(
      {
        name: name.trim(),
        description: description.trim(),
        type: groupTypeOf(category),
        ...(isVisible(gRules.provider_name) ? { provider_name: provider } : {}),
        ...(isVisible(gRules.region_name) && region.trim()
          ? { region_name: region.trim() }
          : {}),
        connection_info: conns.map(toCreateConnectionReq),
      },
      { onSuccess: onClose },
    );
  }

  return (
    <Panel className="mb-4 panel-open">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-base font-extrabold">
          New source — {CATEGORY_LABEL[category]}
        </h3>
        <Button onClick={onClose}>close</Button>
      </div>

      <div className="space-y-5">
        <StepBlock n={1} title="Category">
          <div className="flex flex-wrap gap-6">
            {CATEGORIES.map((c) => (
              <Radio
                key={c}
                name="new-source-category"
                value={c}
                checked={category === c}
                onChange={(v) => switchCategory(v as Category)}
                label={CATEGORY_LABEL[c]}
              />
            ))}
          </div>
        </StepBlock>

        <StepBlock n={2} title="Source group">
          <div className="grid md:grid-cols-2 gap-3">
            <Field label="name" required>
              <TextInput
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="ex) prod-seoul-fs"
              />
            </Field>
            <Field label="description">
              <TextInput value={description} onChange={(e) => setDescription(e.target.value)} />
            </Field>

            {isVisible(gRules.provider_name) && (
              <Field
                label="provider_name"
                required={gRules.provider_name === 'required'}
                hint={category === 'database' ? 'recorded only — nothing is derived from it' : undefined}
              >
                <Select value={provider} onChange={(e) => setProvider(e.target.value)}>
                  <option value="">select…</option>
                  {PROVIDER_OPTIONS.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </Select>
              </Field>
            )}

            {isVisible(gRules.region_name) && (
              <Field
                label="region_name"
                required={gRules.region_name === 'required'}
                hint={regionRequired(provider) ? `required for ${provider}` : 'optional'}
              >
                <TextInput value={region} onChange={(e) => setRegion(e.target.value)} />
              </Field>
            )}
          </div>

          {category === 'filesystem' && (
            <p className="text-xs text-text-muted mt-2">
              no provider / region — an fs group derives nothing from them
            </p>
          )}
          {category === 'database' && (
            <p className="text-xs text-text-muted mt-2">
              no region — a database carries its own host:port
            </p>
          )}
        </StepBlock>

        <StepBlock n={3} title="Connection">
          <div className="space-y-4">
            {conns.map((c, i) => {
              const fields = (
                <ConnectionFields
                  category={category}
                  provider={provider}
                  draft={c}
                  onChange={(patch) => patchConn(i, patch)}
                />
              );
              // A lone connection is the common case, and a card around the only
              // thing on screen is a border that separates nothing.
              if (conns.length === 1) return <div key={c.uid}>{fields}</div>;
              return (
                <div key={c.uid} className="panel panel-inset p-4">
                  <ConnectionCardHeader
                    n={i + 1}
                    name={c.name}
                    onRemove={() => setConns((cur) => cur.filter((_, idx) => idx !== i))}
                  />
                  {fields}
                </div>
              );
            })}

            <Button onClick={() => setConns((cur) => [...cur, emptyConnectionDraft(category)])}>
              + add another connection
            </Button>
          </div>
        </StepBlock>

        {attempted && problems.length > 0 && (
          <ul className="text-xs text-amber-500 dark:text-amber-400 space-y-0.5">
            {problems.map((p) => (
              <li key={p}>· {p}</li>
            ))}
          </ul>
        )}

        <ErrorNote error={create.error} />

        <div className="flex flex-wrap items-center justify-end gap-x-4 gap-y-2">
          {/* The agent install is the one surprise in this form — it downloads a
              binary and can take tens of seconds. It is a footnote to the button
              that triggers it, not an alert: for a filesystem group it is always
              true, so an alert box would be permanent furniture.

              The call answers 200 either way; the outcome is read afterwards from
              connection_status / agent_status on the connection block. */}
          {installsAgent && (
            <span className="text-xs text-text-muted mr-auto">
              creating installs the cm-honeybee agent on the host — this can take tens of seconds
            </span>
          )}
          <Button onClick={onClose}>cancel</Button>
          <Button variant="primary" onClick={submit} disabled={create.isPending}>
            {create.isPending ? 'creating… (agent install can take a while)' : 'create'}
          </Button>
        </div>
      </div>
    </Panel>
  );
}

/**
 * What tells two connection forms apart while scrolling through them.
 *
 * The number is here because the validation list addresses them that way —
 * "connection 2: user is required." — and an unnamed draft has nothing else to
 * be called. But the number is not what a reader recognises a connection by, so
 * the name they are typing is echoed beside it and carries the weight.
 */
function ConnectionCardHeader({
  n,
  name,
  onRemove,
}: {
  n: number;
  name: string;
  onRemove: () => void;
}) {
  return (
    <div className="flex items-center gap-2.5 mb-4 pb-3 border-b border-border-main">
      <span className="inline-flex items-center justify-center w-5 h-5 shrink-0 rounded-full bg-indigo-500 text-white text-2xs font-mono font-bold">
        {n}
      </span>
      <span className="flex-1 min-w-0 truncate text-sm font-bold">
        {name.trim() || <span className="font-normal italic text-text-muted">unnamed</span>}
      </span>
      <Button variant="danger" onClick={onRemove}>
        remove
      </Button>
    </div>
  );
}

function StepBlock({
  n,
  title,
  children,
}: {
  n: number;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-t border-border-main pt-4 first:border-0 first:pt-0">
      <div className="flex items-baseline gap-3 mb-3">
        <span className="text-xs font-mono font-extrabold text-indigo-600 dark:text-indigo-400">STEP {n}</span>
        <h4 className="text-sm font-bold">{title}</h4>
      </div>
      {children}
    </div>
  );
}
