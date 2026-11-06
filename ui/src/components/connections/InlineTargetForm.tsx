'use client';

import React from 'react';
import {
  Button,
  Checkbox,
  ErrorNote,
  Field,
  Note,
  Panel,
  Radio,
  Select,
  TextArea,
  TextInput,
} from '@/components/ui/primitives';
import { PROVIDER_OPTIONS, endpointRule, regionRequired, useSSLRule } from '@/lib/sourceFormSchema';
import {
  DBMS_DEFAULT_PORT,
  DBMS_TYPES,
  TLS_MODES,
  isDefaultDbPort,
  tlsModeVerifies,
  type DBMSType,
  type TLSMode,
} from '@/types/common';
import { emptyInlineTarget, validateInlineTarget } from './inlineTarget';
import { newInlineTargetId, useSecretStore, type InlineTarget } from '@/store/secretStore';

export function InlineTargetForm({
  initial,
  onClose,
}: {
  initial?: InlineTarget;
  onClose: () => void;
}) {
  const put = useSecretStore((s) => s.put);
  const unlocked = useSecretStore((s) => s.unlocked);
  const [saveError, setSaveError] = React.useState<unknown>(null);
  const [target, setTarget] = React.useState<InlineTarget>(
    () => initial ?? emptyInlineTarget(newInlineTargetId(), 'ssh'),
  );

  const set = (patch: Partial<InlineTarget>) => setTarget((d) => ({ ...d, ...patch }));
  const setSsh = (patch: Partial<NonNullable<InlineTarget['ssh']>>) =>
    setTarget((d) => ({ ...d, ssh: { ...d.ssh!, ...patch } }));
  const setMinio = (patch: Partial<NonNullable<InlineTarget['minio']>>) =>
    setTarget((d) => ({ ...d, minio: { ...d.minio!, ...patch } }));
  const setDb = (patch: Partial<NonNullable<InlineTarget['db']>>) =>
    setTarget((d) => ({ ...d, db: { ...d.db!, ...patch } }));
  const setTunnel = (patch: Partial<NonNullable<InlineTarget['ssh']>>) =>
    setTarget((d) => ({
      ...d,
      db: {
        ...d.db!,
        sshTunnel: { host: '', port: 22, username: '', ...d.db!.sshTunnel, ...patch },
      },
    }));

  const problems = validateInlineTarget(target);
  // An empty form is not a form full of mistakes — the same rule the two source
  // forms follow. Nothing is reported until a save has been attempted.
  const [attempted, setAttempted] = React.useState(false);
  const provider = target.minio?.providerName || target.db?.providerName;

  return (
    <Panel className="panel-open mb-4">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-base font-extrabold">
          {initial ? 'Edit inline target' : 'New inline target'}
        </h3>
        <Button onClick={onClose}>close</Button>
      </div>

      <div className="space-y-4">
        <Field label="type">
          <div className="flex gap-6 pt-1">
            {(['ssh', 'minio', 'db'] as const).map((s) => (
              <Radio
                key={s}
                name="inline-type"
                value={s}
                checked={target.source === s}
                onChange={() => setTarget(emptyInlineTarget(target.id, s))}
                label={s}
              />
            ))}
          </div>
        </Field>

        <Field
          label="label"
          hint="UI only — not sent to centipede, it names this inline target in the list"
        >
          <TextInput value={target.label} onChange={(e) => set({ label: e.target.value })} />
        </Field>

        {target.source === 'ssh' && (
          <div className="grid md:grid-cols-2 gap-3">
            <Field label="host" required>
              <TextInput
                value={target.ssh!.host}
                onChange={(e) => setSsh({ host: e.target.value })}
                placeholder="ex) 127.0.0.1"
              />
            </Field>
            <Field label="port">
              <TextInput
                type="number"
                value={target.ssh!.port ?? 22}
                onChange={(e) => setSsh({ port: Number(e.target.value) || undefined })}
              />
            </Field>
            <Field label="username" required>
              <TextInput
                value={target.ssh!.username}
                onChange={(e) => setSsh({ username: e.target.value })}
                placeholder="ex) ubuntu"
              />
            </Field>
            {/* No password field: the server's SSHConnConfig has none, and
                rsync runs ssh with BatchMode=yes, so a key is the only
                credential an ssh inline target can carry. */}
            <Field label="privateKey" required hint="paste a PEM" className="md:col-span-2">
              <TextArea
                rows={3}
                value={target.ssh!.privateKey ?? ''}
                onChange={(e) => setSsh({ privateKey: e.target.value })}
                placeholder="ex) -----BEGIN OPENSSH PRIVATE KEY-----"
              />
            </Field>
          </div>
        )}

        {target.source === 'minio' && (
          <>
            <div className="grid md:grid-cols-2 gap-3">
              <Field label="providerName" required>
                <Select
                  value={target.minio!.providerName}
                  onChange={(e) => setMinio({ providerName: e.target.value as never })}
                >
                  <option value="">select…</option>
                  {PROVIDER_OPTIONS.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="region"
                required={regionRequired(provider)}
                hint={regionRequired(provider) ? `required for ${provider}` : undefined}
              >
                <TextInput
                  value={target.minio!.region ?? ''}
                  onChange={(e) => setMinio({ region: e.target.value })}
                />
              </Field>
              <Field
                label="endpoint"
                required={endpointRule(provider) === 'required'}
                hint={
                  endpointRule(provider) === 'ignored'
                    ? 'ignored — azure derives it from accessKeyId'
                    : 'derived from the provider when empty'
                }
              >
                <TextInput
                  value={target.minio!.endpoint ?? ''}
                  onChange={(e) => setMinio({ endpoint: e.target.value })}
                  disabled={endpointRule(provider) === 'ignored'}
                  placeholder="ex) http://127.0.0.1:9000"
                />
              </Field>
              <Field label="bucketLookup">
                <Select
                  value={target.minio!.bucketLookup ?? ''}
                  onChange={(e) => setMinio({ bucketLookup: e.target.value as '' | 'dns' | 'path' })}
                >
                  <option value="">auto</option>
                  <option value="dns">dns</option>
                  <option value="path">path</option>
                </Select>
              </Field>
              <Field label="accessKeyId" required>
                <TextInput
                  value={target.minio!.accessKeyId}
                  onChange={(e) => setMinio({ accessKeyId: e.target.value })}
                />
              </Field>
              <Field label="secretAccessKey" required>
                <TextInput
                  type="password"
                  value={target.minio!.secretAccessKey}
                  onChange={(e) => setMinio({ secretAccessKey: e.target.value })}
                />
              </Field>
            </div>
            <Checkbox
              checked={!!target.minio!.useSSL}
              onChange={(v) => setMinio({ useSSL: v })}
              label="useSSL"
              hint={
                useSSLRule(provider) !== 'ignored'
                  ? 'a scheme in endpoint wins over this'
                  : provider
                    ? `ignored for ${provider} — the CSP sets this itself`
                    : 'honored only for openstack / onprem'
              }
            />
          </>
        )}

        {target.source === 'db' && (
          <>
            <div className="grid md:grid-cols-2 gap-3">
              <Field label="providerName" required>
                <Select
                  value={target.db!.providerName}
                  onChange={(e) => setDb({ providerName: e.target.value as never })}
                >
                  <option value="">select…</option>
                  {PROVIDER_OPTIONS.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label="dbmsType" required>
                <Select
                  value={target.db!.dbmsType}
                  onChange={(e) => {
                    const dbmsType = e.target.value as DBMSType;
                    setDb({
                      dbmsType,
                      ...(isDefaultDbPort(target.db!.port, target.db!.dbmsType) && {
                        port: DBMS_DEFAULT_PORT[dbmsType],
                      }),
                    });
                  }}
                >
                  {DBMS_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>

            <Field label="accessType">
              <div className="flex gap-6 pt-1">
                <Radio
                  name="inline-db-access"
                  value="direct"
                  checked={target.db!.accessType === 'direct'}
                  onChange={() => setDb({ accessType: 'direct' })}
                  label="direct"
                />
                <Radio
                  name="inline-db-access"
                  value="sshTunnel"
                  checked={target.db!.accessType === 'sshTunnel'}
                  onChange={() => setDb({ accessType: 'sshTunnel' })}
                  label="sshTunnel"
                />
              </div>
            </Field>

            {target.db!.accessType === 'sshTunnel' && (
              <div className="border-l-2 border-indigo-500/30 pl-4 space-y-3">
                <h5 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
                  Tunnel host
                </h5>
                <div className="grid md:grid-cols-2 gap-3">
                  <Field label="host" required>
                    <TextInput
                      value={target.db!.sshTunnel?.host ?? ''}
                      onChange={(e) => setTunnel({ host: e.target.value })}
                      placeholder="ex) 127.0.0.1"
                    />
                  </Field>
                  <Field label="port">
                    <TextInput
                      type="number"
                      value={target.db!.sshTunnel?.port ?? 22}
                      onChange={(e) => setTunnel({ port: Number(e.target.value) || undefined })}
                    />
                  </Field>
                  <Field label="username" required>
                    <TextInput
                      value={target.db!.sshTunnel?.username ?? ''}
                      onChange={(e) => setTunnel({ username: e.target.value })}
                      placeholder="ex) ubuntu"
                    />
                  </Field>
                  {/* Key only, like an ssh inline target: the tunnel is the same
                      SSHConnConfig, which has no password on the server. */}
                  <Field label="privateKey" required className="md:col-span-2">
                    <TextArea
                      rows={3}
                      value={target.db!.sshTunnel?.privateKey ?? ''}
                      onChange={(e) => setTunnel({ privateKey: e.target.value })}
                      placeholder="ex) -----BEGIN OPENSSH PRIVATE KEY-----"
                    />
                  </Field>
                </div>
              </div>
            )}

            <div className="grid md:grid-cols-2 gap-3">
              <Field
                label="host"
                required
                hint={target.db!.accessType === 'sshTunnel' ? 'as seen FROM the tunnel host' : undefined}
              >
                <TextInput
                  value={target.db!.host}
                  onChange={(e) => setDb({ host: e.target.value })}
                  placeholder="ex) 127.0.0.1"
                />
              </Field>
              <Field label="port" required>
                <TextInput
                  type="number"
                  value={target.db!.port ?? ''}
                  onChange={(e) => setDb({ port: Number(e.target.value) || undefined })}
                />
              </Field>
              <Field label="username" required>
                <TextInput
                  value={target.db!.username ?? ''}
                  onChange={(e) => setDb({ username: e.target.value })}
                />
              </Field>
              <Field label="password" required>
                <TextInput
                  type="password"
                  value={target.db!.password ?? ''}
                  onChange={(e) => setDb({ password: e.target.value })}
                />
              </Field>
              <Field label="tlsMode">
                <Select
                  value={target.db!.tlsMode ?? ''}
                  onChange={(e) => {
                    const tlsMode = (e.target.value || undefined) as TLSMode | undefined;
                    // Leaving a verifying mode drops the CA with it. Kept, it
                    // would ride along invisibly in an inline target whose mode no longer
                    // checks anything.
                    setDb(
                      tlsModeVerifies(tlsMode)
                        ? { tlsMode }
                        : { tlsMode, tlsCAPem: undefined },
                    );
                  }}
                >
                  <option value="">(disable)</option>
                  {TLS_MODES.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </Select>
              </Field>
              {tlsModeVerifies(target.db!.tlsMode) && (
                <Field label="tlsCAPem" className="md:col-span-2">
                  <TextArea
                    rows={3}
                    value={target.db!.tlsCAPem ?? ''}
                    onChange={(e) => setDb({ tlsCAPem: e.target.value })}
                    placeholder="ex) -----BEGIN CERTIFICATE-----"
                  />
                </Field>
              )}
              {target.db!.dbmsType === 'mongodb' && (
                <Field label="authSource" hint="empty defaults to “admin”">
                  <TextInput
                    value={target.db!.authSource ?? ''}
                    onChange={(e) => setDb({ authSource: e.target.value })}
                  />
                </Field>
              )}
            </div>

            <Note>
              No database name here. The databases to migrate come from the Step 3 filter, not from
              the connection.
            </Note>
          </>
        )}

        {attempted && problems.length > 0 && (
          <ul className="text-xs text-amber-500 dark:text-amber-400 space-y-0.5">
            {problems.map((p) => (
              <li key={p}>· {p}</li>
            ))}
          </ul>
        )}

        {/* Typing may start before the key is in; only sealing needs it. */}
        {attempted && problems.length === 0 && !unlocked && (
          <Note tone="warn">Enter the secret key in the bar above, then save again.</Note>
        )}
        <ErrorNote error={saveError} />

        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>cancel</Button>
          {/* Enabled even while the inline target is incomplete: with the problem list
              hidden until now, a disabled button would refuse to say what it is
              waiting for. Pressing it is what asks. */}
          <Button
            variant="primary"
            onClick={async () => {
              setAttempted(true);
              setSaveError(null);
              if (problems.length > 0 || !unlocked) return;
              try {
                await put(target);
                onClose();
              } catch (e) {
                setSaveError(e);
              }
            }}
          >
            save inline target
          </Button>
        </div>
      </div>
    </Panel>
  );
}
