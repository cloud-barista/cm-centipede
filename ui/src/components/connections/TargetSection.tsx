'use client';

import React from 'react';
import { TargetPicker } from './TargetPicker';
import { Note } from '@/components/ui/primitives';
import { useUiStore } from '@/store/uiStore';

/**
 * A browser, not a CRUD screen. Creating target infrastructure is cm-beetle
 * UI's job; cm-centipede only points at what already exists.
 */
export function TargetSection() {
  const nsId = useUiStore((s) => s.namespaceId);
  const setNamespaceId = useUiStore((s) => s.setNamespaceId);

  return (
    <div className="space-y-3">
      <Note>
        Somewhere to look, not somewhere to change. cm-beetle owns these — a
        destination is created and edited in cm-beetle. What this screen does is
        set candidates aside: the list keeps them across namespaces and across
        visits, and the wizard picks one of them as a migration&rsquo;s
        destination.
      </Note>
      <TargetPicker nsId={nsId} onNsChange={setNamespaceId} />
    </div>
  );
}
