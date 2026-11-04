'use client';

import React from 'react';
import { Button, ErrorNote, Field, Note, Panel, TextInput } from '@/components/ui/primitives';
import { targetModelStore } from '@/lib/targetModelStore';
import { useWizardStore } from '@/store/wizardStore';
import type { Category } from '@/lib/connectionCategory';

export function SaveTargetModelBlock({ category }: { category: Category }) {
  const state = useWizardStore((s) => s.byCategory[category]);
  const patch = useWizardStore((s) => s.patch);
  const [name, setName] = React.useState('');
  const [version, setVersion] = React.useState('1.0.0');
  const [error, setError] = React.useState<unknown>(null);
  const [saving, setSaving] = React.useState(false);

  if (!targetModelStore || !state.plan) return null;

  async function save() {
    if (!targetModelStore || !state.plan) return;
    setSaving(true);
    setError(null);
    try {
      const { id } = await targetModelStore.save({
        name: name.trim(),
        version: version.trim(),
        category,
        plan: state.plan,
      });
      patch(category, { targetModelId: id });
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Panel className="panel-open">
      <h4 className="text-sm font-bold mb-3">Save as target model</h4>

      <div className="grid md:grid-cols-2 gap-3">
        <Field label="name" required>
          <TextInput value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="version" required>
          <TextInput value={version} onChange={(e) => setVersion(e.target.value)} />
        </Field>
      </div>

      <div className="mt-3 space-y-2">
        <Note>
          Required to run through cicada. Direct execution on cm-centipede does not need it.
        </Note>
        <Note tone="warn">
          Editing steps 1–3 invalidates the plan and this saved model — you will be asked to save
          again.
        </Note>
      </div>

      <ErrorNote error={error} />

      <div className="flex items-center gap-3 mt-3">
        <span className="text-xs font-mono text-text-muted">
          {state.targetModelId ? `saved · ${state.targetModelId}` : 'not saved'}
        </span>
        <Button
          variant="primary"
          className="ml-auto"
          onClick={save}
          disabled={saving || !name.trim()}
        >
          {saving ? 'saving…' : 'save to damselfly'}
        </Button>
      </div>
    </Panel>
  );
}
