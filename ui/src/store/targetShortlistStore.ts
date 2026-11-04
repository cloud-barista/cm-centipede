'use client';

// Targets a reader has set aside while browsing cm-beetle. A shortlist, not a
// destination: the wizard promotes one of these to the destination of the
// migration it is composing, and the rest stay candidates.
//
// Persisting is safe here, and deliberate. A cm-beetle reference is identifiers
// only — nsId with an infra/node, an object storage id, or an rdbms id — and
// beetleDb's password field is blanked on the way in: cm-beetle never returns
// one, so there is nothing to keep, and the wizard collects it at Step 2 into
// the secret store, which has no persist middleware.

import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { ConnectionRef } from '@/types/common';

export interface TargetSelection {
  /**
   * Identity and display text at once — it already spells out the source, the
   * namespace and the ids ("beetleSsh cpbt01 / web-tier / web-02"), so the same
   * target picked from two namespaces is two entries, as it should be.
   */
  label: string;
  ref: ConnectionRef;
}

interface TargetShortlistState {
  items: TargetSelection[];
  add: (item: TargetSelection) => void;
  remove: (label: string) => void;
  clear: () => void;
}

/** Defence in depth: a password must never reach a persisted store. */
function withoutSecrets(ref: ConnectionRef): ConnectionRef {
  if (ref.source !== 'beetleDb' || !ref.beetleDb) return ref;
  return { ...ref, beetleDb: { ...ref.beetleDb, password: '' } };
}

export const useTargetShortlist = create<TargetShortlistState>()(
  persist(
    (set) => ({
      items: [],
      add: (item) =>
        set((s) =>
          s.items.some((i) => i.label === item.label)
            ? s
            : { items: [...s.items, { label: item.label, ref: withoutSecrets(item.ref) }] },
        ),
      remove: (label) => set((s) => ({ items: s.items.filter((i) => i.label !== label) })),
      clear: () => set({ items: [] }),
    }),
    { name: 'centipede-target-shortlist' },
  ),
);
