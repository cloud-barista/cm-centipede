// Draft shape and validation for a cm-honeybee connection form. Kept apart from
// the field components so neither file outgrows the 400-line cap.

import { connectionFields, type HoneybeeAccess } from '@/lib/sourceFormSchema';
import type { Category } from '@/lib/connectionCategory';
import type { CreateConnectionInfoReq } from '@/types/honeybee';
import { DBMS_DEFAULT_PORT } from '@/types/common';

export type ConnectionDraft = CreateConnectionInfoReq & {
  /**
   * Identity of this draft within the list it is being edited in — UI state
   * only. The list was keyed by array index, so removing a connection shifted
   * every draft below it onto the element above: React kept that element's own
   * state, and ConnectionFields' "advanced" toggle stayed with the position
   * rather than with the connection.
   */
  uid: string;
};

let nextUid = 0;

export function emptyConnectionDraft(category: Category): ConnectionDraft {
  const base: ConnectionDraft = { name: '', uid: `draft-${nextUid++}` };
  // fs_scan_path is deliberately NOT seeded with cm-honeybee's "/home" fallback: a
  // value sitting in the box reads as one chosen for this host. The field's hint
  // says what an empty one registers instead.
  if (category === 'filesystem') return { ...base, ssh_port: '22' };
  if (category === 'objectstorage') return { ...base, os_access_type: 'direct' };
  return { ...base, db_access_type: 'direct', db_type: 'mysql', db_port: String(DBMS_DEFAULT_PORT.mysql) };
}

export function draftAccessType(draft: ConnectionDraft): HoneybeeAccess | undefined {
  return (draft.os_access_type ?? draft.db_access_type) as HoneybeeAccess | undefined;
}

/**
 * cm-honeybee's own rule for ip_address, mirrored here.
 *
 * The value has to be a literal IPv4 address. Nothing is resolved, so every
 * host name fails — "localhost" included — and a genuine IPv6 address fails
 * too.
 *
 * Worth catching here rather than letting the server say it, because creating an
 * SSH connection also installs the agent: the round trip that rejects the
 * address is tens of seconds long.
 *
 * Only ip_address. The rule applies to nothing else, so db_host and
 * os_endpoint still take host names.
 */
function isIPv4(v: string): boolean {
  const parts = v.split('.');
  // No leading zeros: the server rejects them.
  return (
    parts.length === 4 && parts.every((p) => /^(0|[1-9]\d{0,2})$/.test(p) && Number(p) <= 255)
  );
}

/** cm-honeybee's port rule: an integer in 1..65535, nothing else. */
function isPort(v: string): boolean {
  return /^\d{1,5}$/.test(v) && Number(v) >= 1 && Number(v) <= 65535;
}

/**
 * cm-honeybee's scan-prefix normalization, applied before the draft is sent rather
 * than after the server has corrected it.
 *
 * An object key does not start with a slash, so a prefix that does matches
 * nothing. The server strips it silently; stripping it here means the field
 * shows what was actually registered.
 */
export function normalizeScanPrefix(prefix: string): string {
  return prefix.trim().replace(/^\/+/, '');
}

/**
 * Everything the UI must enforce beyond what cm-honeybee checks. Returns the
 * reasons the draft cannot be submitted; empty means it can.
 */
export function validateConnectionDraft(
  category: Category,
  draft: ConnectionDraft,
  provider?: string,
): string[] {
  const errs: string[] = [];
  const rules = connectionFields(category, {
    provider,
    dbType: draft.db_type,
    dbName: draft.db_name,
    accessType: draftAccessType(draft),
  });

  if (!draft.name.trim()) errs.push('name is required.');

  if (rules.ip_address === 'required') {
    const ip = draft.ip_address?.trim();
    const port = draft.ssh_port?.trim();
    if (!ip) errs.push('ip_address is required.');
    else if (!isIPv4(ip)) errs.push('ip_address must be a literal IPv4 address.');
    if (!port) errs.push('ssh_port is required.');
    else if (!isPort(port)) errs.push('ssh_port must be a number between 1 and 65535.');
    if (!draft.user?.trim()) errs.push('user is required.');
    // Key only: cm-honeybee refuses a password outright, and "-" counts
    // as no key at all. It also parses the key, so a malformed PEM is a 400 at
    // registration rather than a failure at the first scan — said here so the
    // field is the one reporting it.
    const key = draft.private_key?.trim();
    if (!key || key === '-') errs.push('private_key is required.');
  }

  if (category === 'filesystem') {
    // Must be absolute. A relative path would be resolved against
    // whatever working directory the agent happens to have, so the caller could
    // not say what was about to be scanned.
    const path = draft.fs_scan_path?.trim();
    if (path && !path.startsWith('/')) errs.push('fs_scan_path must be an absolute path.');
  }

  if (category === 'objectstorage') {
    if (!draft.os_access_key_id?.trim()) errs.push('os_access_key_id is required.');
    if (!draft.os_secret_access_key?.trim()) errs.push('os_secret_access_key is required.');
    if (rules.os_endpoint === 'required' && !draft.os_endpoint?.trim()) {
      errs.push(`os_endpoint is required for the ${provider} provider.`);
    }
    const bucket = draft.os_scan_bucket?.trim();
    if (!bucket) errs.push('os_scan_bucket is required.');
    else if (bucket.includes('/')) {
      errs.push('os_scan_bucket must not contain "/" — put the prefix in os_scan_prefix.');
    }
  }

  if (category === 'database') {
    if (!draft.db_type?.trim()) errs.push('db_type is required.');
    if (!draft.db_host?.trim()) errs.push('db_host is required.');
    if (!draft.db_port?.trim()) errs.push('db_port is required.');
    if (!draft.db_username?.trim()) errs.push('db_username is required.');
    if (!draft.db_password?.trim()) errs.push('db_password is required.');
  }

  return errs;
}

/** Strips the UI-only field before the draft goes on the wire. */
export function toCreateConnectionReq(draft: ConnectionDraft): CreateConnectionInfoReq {
  const { uid, ...req } = draft;
  void uid;
  if (req.os_scan_prefix !== undefined) {
    req.os_scan_prefix = normalizeScanPrefix(req.os_scan_prefix);
  }
  return req;
}
