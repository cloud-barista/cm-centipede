'use client';

// Inline targets, sealed in localStorage under a secret key the user types
// (lib/vault). Still deliberately WITHOUT the persist middleware:
// the vault writes only ciphertext and the clear part of each
// inline target, and nothing else in this store — decrypted inline targets, the derived keys,
// the wizard's passwords — ever leaves memory.
//
// The secret key itself is never kept, not even in sessionStorage: Chrome's
// session restore brings sessionStorage back, so a reload or a reopened browser
// would no longer ask for it. Only the derived keys are held, below and outside
// the store state, and lock() drops them.

import { create } from 'zustand';
import type { Category } from '@/lib/connectionCategory';
import {
  pickBackend,
  randomBytes,
  wipeKey,
  type BackendId,
  type Bytes,
  type CipherBackend,
  type VaultKey,
} from '@/lib/vault/backend';
import {
  ID_PREFIX,
  KDF_ITERATIONS,
  MIN_SECRET_KEY_LENGTH,
  SALT_BYTES,
  VAULT_STORAGE_KEY,
  clearVault,
  kdfId,
  openRecord,
  parseVault,
  readVault,
  sealInlineTarget,
  toB64,
  writeVault,
  type SealedRecord,
  type VaultFile,
} from '@/lib/vault/vault';
import type { DBConnConfig, MinioConnConfig, SSHConnConfig } from '@/types/common';

export interface InlineTarget {
  id: string;
  /** UI-only. It is not part of any ConnConfig and is never sent. */
  label: string;
  source: 'ssh' | 'minio' | 'db';
  ssh?: SSHConnConfig;
  minio?: MinioConnConfig;
  db?: DBConnConfig;
}

/**
 * Where a stored inline target stands with the key in hand.
 *
 * `tampered` is a guess: the record failed under a key that opened another
 * record sealed with the same salt, so the key is right and the record is not.
 */
export type InlineTargetStatus = 'unlocked' | 'locked' | 'key-mismatch' | 'tampered' | 'corrupt';

/** One stored inline target as the list draws it — known with or without the key. */
export interface InlineTargetRow {
  id: string;
  label?: string;
  source?: InlineTarget['source'];
  status: InlineTargetStatus;
  /** The clear part as an inline target. Absent only for a corrupt record. */
  preview?: InlineTarget;
  /** Why a corrupt record could not be read. */
  reason?: string;
  fingerprint?: string;
}

/** A beetleDb admin login, which cm-beetle never returns. Memory only, never sealed. */
export interface WizardSecret {
  username: string;
  password: string;
}

interface SecretState {
  /** The vault has been read at least once. Nothing is known before that. */
  ready: boolean;
  /** The vault as a whole cannot be read. Only forgetAll() is offered then. */
  vaultError: string | null;
  backend: BackendId | null;
  /** A key to seal new inline targets with is held. */
  unlocked: boolean;
  /** A key is being derived — a second or more on the noble backend. */
  busy: boolean;
  rows: Record<string, InlineTargetRow>;
  /** The inline targets that opened, decrypted. Only these can be edited or chosen. */
  targets: Record<string, InlineTarget>;

  /**
   * Kept apart from `targets`: these are not inline targets, must not appear in any list
   * of them, and must never reach the vault.
   */
  /** Keyed by targetPasswordKey(category, connId). */
  wizardSecrets: Record<string, WizardSecret>;

  load(): Promise<void>;
  /** Throws on a key that is too short or opens nothing. */
  unlock(pass: string): Promise<void>;
  lock(): Promise<void>;
  /**
   * Re-seals the open inline targets under a new key. Those that are not open
   * keep their old one.
   */
  changeKey(pass: string): Promise<void>;
  /** Deletes the vault. The way out when the key is lost. */
  forgetAll(): Promise<void>;
  put(d: InlineTarget): Promise<void>;
  remove(id: string): Promise<void>;
  get(id: string): InlineTarget | undefined;
  list(): InlineTarget[];

  putWizardSecret(key: string, s: WizardSecret): void;
  /** Every secret one wizard holds, for all of its connections. */
  clearWizardSecret(category: Category): void;
}

/**
 * Where a source connection's beetleDb login is held. Per connection, because
 * each source connection of a group can go to a different managed instance.
 */
export function targetPasswordKey(category: Category, connId: string): string {
  return `${category}\0${connId}`;
}

// ── Session key material — outside the store, so no snapshot of state holds it ──

/** kdfId → a key that opened at least one record, or the key new inline targets are sealed with. */
const keys = new Map<string, VaultKey>();
/** The salt new inline targets are sealed with. Null while locked. */
let writeKdf: { id: string; salt: Bytes; iter: number } | null = null;

function dropKeys() {
  for (const k of keys.values()) wipeKey(k);
  keys.clear();
  writeKdf = null;
}

/** One vault operation at a time, so a slow unlock cannot undo a delete. */
let queue: Promise<unknown> = Promise.resolve();
function serial<T>(fn: () => Promise<T>): Promise<T> {
  const run = queue.then(fn);
  queue = run.catch(() => undefined);
  return run;
}

function readOrThrow(): VaultFile {
  const read = readVault();
  if ('error' in read) throw new Error(read.error);
  return read.file;
}

/**
 * Judges every record against the keys in hand. With `pass`, a key is also
 * derived for each salt no held key covers, and kept if it opens anything.
 */
async function assess(
  backend: CipherBackend,
  file: VaultFile,
  prevRows: Record<string, InlineTargetRow>,
  pass?: string,
): Promise<{ rows: Record<string, InlineTargetRow>; targets: Record<string, InlineTarget> }> {
  const rows: Record<string, InlineTargetRow> = {};
  const targets: Record<string, InlineTarget> = {};

  const groups = new Map<string, SealedRecord[]>();
  for (const rec of parseVault(file)) {
    if (rec.kind === 'corrupt') {
      rows[rec.id] = { ...rec, status: 'corrupt' };
      continue;
    }
    groups.set(rec.kdfId, [...(groups.get(rec.kdfId) ?? []), rec]);
  }

  for (const [id, recs] of groups) {
    const held = keys.get(id);
    const key = held ?? (pass ? await backend.deriveKey(pass, recs[0].salt, recs[0].iter) : null);

    const results = key ? await Promise.all(recs.map((r) => openRecord(backend, key, r))) : null;
    const opened = results?.some((r) => r.ok) ?? false;
    if (key && !held) {
      if (opened) keys.set(id, key);
      else wipeKey(key);
    }

    recs.forEach((rec, i) => {
      const row: InlineTargetRow = {
        id: rec.id,
        label: rec.label,
        source: rec.source,
        preview: rec.preview,
        fingerprint: rec.fingerprint,
        status: 'locked',
      };
      const res = results?.[i];
      if (!res) {
        // No key for this salt. A wrong key said so earlier and the record
        // has not changed since: keep saying it.
        const prev = prevRows[rec.id];
        if (prev?.status === 'key-mismatch' && prev.fingerprint === rec.fingerprint) {
          row.status = 'key-mismatch';
        }
      } else if (res.ok) {
        row.status = 'unlocked';
        targets[rec.id] = res.target;
      } else if (res.kind === 'corrupt') {
        row.status = 'corrupt';
        row.reason = res.reason;
      } else {
        // The tag failed. If this key opened a sibling, the key is right and
        // the record is not; otherwise it is just the wrong key.
        row.status = held || opened ? 'tampered' : 'key-mismatch';
      }
      rows[rec.id] = row;
    });
  }

  return { rows, targets };
}

/**
 * The key bar's one-line count. Derived from the rows on every render rather
 * than stored at unlock, so a save or delete afterwards is reflected at once.
 */
export function vaultSummary(rows: Record<string, InlineTargetRow>): string {
  const all = Object.values(rows);
  if (all.length === 0) return 'no inline targets stored';
  const count = (s: InlineTargetStatus) => all.filter((r) => r.status === s).length;
  const parts = [`${count('unlocked')} of ${all.length} unlocked`];
  if (count('key-mismatch')) parts.push(`${count('key-mismatch')} wrong key`);
  if (count('tampered')) parts.push(`${count('tampered')} tampered`);
  if (count('corrupt')) parts.push(`${count('corrupt')} unreadable`);
  if (count('locked')) parts.push(`${count('locked')} still locked`);
  return parts.join(' · ');
}

export const useSecretStore = create<SecretState>((set, get) => {
  /** Runs a vault operation that may derive a key, with the busy flag up. */
  async function withBackend<T>(fn: (b: CipherBackend) => Promise<T>): Promise<T> {
    set({ busy: true });
    try {
      return await fn(await pickBackend());
    } finally {
      set({ busy: false });
    }
  }

  async function reassess(backend: CipherBackend, file: VaultFile) {
    const next = await assess(backend, file, get().rows);
    set({ ...next, unlocked: writeKdf !== null, vaultError: null });
  }

  return {
    ready: false,
    vaultError: null,
    backend: null,
    unlocked: false,
    busy: false,
    rows: {},
    targets: {},
    wizardSecrets: {},

    load: () =>
      serial(async () => {
        try {
          const backend = await pickBackend();
          set({ backend: backend.id });
          const read = readVault();
          if ('error' in read) {
            set({ ready: true, vaultError: read.error, rows: {}, targets: {} });
            return;
          }
          await reassess(backend, read.file);
        } catch (e) {
          set({ vaultError: e instanceof Error ? e.message : String(e) });
        } finally {
          set({ ready: true });
        }
      }),

    unlock: (pass) =>
      serial(() =>
        withBackend(async (backend) => {
          if (pass.length < MIN_SECRET_KEY_LENGTH) {
            throw new Error(`The secret key must be at least ${MIN_SECRET_KEY_LENGTH} characters.`);
          }
          const file = readOrThrow();
          const next = await assess(backend, file, get().rows, pass);

          if (!writeKdf) {
            // Locked, so every held key was derived just now. Seal new inline targets
            // under the salt that opened the most, so a vault converges on one
            // salt instead of gaining one per session.
            const sealed = parseVault(file).filter((r): r is SealedRecord => r.kind === 'sealed');
            const tally = new Map<string, SealedRecord[]>();
            for (const r of sealed.filter((r) => keys.has(r.kdfId))) {
              tally.set(r.kdfId, [...(tally.get(r.kdfId) ?? []), r]);
            }
            const best = [...tally.values()].sort((a, b) => b.length - a.length)[0];
            if (best) {
              writeKdf = { id: best[0].kdfId, salt: best[0].salt, iter: best[0].iter };
            } else if (sealed.length === 0) {
              // Nothing to check the key against: it becomes the key.
              const salt = randomBytes(SALT_BYTES);
              const id = kdfId(KDF_ITERATIONS, toB64(salt));
              keys.set(id, await backend.deriveKey(pass, salt, KDF_ITERATIONS));
              writeKdf = { id, salt, iter: KDF_ITERATIONS };
            }
          }

          set({ ...next, unlocked: writeKdf !== null, vaultError: null });
          if (!writeKdf) throw new Error('This key opens none of the stored inline targets.');
        }),
      ),

    lock: () =>
      serial(async () => {
        dropKeys();
        const rows: Record<string, InlineTargetRow> = {};
        for (const r of Object.values(get().rows)) {
          rows[r.id] = r.status === 'corrupt' ? r : { ...r, status: 'locked' };
        }
        set({ rows, targets: {}, unlocked: false });
      }),

    changeKey: (pass) =>
      serial(() =>
        withBackend(async (backend) => {
          if (!writeKdf) throw new Error('Unlock the vault before changing its key.');
          if (pass.length < MIN_SECRET_KEY_LENGTH) {
            throw new Error(`The secret key must be at least ${MIN_SECRET_KEY_LENGTH} characters.`);
          }
          const file = readOrThrow();
          // Re-read first: another tab may have saved since this one looked.
          const { targets } = await assess(backend, file, get().rows);

          const salt = randomBytes(SALT_BYTES);
          const key = await backend.deriveKey(pass, salt, KDF_ITERATIONS);
          const kdf = { salt, iter: KDF_ITERATIONS };
          for (const d of Object.values(targets)) {
            file.targets[d.id] = await sealInlineTarget(backend, key, kdf, d);
          }
          writeVault(file);

          dropKeys();
          writeKdf = { id: kdfId(KDF_ITERATIONS, toB64(salt)), ...kdf };
          keys.set(writeKdf.id, key);
          await reassess(backend, file);
        }),
      ),

    forgetAll: () =>
      serial(async () => {
        clearVault();
        dropKeys();
        set({ rows: {}, targets: {}, unlocked: false, vaultError: null });
      }),

    put: (d) =>
      serial(async () => {
        if (!writeKdf) {
          throw new Error('Unlock the inline vault above before saving an inline target.');
        }
        const backend = await pickBackend();
        const file = readOrThrow();
        file.targets[d.id] = await sealInlineTarget(backend, keys.get(writeKdf.id)!, writeKdf, d);
        writeVault(file);
        await reassess(backend, file);
      }),

    remove: (id) =>
      serial(async () => {
        const file = readOrThrow();
        delete file.targets[id];
        writeVault(file);
        await reassess(await pickBackend(), file);
      }),

    get: (id) => get().targets[id],
    list: () => Object.values(get().targets),

    putWizardSecret: (key, s) =>
      set((st) => ({ wizardSecrets: { ...st.wizardSecrets, [key]: s } })),
    clearWizardSecret: (category) =>
      set((st) => {
        const prefix = targetPasswordKey(category, '');
        const next = Object.fromEntries(
          Object.entries(st.wizardSecrets).filter(([k]) => !k.startsWith(prefix)),
        );
        return { wizardSecrets: next };
      }),
  };
});

let booted = false;

/**
 * Reads the vault once and follows it across tabs. Called from an effect: the
 * server render has no localStorage, so reading at import time would draw a
 * different first frame on each side.
 */
export function bootVault(): void {
  if (booted || typeof window === 'undefined') return;
  booted = true;
  void useSecretStore.getState().load();
  // Another tab saved or deleted. The last write wins; this tab re-reads and
  // judges every record again with the keys it holds.
  window.addEventListener('storage', (e) => {
    if (e.key === VAULT_STORAGE_KEY || e.key === null) void useSecretStore.getState().load();
  });
}

export function newInlineTargetId(): string {
  return `${ID_PREFIX}${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}
