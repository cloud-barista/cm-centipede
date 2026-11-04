import { NextRequest } from 'next/server';

/** Credentials injected server-side so the browser never sees them. */
export interface BasicAuthCredentials {
  username: string;
  password: string;
}

export interface ProxyOptions {
  basicAuth?: BasicAuthCredentials;
}

/**
 * Generic reverse-proxy helper for Next.js API route handlers.
 *
 * Forwards an incoming request to a backend service, preserving the method,
 * headers, query string, and body. Backend endpoints are resolved from
 * server-side environment variables, so browser code never needs to know the
 * real service URLs (which differ between local dev and Docker Compose).
 *
 * @param req        The incoming Next.js request.
 * @param targetBase Base URL of the backend service (e.g. http://cm-centipede:8085).
 * @param prefix     Path prefix the backend serves under (e.g. "centipede").
 * @param path       Catch-all path segments after the prefix.
 * @param opts       Optional Basic Auth credentials injected into the request.
 */
export async function proxy(
  req: NextRequest,
  targetBase: string,
  prefix: string,
  path: string[],
  opts?: ProxyOptions,
): Promise<Response> {
  const suffix = path.length > 0 ? `/${path.join('/')}` : '';
  const targetUrl = `${targetBase.replace(/\/$/, '')}/${prefix}${suffix}${req.nextUrl.search}`;

  const headers = new Headers(req.headers);
  // Strip hop-by-hop / host-specific headers before forwarding
  headers.delete('host');
  headers.delete('connection');
  headers.delete('content-length');

  if (opts?.basicAuth) {
    const { username, password } = opts.basicAuth;
    const token = Buffer.from(`${username}:${password}`).toString('base64');
    headers.set('authorization', `Basic ${token}`);
  }

  const init: RequestInit & { duplex?: 'half' } = {
    method: req.method,
    headers,
    redirect: 'manual',
  };

  if (req.method !== 'GET' && req.method !== 'HEAD') {
    init.body = await req.arrayBuffer();
    init.duplex = 'half';
  }

  let upstream: Response;
  try {
    upstream = await fetch(targetUrl, init);
  } catch {
    return new Response(
      JSON.stringify({ error: `Upstream service unavailable: ${prefix}` }),
      { status: 502, headers: { 'content-type': 'application/json' } },
    );
  }

  const respHeaders = new Headers(upstream.headers);
  // These are recomputed by the runtime and must not be forwarded verbatim
  respHeaders.delete('content-encoding');
  respHeaders.delete('content-length');
  respHeaders.delete('transfer-encoding');
  // Never let an upstream auth challenge reach the browser: the credentials are
  // the proxy's, so a 401 here is a server misconfiguration, not a user prompt.
  respHeaders.delete('www-authenticate');

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: respHeaders,
  });
}
