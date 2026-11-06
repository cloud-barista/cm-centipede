import { NextRequest } from 'next/server';
import { proxy } from '@/lib/proxy';

const TARGET = process.env.BEETLE_ENDPOINT || 'http://localhost:8056';

async function handler(
  req: NextRequest,
  { params }: { params: Promise<{ path?: string[] }> },
) {
  const { path = [] } = await params;
  // cm-beetle requires Basic Auth: cm-centipede's own client sets it on every
  // call (pkg/client/beetle/client.go). Without it every Targets request comes
  // back 401. The ChangeMe fallback never reaches cm-beetle: instrumentation.ts
  // stops the server at startup while any credential still reads ChangeMe.
  return proxy(req, TARGET, 'beetle', path, {
    basicAuth: {
      username: process.env.BEETLE_API_USERNAME ?? 'ChangeMe',
      password: process.env.BEETLE_API_PASSWORD ?? 'ChangeMe',
    },
  });
}

export {
  handler as GET,
  handler as POST,
  handler as PUT,
  handler as DELETE,
  handler as PATCH,
  handler as HEAD,
};
