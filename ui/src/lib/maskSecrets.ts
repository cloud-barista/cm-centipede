// The plan response carries credentials back, so the Step 4 JSON viewer masks
// them immediately before rendering.

const SECRET_KEYS = new Set([
  'password',
  'privateKey',
  'private_key',
  'secretAccessKey',
  'os_secret_access_key',
  'accessKeyId',
  'os_access_key_id',
  'db_password',
  'credential',
  'tlsCAPem',
  'db_tls_ca_pem',
]);

const MASK = '••••••••';

/** Deep copy with every known secret key replaced. Never mutates the input. */
export function maskSecrets<T>(value: T): T {
  return walk(value) as T;
}

function walk(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(walk);
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = SECRET_KEYS.has(k) && v !== undefined && v !== null && v !== '' ? MASK : walk(v);
    }
    return out;
  }
  return value;
}

/** Pretty-printed, masked JSON for a viewer pane. */
export function maskedJson(value: unknown): string {
  return JSON.stringify(maskSecrets(value), null, 2);
}
