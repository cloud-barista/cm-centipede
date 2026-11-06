// A connection reference's identity as one string.

import type { ConnectionRef } from '@/types/common';

/**
 * A destination's identity, spelled exactly as the server's dstKey
 * (pkg/core/plan/entry.go): the target, never the credentials used to reach it.
 *
 * Kept identical rather than smarter. Two spellings of one destination — a port
 * left out beside the same port written out — differ here as they do there, and
 * the server is what accepts or refuses the plan.
 */
export function dstKey(ref: ConnectionRef): string {
  switch (ref.source) {
    case 'honeybee':
      if (ref.honeybee) return `honeybee:${ref.honeybee.connectionId}`;
      break;
    case 'beetleSsh':
      if (ref.beetleSsh) {
        const r = ref.beetleSsh;
        return `beetleSsh:${r.nsId}/${r.infraId}/${r.nodeId}`;
      }
      break;
    case 'beetleObjectStorage':
      if (ref.beetleObjectStorage) {
        const r = ref.beetleObjectStorage;
        return `beetleObjectStorage:${r.nsId}/${r.osId}`;
      }
      break;
    case 'beetleDb':
      if (ref.beetleDb) return `beetleDb:${ref.beetleDb.nsId}/${ref.beetleDb.rdbmsId}`;
      break;
    case 'ssh':
      if (ref.ssh) return `ssh:${ref.ssh.username}@${ref.ssh.host}:${ref.ssh.port ?? 0}`;
      break;
    case 'minio':
      if (ref.minio) return `minio:${ref.minio.providerName}/${ref.minio.endpoint ?? ''}`;
      break;
    case 'db':
      if (ref.db) return `db:${ref.db.dbmsType}/${ref.db.host}:${ref.db.port ?? 0}`;
      break;
  }
  return `unresolved:${ref.source}`;
}
