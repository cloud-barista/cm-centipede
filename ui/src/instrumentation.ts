// Runs once when the Next.js server starts (dev and start alike, not at build).
//
// The Basic Auth credentials the proxies inject ship as ChangeMe — in
// .env.example and as the routes' fallback when a variable is unset. A server
// left on those would only fail later, request by request, with a 401 from the
// backend, so it refuses to start instead and names what to change.

const PLACEHOLDER = 'changeme';

const CREDENTIALS = [
  'CENTIPEDE_API_USERNAME',
  'CENTIPEDE_API_PASSWORD',
  'BEETLE_API_USERNAME',
  'BEETLE_API_PASSWORD',
] as const;

export function register() {
  // An unset variable counts: the route falls back to ChangeMe for it.
  const unchanged = CREDENTIALS.filter(
    (name) => (process.env[name] ?? PLACEHOLDER).trim().toLowerCase() === PLACEHOLDER,
  );
  if (unchanged.length > 0) {
    throw new Error(
      `${unchanged.join(', ')} still ${unchanged.length === 1 ? 'reads' : 'read'} ChangeMe. ` +
        'Set the real value in .env.local (or the environment) and start again.',
    );
  }
}
