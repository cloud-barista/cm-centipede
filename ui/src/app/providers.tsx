'use client';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { ApiError } from '@/api/http';

/**
 * Every screen is a client component, so there is no SSR hydration of server
 * state to arrange — the provider only has to exist above the tree.
 */
export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = React.useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // cm-centipede rate-limits at 20 req/s; a 429 or a 5xx is worth one
            // more try with backoff, a 4xx never is.
            retry: (failureCount, error) => {
              if (failureCount >= 2) return false;
              return error instanceof ApiError ? error.retryable : false;
            },
            retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
            refetchOnWindowFocus: true,
          },
        },
      }),
  );

  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
