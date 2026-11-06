// The JS implementation, for pages served outside a secure context. The key is
// an ordinary byte array here: wipeKey() zeroes it on lock, but until then any
// script on the page could read it — the same script that could read the
// decrypted inline targets or the typed secret key, so the threat model is unchanged.

import { gcm } from '@noble/ciphers/aes.js';
import { pbkdf2Async } from '@noble/hashes/pbkdf2.js';
import { sha256 } from '@noble/hashes/sha2.js';
import type { Bytes, CipherBackend, VaultKey } from './backend';

function rawKey(k: VaultKey): Bytes {
  if (k.backend !== 'noble') throw new Error('vault key belongs to another backend');
  return k.key;
}

export const nobleBackend: CipherBackend = {
  id: 'noble',

  async deriveKey(pass, salt, iter) {
    // asyncTick yields to the event loop, so the page keeps painting through
    // the second or so this takes.
    const key = await pbkdf2Async(sha256, pass, salt, { c: iter, dkLen: 32, asyncTick: 10 });
    return { backend: 'noble', key: key as Bytes };
  },

  async encrypt(k, iv, pt, aad) {
    return gcm(rawKey(k), iv, aad).encrypt(pt) as Bytes;
  },

  async decrypt(k, iv, ct, aad) {
    return gcm(rawKey(k), iv, aad).decrypt(ct) as Bytes;
  },
};
