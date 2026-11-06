// Pure HTTP layer. Knows nothing about TanStack Query, so the server-state
// library can be swapped without touching these functions.
//
// The three backends disagree about envelopes:
//   cm-centipede  ApiResponse{success, data, error}
//   cm-beetle     ApiResponse{success, data} on its own routes, bare objects on
//                 the tumblebug pass-through routes
//   cm-honeybee   bare objects, ErrorResponse on failure
// Normalization happens here so every caller above sees plain data and a
// plain-string error message.

import axios, { AxiosError, AxiosRequestConfig, AxiosResponse } from 'axios';

export class ApiError extends Error {
  readonly status?: number;
  readonly retryable: boolean;
  /**
   * Seconds the server asked us to wait, from its Retry-After header.
   *
   * cm-beetle sets it on the 503 it returns when the underlying cloud provider
   * rate-limits it (its ratelimit.RetryAfter path). A poller that keeps its
   * cadence through that is the thing the header exists to prevent, so the
   * number is carried up rather than guessed at by the caller.
   */
  readonly retryAfterSec?: number;

  constructor(message: string, status?: number, retryable = false, retryAfterSec?: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.retryable = retryable;
    this.retryAfterSec = retryAfterSec;
  }
}

interface CentipedeEnvelope<T> {
  success?: boolean;
  data?: T;
  message?: string;
  error?: string;
}

const RETRYABLE_STATUS = new Set([408, 425, 429, 500, 502, 503, 504]);

function messageFrom(body: unknown, fallback: string): string {
  if (body && typeof body === 'object') {
    const b = body as Record<string, unknown>;
    for (const key of ['error', 'message', 'err', 'detail']) {
      const v = b[key];
      if (typeof v === 'string' && v.trim() !== '') return v;
    }
  }
  if (typeof body === 'string' && body.trim() !== '') return body;
  return fallback;
}

/** Reads Retry-After as delta-seconds. The HTTP-date form is not used by these backends. */
function retryAfterOf(headers: unknown): number | undefined {
  if (!headers || typeof headers !== 'object') return undefined;
  const raw = (headers as Record<string, unknown>)['retry-after'];
  const n = Number(raw);
  return Number.isFinite(n) && n >= 0 ? n : undefined;
}

/** Turns anything thrown by axios into an ApiError carrying a plain message. */
export function normalize(e: unknown): ApiError {
  if (e instanceof ApiError) return e;

  const err = e as AxiosError;
  if (err?.isAxiosError) {
    const status = err.response?.status;
    const msg = err.response
      ? messageFrom(err.response.data, `${status} ${err.response.statusText}`)
      : (err.message || 'network error');
    return new ApiError(
      msg,
      status,
      status !== undefined && RETRYABLE_STATUS.has(status),
      retryAfterOf(err.response?.headers),
    );
  }

  return new ApiError(e instanceof Error ? e.message : String(e));
}

/** Unwraps whichever envelope came back and raises ApiError on failure. */
export async function call<T>(p: Promise<AxiosResponse>): Promise<T> {
  let res: AxiosResponse;
  try {
    res = await p;
  } catch (e) {
    throw normalize(e);
  }

  const body = res.data as CentipedeEnvelope<T> | T;
  if (body && typeof body === 'object' && 'success' in (body as object)) {
    const env = body as CentipedeEnvelope<T>;
    if (env.success === false) {
      throw new ApiError(env.error ?? env.message ?? 'request failed', res.status);
    }
    return env.data as T;
  }
  return body as T;
}

/**
 * Go marshals a nil slice as `null`, not `[]`, and none of these backends tag
 * their list fields `omitempty` — so an empty list arrives as null on every one
 * of them. Every list response is normalized at this boundary rather than at
 * each call site, which is what makes the array types declared in src/types
 * actually true for the code above.
 */
export function arr<T>(v: T[] | null | undefined): T[] {
  return v ?? [];
}

const client = axios.create({
  // Same-origin: every backend is reached through this app's proxy routes.
  timeout: 120_000,
  headers: { 'Content-Type': 'application/json' },
});

export const http = {
  get: <T>(url: string, config?: AxiosRequestConfig) => call<T>(client.get(url, config)),
  post: <T>(url: string, body?: unknown, config?: AxiosRequestConfig) =>
    call<T>(client.post(url, body, config)),
  put: <T>(url: string, body?: unknown, config?: AxiosRequestConfig) =>
    call<T>(client.put(url, body, config)),
  del: <T>(url: string, config?: AxiosRequestConfig) => call<T>(client.delete(url, config)),
};
