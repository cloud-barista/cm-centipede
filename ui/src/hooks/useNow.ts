'use client';

// One clock for the whole app.
//
// A relative stamp computed during render only moves when something else
// re-renders the component — and what these stamps measure, a query's own
// dataUpdatedAt, is set to the render time by the very update that causes the
// render. "updated 0s ago" was therefore literal: it rendered 0s, froze until
// the next poll, and rendered 0s again. Only Overview had a ticker, so only
// Overview's stamp moved.
//
// One module-level interval rather than one per caller, so every stamp on a
// screen advances at the same moment and a list of them still costs one timer.

import React from 'react';

const TICK_MS = 1000;

let current = Date.now();
let timer: ReturnType<typeof setInterval> | null = null;
const listeners = new Set<() => void>();

function subscribe(onChange: () => void): () => void {
  listeners.add(onChange);
  if (timer === null) {
    timer = setInterval(() => {
      current = Date.now();
      listeners.forEach((l) => l());
    }, TICK_MS);
  }
  return () => {
    listeners.delete(onChange);
    if (listeners.size === 0 && timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  };
}

/** Cached, never freshly computed: a getSnapshot that returns a new value on
 *  every call makes React re-render without end. */
function getSnapshot(): number {
  return current;
}

/**
 * A fixed value on the server, so a stamp rendered there and hydrated here does
 * not count as a mismatch. Nothing shows through it in practice — a query has no
 * data during SSR, so every stamp renders its "-" branch.
 */
function getServerSnapshot(): number {
  return 0;
}

/** The current time, re-rendering the caller once a second. */
export function useNow(): number {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
