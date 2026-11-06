// The cipher behind the inline vault. Two implementations of one algorithm:
// PBKDF2-SHA256 for the key, AES-256-GCM (12-byte IV, 128-bit tag) for the
// data. Same inputs, same bytes, so a vault written over HTTPS opens over plain
// http://<IP> and back.
//
// crypto.subtle exists only in a secure context (HTTPS or localhost). Anywhere
// else the noble implementation stands in, loaded on demand so that a secure
// context never ships it.

import { webcryptoBackend } from './webcrypto';

/** Bytes backed by a plain ArrayBuffer, which is what BufferSource accepts. */
export type Bytes = Uint8Array<ArrayBuffer>;

export type BackendId = 'webcrypto' | 'noble';

export type VaultKey =
  | { backend: 'webcrypto'; key: CryptoKey }
  | { backend: 'noble'; key: Bytes };

export interface CipherBackend {
  id: BackendId;
  deriveKey(pass: string, salt: Bytes, iter: number): Promise<VaultKey>;
  /** Returns ciphertext with the tag appended, as Web Crypto does. */
  encrypt(k: VaultKey, iv: Bytes, pt: Bytes, aad: Bytes): Promise<Bytes>;
  /** Throws when the tag does not verify — a wrong key or altered data. */
  decrypt(k: VaultKey, iv: Bytes, ct: Bytes, aad: Bytes): Promise<Bytes>;
}

let picked: Promise<CipherBackend> | null = null;

export function pickBackend(): Promise<CipherBackend> {
  if (!picked) {
    picked =
      globalThis.isSecureContext && globalThis.crypto?.subtle
        ? Promise.resolve(webcryptoBackend)
        : import('./noble').then((m) => m.nobleBackend);
  }
  return picked;
}

/**
 * Overwrites a noble key in place. A CryptoKey has no bytes this page can reach,
 * so there is nothing to wipe on that side.
 */
export function wipeKey(k: VaultKey): void {
  if (k.backend === 'noble') k.key.fill(0);
}

/** Works without a secure context, unlike crypto.subtle. */
export function randomBytes(n: number): Bytes {
  return globalThis.crypto.getRandomValues(new Uint8Array(n));
}
