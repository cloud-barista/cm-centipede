// A cm-beetle object storage destination's path, whose first segment is not the
// reader's to choose.
//
// data-mig reaches a cm-beetle bucket through cb-tumblebug by nsId/osId alone
// (NewS3Provider passes no bucket to NewTumblebugProvider), so the first
// segment of the destination path names nothing the transfer reads — only the
// key prefix after it is used. The server fills an empty one in with the osId
// (resolveObjectStorageDstPath in pkg/core/plan/target.go) so that the plan and
// the records name the bucket the objects actually land in.
//
// The wizard does the same for every path it sends: the osId is fixed as the
// first segment, and the reader types only the prefix. Typing a CSP bucket name
// there was accepted, ignored, and printed into the plan as though it were the
// target.

import type { ConnectionRef } from '@/types/common';

/** The osId when target is a cm-beetle object storage, else undefined. */
export function beetleOsIdOf(target: ConnectionRef | null | undefined): string | undefined {
  return target?.source === 'beetleObjectStorage' ? target.beetleObjectStorage?.osId || undefined : undefined;
}

/** What follows the first segment of a bucket path: "os-1/backup/" → "backup/". */
export function prefixAfterBucket(path: string): string {
  const p = path.trim().replace(/^\/+/, '');
  const i = p.indexOf('/');
  return i < 0 ? '' : p.slice(i + 1);
}

/**
 * The destination path sent for a cm-beetle target: the osId, then the prefix
 * already in path (whatever its first segment said). An empty prefix is the
 * bucket's root.
 */
export function beetleDstPath(osId: string, path: string): string {
  const prefix = prefixAfterBucket(path).replace(/^\/+/, '');
  return prefix ? `${osId}/${prefix}` : osId;
}
