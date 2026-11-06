'use client';

// Keeps a clicked accordion header where the reader is looking.
//
// The accordions here are exclusive, so opening a card below the open one
// collapses that one first. The document then loses the collapsed card's
// height, everything after it slides up, and the card the reader just clicked
// jumps out from under the cursor — sometimes off the top of the viewport.
//
// The fix is to measure the clicked element's distance from the top of the
// viewport before the state change and restore it after: the rest of the page
// moves, that card does not. It runs in useLayoutEffect, so the correction
// lands in the same frame as the collapse and nothing flickers.
//
// Browsers do have native scroll anchoring (overflow-anchor), but it stands
// down around explicit user interaction and around the viewport-top region,
// which is exactly this case.

import React from 'react';

export interface AnchoredAccordion<T extends string> {
  /** ref callback for the element that should hold still — the card wrapper. */
  register: (id: T) => (node: HTMLElement | null) => void;
  /** Call with the id being toggled, immediately before the state update. */
  anchorTo: (id: T) => void;
}

/**
 * @param openId The accordion's current open id. The correction runs whenever
 *               it changes, which is the commit that changes the heights.
 */
export function useAnchoredAccordion<T extends string>(
  openId: T | null,
): AnchoredAccordion<T> {
  const nodes = React.useRef(new Map<T, HTMLElement>());
  const pending = React.useRef<{ id: T; top: number } | null>(null);

  const register = React.useCallback(
    (id: T) => (node: HTMLElement | null) => {
      if (node) nodes.current.set(id, node);
      else nodes.current.delete(id);
    },
    [],
  );

  const anchorTo = React.useCallback((id: T) => {
    const el = nodes.current.get(id);
    if (el) pending.current = { id, top: el.getBoundingClientRect().top };
  }, []);

  React.useLayoutEffect(() => {
    const anchor = pending.current;
    pending.current = null;
    if (!anchor) return;

    const el = nodes.current.get(anchor.id);
    if (!el) return;

    const delta = el.getBoundingClientRect().top - anchor.top;
    // Sub-pixel drift is not worth a scroll call.
    if (Math.abs(delta) > 0.5) {
      window.scrollBy({ top: delta, behavior: 'instant' });
    }
  }, [openId]);

  return { register, anchorTo };
}
