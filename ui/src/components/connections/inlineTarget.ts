// Inline targets build a cm-centipede ConnectionRef, NOT a cm-honeybee
// ConnectionInfo. The two spell the same concepts differently, which is why the
// source form cannot be reused here:
//
//   honeybee  os_access_key_id   db_host   ssh-tunnel   flat SSH fields
//   centipede accessKeyId        host      sshTunnel    nested sshTunnel object

import { CATEGORIES, CATEGORY_LABEL, type Category } from '@/lib/connectionCategory';
import { DBMS_DEFAULT_PORT, type ConnectionRef } from '@/types/common';
import type { InlineTargetStatus, InlineTarget } from '@/store/secretStore';

export const INLINE_SOURCE_LABEL: Record<InlineTarget['source'], string> = {
  ssh: 'ssh',
  minio: 'minio',
  db: 'db',
};

/** How a stored inline target's state reads in the list and in Step 2's choices. */
export const INLINE_TARGET_STATUS_LABEL: Record<InlineTargetStatus, string> = {
  unlocked: 'unlocked',
  locked: 'locked',
  'key-mismatch': 'wrong key',
  tampered: 'tampered',
  corrupt: 'unreadable',
};

/**
 * The migrations an inline target of this type can be the destination of.
 *
 * Computed from the rule rather than written out here. The two used to be
 * separate lists and drifted: this said a minio inline target was an "Object Storage
 * target" while fitsCategory offered the very same inline target to a File System
 * migration, and the ssh entry advertised that cross-storage reach in a
 * parenthetical the minio entry did not have. Only one of the two can be the
 * rule, and it is the one Step 2 calls.
 */
export function usableAs(source: InlineTarget['source']): string {
  return CATEGORIES.filter((c) => fitsCategory(source, c))
    .map((c) => CATEGORY_LABEL[c])
    .join(' · ');
}

/**
 * The type refinement that goes beside the badge — what kind of endpoint this
 * is, one level below `source`. It belongs there rather than in the address:
 * `mysql` and `onprem` answer the question `ssh` answers, and an ssh inline target has
 * no answer below it.
 */
export function inlineTargetKindLabel(d: InlineTarget): string {
  const detail =
    d.source === 'minio' ? d.minio?.providerName : d.source === 'db' ? d.db?.dbmsType : '';
  return detail ? `${d.source} ${detail}` : d.source;
}

function hostPort(host?: string, port?: number): string {
  const h = host?.trim();
  if (!h) return '';
  return port ? `${h}:${port}` : h;
}

/**
 * Where an inline target lands and who it authenticates as, written the one way for all
 * three types: `identity@address`.
 *
 * Each type used to answer a different question — an address and an account for
 * ssh, a provider for minio, an engine and an access type for db — so two rows
 * could not be read against each other, and the fact that actually tells two
 * inline targets apart was the one left out: two minio inline targets on onprem rendered
 * identically, as did two mysql inline targets.
 */
export function describeInlineTarget(d: InlineTarget): string {
  switch (d.source) {
    case 'ssh':
      return identityAt(d.ssh?.username, hostPort(d.ssh?.host, d.ssh?.port ?? 22));

    case 'minio': {
      // No account: accessKeyId is half of a credential pair, and this list
      // shows no secret. onprem and openstack ask for the endpoint outright;
      // every other provider derives one from provider and region, so there the
      // region is the only locator there is.
      const m = d.minio;
      return m?.endpoint?.trim() || m?.region?.trim() || '-';
    }

    case 'db': {
      const b = d.db;
      // A tunnelled inline target may carry no database host — validateInlineTarget does not
      // ask for one, because the tunnel's far end is the host. Then the tunnel
      // is the whole address rather than something beside it.
      const tunnel =
        b?.accessType === 'sshTunnel'
          ? hostPort(b.sshTunnel?.host, b.sshTunnel?.port ?? 22)
          : '';
      const head = identityAt(b?.username, hostPort(b?.host, b?.port));
      return [head === '-' ? '' : head, tunnel && `via ${tunnel}`].filter(Boolean).join(' ') || '-';
    }
  }
}

/** `user@host:port`, or whichever half is known. */
function identityAt(identity: string | undefined, address: string): string {
  const who = identity?.trim();
  if (who && address) return `${who}@${address}`;
  return address || who || '-';
}

export function emptyInlineTarget(id: string, source: InlineTarget['source']): InlineTarget {
  const base: InlineTarget = { id, label: '', source };
  if (source === 'ssh') return { ...base, ssh: { host: '', port: 22, username: '' } };
  if (source === 'minio') {
    return {
      ...base,
      minio: { providerName: '', accessKeyId: '', secretAccessKey: '', bucketLookup: '' },
    };
  }
  return {
    ...base,
    db: { providerName: '', dbmsType: 'mysql', accessType: 'direct', host: '', port: DBMS_DEFAULT_PORT.mysql },
  };
}

/**
 * What this UI enforces on top of the server's checks.
 *
 * validateConnectionRef() does not look at the ssh privateKey or at a db's
 * username/password, so without this the plan succeeds and the migration fails
 * at authentication time.
 */
export function validateInlineTarget(target: InlineTarget): string[] {
  const errs: string[] = [];

  if (target.source === 'ssh') {
    const s = target.ssh;
    if (!s?.host.trim()) errs.push('host is required.');
    if (!s?.username.trim()) errs.push('username is required.');
    // Key only: ResolveSSHConfig reads no password, so an inline target without a key
    // has no credential at all however filled-in it looks.
    if (!s?.privateKey?.trim()) errs.push('privateKey is required.');
  }

  if (target.source === 'minio') {
    const m = target.minio;
    if (!m?.providerName) errs.push('providerName is required.');
    if (!m?.accessKeyId.trim()) errs.push('accessKeyId is required.');
    if (!m?.secretAccessKey.trim()) errs.push('secretAccessKey is required.');
  }

  if (target.source === 'db') {
    const d = target.db;
    if (!d?.providerName) errs.push('providerName is required.');
    if (!d?.dbmsType) errs.push('dbmsType is required.');
    if (d?.accessType === 'sshTunnel') {
      if (!d.sshTunnel?.host.trim()) errs.push('sshTunnel.host is required.');
      if (!d.sshTunnel?.username.trim()) errs.push('sshTunnel.username is required.');
      if (!d.sshTunnel?.privateKey?.trim()) errs.push('sshTunnel.privateKey is required.');
    } else if (!d?.host.trim()) {
      errs.push('host is required.');
    }
    if (!d?.port) errs.push('port is required.');
    else if (!Number.isInteger(d.port) || d.port < 1 || d.port > 65535) {
      errs.push('port must be a number between 1 and 65535.');
    }
    if (!d?.username?.trim()) errs.push('username is required.');
    if (!d?.password?.trim()) errs.push('password is required.');
  }

  return errs;
}

/** label and id are UI-only and fall away here — they never reach the server. */
export function toConnectionRef(target: InlineTarget): ConnectionRef {
  switch (target.source) {
    case 'ssh':
      return { source: 'ssh', ssh: target.ssh };
    case 'minio':
      return { source: 'minio', minio: target.minio };
    case 'db':
      return { source: 'db', db: target.db };
  }
}

/** Which categories may use an inline target of this type as their destination. */
function fitsCategory(source: InlineTarget['source'], category: Category): boolean {
  if (category === 'database') return source === 'db';
  // A file system job can land on object storage and vice versa (cross-storage),
  // so both path categories accept both path-shaped inline targets.
  return source === 'ssh' || source === 'minio';
}

/** Which categories may use this inline target as their destination. */
export function inlineTargetFitsCategory(target: InlineTarget, category: Category): boolean {
  return fitsCategory(target.source, category);
}
