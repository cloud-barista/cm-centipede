// The native implementation. The derived key is non-extractable: this page can
// encrypt and decrypt with it but can never read its bytes.

import type { Bytes, CipherBackend, VaultKey } from './backend';

function cryptoKey(k: VaultKey): CryptoKey {
  if (k.backend !== 'webcrypto') throw new Error('vault key belongs to another backend');
  return k.key;
}

export const webcryptoBackend: CipherBackend = {
  id: 'webcrypto',

  async deriveKey(pass, salt, iter) {
    const subtle = globalThis.crypto.subtle;
    const base = await subtle.importKey('raw', new TextEncoder().encode(pass), 'PBKDF2', false, [
      'deriveKey',
    ]);
    const key = await subtle.deriveKey(
      { name: 'PBKDF2', hash: 'SHA-256', salt, iterations: iter },
      base,
      { name: 'AES-GCM', length: 256 },
      false,
      ['encrypt', 'decrypt'],
    );
    return { backend: 'webcrypto', key };
  },

  async encrypt(k, iv, pt, aad) {
    const ct = await globalThis.crypto.subtle.encrypt(
      { name: 'AES-GCM', iv, additionalData: aad, tagLength: 128 },
      cryptoKey(k),
      pt,
    );
    return new Uint8Array(ct) as Bytes;
  },

  async decrypt(k, iv, ct, aad) {
    const pt = await globalThis.crypto.subtle.decrypt(
      { name: 'AES-GCM', iv, additionalData: aad, tagLength: 128 },
      cryptoKey(k),
      ct,
    );
    return new Uint8Array(pt) as Bytes;
  },
};
