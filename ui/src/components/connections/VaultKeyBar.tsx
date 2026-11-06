'use client';

// The secret key for the inline vault. It is typed here, turned into a key, and
// dropped: nothing remembers it, so every reload asks again.

import React from 'react';
import { Lock, RefreshCw } from 'lucide-react';
import { Button, ErrorNote, Note, Panel, TextInput } from '@/components/ui/primitives';
import { useSecretStore, vaultSummary } from '@/store/secretStore';
import { MIN_SECRET_KEY_LENGTH } from '@/lib/vault/vault';

type Mode = 'idle' | 'change';

export function VaultKeyBar() {
  const ready = useSecretStore((s) => s.ready);
  const vaultError = useSecretStore((s) => s.vaultError);
  const backend = useSecretStore((s) => s.backend);
  const unlocked = useSecretStore((s) => s.unlocked);
  const busy = useSecretStore((s) => s.busy);
  const rows = useSecretStore((s) => s.rows);
  const unlock = useSecretStore((s) => s.unlock);
  const lock = useSecretStore((s) => s.lock);
  const changeKey = useSecretStore((s) => s.changeKey);
  const forgetAll = useSecretStore((s) => s.forgetAll);

  const [pass, setPass] = React.useState('');
  const [repeat, setRepeat] = React.useState('');
  const [mode, setMode] = React.useState<Mode>('idle');
  const [error, setError] = React.useState<unknown>(null);

  const all = Object.values(rows);
  // Nothing sealed to check a key against: whatever is typed becomes the key,
  // so it is typed twice.
  const firstKey = !unlocked && all.every((r) => r.status === 'corrupt');
  const newKey = firstKey || mode === 'change';
  const stillSealed = all.filter((r) => r.status === 'locked' || r.status === 'key-mismatch');
  // While locked, say nothing until a key has been tried: "0 of 3 unlocked"
  // before anyone typed is noise.
  const tried = all.some((r) => r.status !== 'locked' && r.status !== 'corrupt');
  const summary = unlocked || tried ? vaultSummary(rows) : null;

  const tooShort = pass.length < MIN_SECRET_KEY_LENGTH;
  const mismatch = newKey && pass !== repeat;

  function clearInputs() {
    setPass('');
    setRepeat('');
  }

  async function run(fn: () => Promise<void>) {
    setError(null);
    try {
      await fn();
      clearInputs();
      setMode('idle');
    } catch (e) {
      setError(e);
    }
  }

  function confirmForget() {
    if (
      confirm(
        'Delete every inline target stored in this browser? Use this when the secret key is lost — it cannot be undone.',
      )
    ) {
      void run(forgetAll);
    }
  }

  if (!ready) return null;

  if (vaultError) {
    return (
      <Note tone="bad">
        <div className="flex flex-wrap items-center gap-3">
          <span>The inline vault cannot be read: {vaultError}</span>
          <Button variant="danger" onClick={confirmForget}>
            forget all
          </Button>
        </div>
      </Note>
    );
  }

  const keyForm = (submitLabel: string, onSubmit: () => Promise<void>) => (
    <form
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        if (!tooShort && !mismatch) void run(onSubmit);
      }}
    >
      <TextInput
        type="password"
        autoComplete="off"
        placeholder={newKey ? `new secret key (min ${MIN_SECRET_KEY_LENGTH})` : 'secret key'}
        value={pass}
        onChange={(e) => setPass(e.target.value)}
        className="w-56"
        disabled={busy}
      />
      {newKey && (
        <TextInput
          type="password"
          autoComplete="off"
          placeholder="repeat"
          value={repeat}
          onChange={(e) => setRepeat(e.target.value)}
          className="w-56"
          disabled={busy}
        />
      )}
      <Button type="submit" variant="primary" disabled={busy || tooShort || mismatch}>
        {submitLabel}
      </Button>
    </form>
  );

  return (
    <Panel className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <Lock className="w-4 h-4 text-text-muted" />
        {unlocked ? (
          <>
            <span className="text-xs font-bold">unlocked</span>
            {summary && <span className="text-xs text-text-muted">{summary}</span>}
            <div className="ml-auto flex gap-2">
              <Button onClick={() => void run(lock)} disabled={busy}>
                lock
              </Button>
              <Button
                onClick={() => {
                  clearInputs();
                  setMode(mode === 'change' ? 'idle' : 'change');
                }}
                disabled={busy}
              >
                change key
              </Button>
              <Button variant="danger" onClick={confirmForget} disabled={busy}>
                forget all
              </Button>
            </div>
          </>
        ) : (
          <>
            {keyForm(firstKey ? 'set key' : 'unlock', () => unlock(pass))}
            {summary && <span className="text-xs text-text-muted">{summary}</span>}
            {all.length > 0 && (
              <Button variant="danger" className="ml-auto" onClick={confirmForget} disabled={busy}>
                forget all
              </Button>
            )}
          </>
        )}
      </div>

      {unlocked && mode === 'change' && (
        <div className="space-y-1">
          {keyForm('apply', () => changeKey(pass))}
          <p className="text-xs text-text-muted">
            Re-seals the unlocked inline targets under the new key. Those that are not unlocked keep
            their old key.
          </p>
        </div>
      )}

      {/* An inline target sealed under some other key: let that key be tried without
          locking the ones already open. */}
      {unlocked && mode === 'idle' && stillSealed.length > 0 && (
        <div className="space-y-1">
          {keyForm('try key', () => unlock(pass))}
          <p className="text-xs text-text-muted">
            {stillSealed.length} inline target{stillSealed.length === 1 ? ' is' : 's are'} sealed
            under another key.
          </p>
        </div>
      )}

      {busy && (
        <div className="flex items-center gap-2 text-xs text-text-muted">
          <RefreshCw className="w-3.5 h-3.5 animate-spin" />
          deriving the key…
        </div>
      )}
      {newKey && pass && tooShort && (
        <p className="text-xs text-text-muted">
          At least {MIN_SECRET_KEY_LENGTH} characters.
        </p>
      )}
      {newKey && repeat && mismatch && !tooShort && (
        <p className="text-xs text-amber-500 dark:text-amber-400">The two entries differ.</p>
      )}
      <ErrorNote error={error} />

      {backend === 'noble' && (
        <p className="text-xs text-text-muted">
          This page is not served over HTTPS, so the browser offers no native crypto here; a
          JavaScript implementation of the same cipher is used instead.
        </p>
      )}
    </Panel>
  );
}
