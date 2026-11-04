import { NextRequest } from 'next/server';
import { proxy } from '@/lib/proxy';

const TARGET = process.env.CENTIPEDE_ENDPOINT || 'http://localhost:8085';

async function handler(
  req: NextRequest,
  { params }: { params: Promise<{ path?: string[] }> },
) {
  const { path = [] } = await params;
  return proxy(req, TARGET, 'centipede', path, {
    basicAuth: {
      username: process.env.CENTIPEDE_API_USERNAME ?? 'ChangeMe',
      password: process.env.CENTIPEDE_API_PASSWORD ?? 'ChangeMe',
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
