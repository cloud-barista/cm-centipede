// The inline targets as they sit in localStorage.
//
// Each record keeps what the list needs to draw a row — label, type, address,
// account — in the clear, and seals the credentials under the secret key. Rows
// therefore still show while the vault is locked or the key is wrong, and the
// list can say which of the two it is.
//
// Every record carries its own KDF parameters, so records sealed under
// different keys can sit side by side and each is judged on its own. The clear
// part is bound to the sealed part as AAD: moving `enc` between records, or
// editing a host in place, makes the record fail to open.

import type { InlineTarget } from '@/store/secretStore';
import type { Bytes, CipherBackend, VaultKey } from './backend';

export const VAULT_STORAGE_KEY = 'centipede-inline-vault';
/** The file's shape. v1 kept the records under `drafts`. */
export const VAULT_VERSION = 2;
/**
 * The records' shape, bound into each one's AAD. Apart from VAULT_VERSION so
 * the file can change around records sealed earlier without unsealing them.
 */
export const RECORD_VERSION = 1;
/** Every record id starts with it. v1's were `draft-`. */
export const ID_PREFIX = 'inline-';
export const KDF_ITERATIONS = 600_000;
export const MIN_SECRET_KEY_LENGTH = 12;
export const SALT_BYTES = 16;
export const IV_BYTES = 12;

const CIPHER_ALG = 'A256GCM';
const KDF_ALG = 'PBKDF2-SHA256';
/** A record cannot ask for more than this — an edited count would hang the page. */
const MAX_ITERATIONS = 10_000_000;
const TAG_BYTES = 16;

type Source = InlineTarget['source'];
const SOURCES: readonly Source[] = ['ssh', 'minio', 'db'];

/**
 * What gets sealed, per type, as paths into the type's config. Everything else
 * stays in the clear. Listing the secrets rather than the clear fields means a
 * field added to a config later lands in the clear visibly, instead of
 * vanishing from the record.
 */
const SECRET_PATHS: Record<Source, string[][]> = {
  ssh: [['privateKey']],
  // accessKeyId too: it is half of a credential pair, and the plan viewer and
  // the list both treat it as secret already.
  minio: [['accessKeyId'], ['secretAccessKey']],
  // tlsCAPem is a public root, but maskSecrets hides it and it is long.
  db: [['password'], ['tlsCAPem'], ['sshTunnel', 'privateKey']],
};

// ── Stored shape ────────────────────────────────────────────────────────────

export interface KdfParams {
  alg: typeof KDF_ALG;
  iter: number;
  salt: string;
}

export interface StoredRecord {
  id: string;
  label: string;
  source: Source;
  public: Record<string, unknown>;
  enc: {
    alg: typeof CIPHER_ALG;
    kdf: KdfParams;
    iv: string;
    ct: string;
  };
}

/** Records are kept as raw JSON: one that cannot be read is carried, not dropped. */
export interface VaultFile {
  v: typeof VAULT_VERSION;
  targets: Record<string, unknown>;
}

// ── Parsed shape ────────────────────────────────────────────────────────────

/** A record whose framing reads. Whether it opens is a separate question. */
export interface SealedRecord {
  kind: 'sealed';
  id: string;
  label: string;
  source: Source;
  /** The clear part as an inline target, for drawing a row without the key. */
  preview: InlineTarget;
  /** Identifies the derived key this record needs: same id, same key. */
  kdfId: string;
  salt: Bytes;
  iter: number;
  iv: Bytes;
  ct: Bytes;
  aad: Bytes;
  /** Every stored byte of the record, to tell whether a re-read one changed. */
  fingerprint: string;
}

export interface CorruptRecord {
  kind: 'corrupt';
  id: string;
  label?: string;
  source?: Source;
  reason: string;
}

export type ParsedRecord = SealedRecord | CorruptRecord;

export type OpenResult =
  | { ok: true; target: InlineTarget }
  /** The tag did not verify: a wrong key, or the record was altered. */
  | { ok: false; kind: 'auth' }
  /** Opened, but what came out is not a sealed inline target. */
  | { ok: false; kind: 'corrupt'; reason: string };

// ── localStorage ────────────────────────────────────────────────────────────

export function emptyVault(): VaultFile {
  return { v: VAULT_VERSION, targets: {} };
}

/** A missing vault is an empty one; an unreadable one is an error, never overwritten silently. */
export function readVault(): { file: VaultFile } | { error: string } {
  let text: string | null;
  try {
    text = globalThis.localStorage.getItem(VAULT_STORAGE_KEY);
  } catch {
    return { error: 'localStorage is not available in this browser.' };
  }
  if (text === null) return { file: emptyVault() };
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { error: 'The stored vault is not valid JSON.' };
  }
  if (!isObject(raw) || !isObject(raw.targets)) {
    return { error: 'The stored vault has an unknown shape.' };
  }
  if (raw.v !== VAULT_VERSION) return { error: `The stored vault has unknown version ${String(raw.v)}.` };
  return { file: { v: VAULT_VERSION, targets: raw.targets } };
}

export function writeVault(file: VaultFile): void {
  globalThis.localStorage.setItem(VAULT_STORAGE_KEY, JSON.stringify(file));
}

export function clearVault(): void {
  globalThis.localStorage.removeItem(VAULT_STORAGE_KEY);
}

// ── Records ─────────────────────────────────────────────────────────────────

export function kdfId(iter: number, salt: string): string {
  return `${iter}:${salt}`;
}

export function parseRecord(id: string, raw: unknown): ParsedRecord {
  const corrupt = (reason: string): CorruptRecord => ({
    kind: 'corrupt',
    id,
    label: isObject(raw) && typeof raw.label === 'string' ? raw.label : undefined,
    source: isObject(raw) && isSource(raw.source) ? raw.source : undefined,
    reason,
  });

  if (!isObject(raw)) return corrupt('not an object');
  if (raw.id !== id) return corrupt('id does not match its key');
  if (typeof raw.label !== 'string') return corrupt('label is missing');
  if (!isSource(raw.source)) return corrupt('unknown source');
  if (!isObject(raw.public)) return corrupt('public part is missing');

  const enc = raw.enc;
  if (!isObject(enc) || enc.alg !== CIPHER_ALG) return corrupt('unknown cipher');
  const kdf = enc.kdf;
  if (!isObject(kdf) || kdf.alg !== KDF_ALG) return corrupt('unknown key derivation');
  if (
    typeof kdf.iter !== 'number' ||
    !Number.isInteger(kdf.iter) ||
    kdf.iter < 1 ||
    kdf.iter > MAX_ITERATIONS
  ) {
    return corrupt('bad iteration count');
  }
  if (typeof kdf.salt !== 'string' || typeof enc.iv !== 'string' || typeof enc.ct !== 'string') {
    return corrupt('salt, iv or ct is missing');
  }

  const salt = fromB64(kdf.salt);
  const iv = fromB64(enc.iv);
  const ct = fromB64(enc.ct);
  if (!salt || salt.length < SALT_BYTES) return corrupt('bad salt');
  if (!iv || iv.length !== IV_BYTES) return corrupt('bad iv');
  if (!ct || ct.length < TAG_BYTES) return corrupt('bad ciphertext');

  const pub = raw.public;
  const source = raw.source;
  const aad = aadFor(id, raw.label, source, pub);
  return {
    kind: 'sealed',
    id,
    label: raw.label,
    source,
    preview: { id, label: raw.label, source, [source]: clone(pub) } as InlineTarget,
    kdfId: kdfId(kdf.iter, kdf.salt),
    salt,
    iter: kdf.iter,
    iv,
    ct,
    aad,
    fingerprint: `${kdfId(kdf.iter, kdf.salt)}.${enc.iv}.${enc.ct}.${toB64(aad)}`,
  };
}

export function parseVault(file: VaultFile): ParsedRecord[] {
  return Object.entries(file.targets).map(([id, raw]) => parseRecord(id, raw));
}

export async function openRecord(
  backend: CipherBackend,
  key: VaultKey,
  rec: SealedRecord,
): Promise<OpenResult> {
  let pt: Bytes;
  try {
    pt = await backend.decrypt(key, rec.iv, rec.ct, rec.aad);
  } catch {
    // OperationError from Web Crypto, "invalid tag" from noble: one meaning.
    return { ok: false, kind: 'auth' };
  }
  let secret: unknown;
  try {
    secret = JSON.parse(new TextDecoder().decode(pt));
  } catch {
    return { ok: false, kind: 'corrupt', reason: 'sealed part is not JSON' };
  } finally {
    pt.fill(0);
  }
  if (!isObject(secret)) return { ok: false, kind: 'corrupt', reason: 'sealed part is not an object' };

  const target = clone(rec.preview);
  target[rec.source] = mergeDeep(
    (target[rec.source] ?? {}) as Record<string, unknown>,
    secret,
  ) as never;
  return { ok: true, target };
}

export async function sealInlineTarget(
  backend: CipherBackend,
  key: VaultKey,
  kdf: { salt: Bytes; iter: number },
  target: InlineTarget,
): Promise<StoredRecord> {
  const { pub, secret } = splitSecrets(
    (target[target.source] ?? {}) as Record<string, unknown>,
    SECRET_PATHS[target.source],
  );
  // A fresh IV on every save: GCM must never see the same IV twice under one key.
  const iv = globalThis.crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const pt = new TextEncoder().encode(JSON.stringify(secret));
  const ct = await backend.encrypt(key, iv, pt, aadFor(target.id, target.label, target.source, pub));
  pt.fill(0);
  return {
    id: target.id,
    label: target.label,
    source: target.source,
    public: pub,
    enc: {
      alg: CIPHER_ALG,
      kdf: { alg: KDF_ALG, iter: kdf.iter, salt: toB64(kdf.salt) },
      iv: toB64(iv),
      ct: toB64(ct),
    },
  };
}

// ── Helpers ─────────────────────────────────────────────────────────────────

/** Everything in the clear goes into the AAD, so none of it can be edited unseen. */
function aadFor(id: string, label: string, source: Source, pub: Record<string, unknown>): Bytes {
  return new TextEncoder().encode(
    canonicalJson({ v: RECORD_VERSION, id, label, source, public: pub }),
  );
}

/** JSON with keys sorted at every level, so equal values give equal bytes. */
export function canonicalJson(value: unknown): string {
  return JSON.stringify(sortKeys(value));
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortKeys);
  if (!isObject(value)) return value;
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(value).sort()) {
    if (value[k] !== undefined) out[k] = sortKeys(value[k]);
  }
  return out;
}

function splitSecrets(
  cfg: Record<string, unknown>,
  paths: string[][],
): { pub: Record<string, unknown>; secret: Record<string, unknown> } {
  const pub = clone(cfg);
  const secret: Record<string, unknown> = {};
  for (const path of paths) {
    let from: unknown = pub;
    for (const step of path.slice(0, -1)) from = isObject(from) ? from[step] : undefined;
    const leaf = path[path.length - 1];
    if (!isObject(from) || !(leaf in from)) continue;

    let to = secret;
    for (const step of path.slice(0, -1)) {
      if (!isObject(to[step])) to[step] = {};
      to = to[step] as Record<string, unknown>;
    }
    to[leaf] = from[leaf];
    delete from[leaf];
  }
  return { pub, secret };
}

function mergeDeep(base: Record<string, unknown>, extra: Record<string, unknown>) {
  for (const [k, v] of Object.entries(extra)) {
    base[k] = isObject(v) && isObject(base[k]) ? mergeDeep(base[k], v) : v;
  }
  return base;
}

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T;
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isSource(v: unknown): v is Source {
  return SOURCES.includes(v as Source);
}

export function toB64(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

/** null on anything that is not base64. */
export function fromB64(text: string): Bytes | null {
  try {
    const s = atob(text);
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}
