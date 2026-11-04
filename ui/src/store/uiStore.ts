'use client';

import { create } from 'zustand';
import { persist } from 'zustand/middleware';

export type TabId = 'overview' | 'connections' | 'filesystem' | 'objectstorage' | 'database' | 'runs';

export type ThemeMode = 'dark' | 'light';

/**
 * How the wizard's Step 1 draws a scan result.
 *
 * Three because each covers what the others cannot: `folders` is the units the
 * later steps work in, as the hierarchy they already are; `json` is the whole
 * response with its structure navigable; `raw` is the one a reader can select,
 * copy and search with the browser's own find.
 *
 * The id is what persists; what the switch prints for `folders` follows the
 * domain, since the unit is a folder, a bucket or a database.
 */
export type InspectResultView = 'folders' | 'json' | 'raw';

interface UiState {
  activeTab: TabId;
  themeMode: ThemeMode;
  layoutWidth: number;
  /** cm-beetle namespace the Targets picker reads. */
  namespaceId: string;
  /**
   * Kept here rather than in the wizard draft: it is how someone reads a
   * result, not part of the migration they are composing. Step 1 unmounts
   * whenever the step is collapsed, so component state would forget the choice
   * every time — and it is the same choice in all three wizards.
   */
  inspectResultView: InspectResultView;
  setActiveTab: (t: TabId) => void;
  toggleTheme: () => void;
  setLayoutWidth: (w: number) => void;
  setNamespaceId: (n: string) => void;
  setInspectResultView: (v: InspectResultView) => void;
}

/** Client-owned state only — nothing here is server truth, so persisting is safe. */
export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      activeTab: 'overview',
      themeMode: 'light',
      layoutWidth: 1920,
      namespaceId: 'cpbt01',
      inspectResultView: 'folders',
      setActiveTab: (activeTab) => set({ activeTab }),
      toggleTheme: () => set((s) => ({ themeMode: s.themeMode === 'dark' ? 'light' : 'dark' })),
      setLayoutWidth: (layoutWidth) => set({ layoutWidth }),
      setNamespaceId: (namespaceId) => set({ namespaceId }),
      setInspectResultView: (inspectResultView) => set({ inspectResultView }),
    }),
    {
      name: 'centipede-ui',
      version: 2,
      /**
       * The result view has been renamed twice, and a restored value matching
       * none of the current ids would leave the panel drawing nothing — so every
       * id this store has ever written is mapped here rather than left to fall
       * through.
       *
       *   v0  'table' | 'json'          json was raw text
       *   v1  'table' | 'tree' | 'raw'  tree was the JSON tree
       *   v2  'folders' | 'json' | 'raw'
       *
       * v0's 'json' lands on the JSON tree rather than on raw: someone who
       * chose JSON wanted the document, and the tree is the better reading of
       * it now that there is one.
       */
      migrate: (persisted, version) => {
        // Read as a plain string: the old values are not in the current union,
        // which is the whole reason this runs.
        const s = (persisted ?? {}) as Omit<Partial<UiState>, 'inspectResultView'> & {
          inspectResultView?: string;
        };
        void version;
        const RENAMED: Record<string, InspectResultView> = {
          table: 'folders',
          tree: 'json',
          json: 'json',
          raw: 'raw',
          folders: 'folders',
        };
        return {
          ...s,
          inspectResultView: RENAMED[s.inspectResultView ?? ''] ?? 'folders',
        } as UiState;
      },
    },
  ),
);
