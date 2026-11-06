'use client';

// One field set drives all three categories. What differs between them is
// entirely conditional fields, so the shape comes from lib/sourceFormSchema.ts
// rather than from three near-identical components.

import React from 'react';
import {
  Checkbox,
  Field,
  Note,
  Radio,
  Select,
  TextArea,
  TextInput,
} from '@/components/ui/primitives';
import { connectionFields, isVisible } from '@/lib/sourceFormSchema';
import { ScanTargetFields } from './ScanTargetFields';
import { draftAccessType, type ConnectionDraft } from './connectionDraft';
import {
  DBMS_DEFAULT_PORT,
  DBMS_TYPES,
  TLS_MODES,
  isDefaultDbPort,
  type DBMSType,
} from '@/types/common';
import type { Category } from '@/lib/connectionCategory';

interface Props {
  category: Category;
  provider?: string;
  draft: ConnectionDraft;
  onChange: (patch: Partial<ConnectionDraft>) => void;
}

export function ConnectionFields({ category, provider, draft, onChange }: Props) {
  const accessType = draftAccessType(draft);
  const rules = connectionFields(category, {
    provider,
    dbType: draft.db_type,
    dbName: draft.db_name,
    accessType,
  });
  const [advanced, setAdvanced] = React.useState(false);

  const sshBlock = isVisible(rules.ip_address) && (
    <div className="space-y-3 border-l-2 border-indigo-500/30 pl-4">
      {category !== 'filesystem' && (
        <h5 className="text-2xs font-extrabold tracking-label uppercase text-text-muted">
          Tunnel host
        </h5>
      )}
      <div className="grid md:grid-cols-3 gap-3">
        <Field label="ip_address" required>
          <TextInput
            value={draft.ip_address ?? ''}
            onChange={(e) => onChange({ ip_address: e.target.value })}
            placeholder="ex) 127.0.0.1"
          />
        </Field>
        <Field label="ssh_port" required>
          <TextInput
            value={draft.ssh_port ?? ''}
            onChange={(e) => onChange({ ssh_port: e.target.value })}
            placeholder="ex) 22"
          />
        </Field>
        <Field label="user" required>
          <TextInput
            value={draft.user ?? ''}
            onChange={(e) => onChange({ user: e.target.value })}
            placeholder="ex) ubuntu"
          />
        </Field>
      </div>

      {/* No password, and no choice between the two: honeybee refuses one on
          every type this form registers, because the host is
          also reached by cm-centipede to move the data and rsync there runs ssh
          with BatchMode=yes — nothing to type a password into. */}
      <Field label="private_key" required hint="paste a PEM">
        <TextArea
          rows={4}
          value={draft.private_key ?? ''}
          onChange={(e) => onChange({ private_key: e.target.value })}
          placeholder="ex) -----BEGIN OPENSSH PRIVATE KEY-----"
        />
      </Field>
    </div>
  );

  return (
    <div className="space-y-4">
      <Field label="name" required>
        <TextInput
          value={draft.name}
          onChange={(e) => onChange({ name: e.target.value })}
          placeholder="ex) prod-fs-seoul-web01"
        />
      </Field>

      {category === 'objectstorage' && (
        <Field label="os_access_type">
          <div className="flex gap-6 pt-1">
            <Radio
              name={`os-access-${draft.uid}`}
              value="direct"
              checked={(draft.os_access_type ?? 'direct') === 'direct'}
              onChange={() => onChange({ os_access_type: 'direct' })}
              label="direct"
            />
            <Radio
              name={`os-access-${draft.uid}`}
              value="ssh-tunnel"
              checked={draft.os_access_type === 'ssh-tunnel'}
              onChange={() => onChange({ os_access_type: 'ssh-tunnel' })}
              label="ssh-tunnel"
              hint="installs the agent on the tunnel host"
              // `direct` beside it has no hint, so a hint on its own line would
              // make this option a line taller than its neighbour.
              inlineHint
            />
          </div>
        </Field>
      )}

      {category === 'database' && (
        <>
          <Field label="db_type" required>
            <div className="flex flex-wrap gap-5 pt-1">
              {DBMS_TYPES.map((t) => (
                <Radio
                  key={t}
                  name={`db-type-${draft.uid}`}
                  value={t}
                  checked={draft.db_type === t}
                  onChange={(v) =>
                    onChange({
                      db_type: v,
                      ...(isDefaultDbPort(draft.db_port, draft.db_type) && {
                        db_port: String(DBMS_DEFAULT_PORT[v as DBMSType]),
                      }),
                    })
                  }
                  label={t}
                />
              ))}
            </div>
          </Field>
          <Field label="db_access_type">
            <div className="flex gap-6 pt-1">
              <Radio
                name={`db-access-${draft.uid}`}
                value="direct"
                checked={(draft.db_access_type ?? 'direct') === 'direct'}
                onChange={() => onChange({ db_access_type: 'direct' })}
                label="direct"
              />
              <Radio
                name={`db-access-${draft.uid}`}
                value="ssh-tunnel"
                checked={draft.db_access_type === 'ssh-tunnel'}
                onChange={() => onChange({ db_access_type: 'ssh-tunnel' })}
                label="ssh-tunnel"
              />
            </div>
          </Field>
        </>
      )}

      {sshBlock}

      {category === 'objectstorage' && (
        <div className="grid md:grid-cols-2 gap-3">
          <Field label="os_access_key_id" required hint={provider === 'azure' ? 'storage account name' : undefined}>
            <TextInput
              value={draft.os_access_key_id ?? ''}
              onChange={(e) => onChange({ os_access_key_id: e.target.value })}
            />
          </Field>
          <Field label="os_secret_access_key" required>
            <TextInput
              type="password"
              value={draft.os_secret_access_key ?? ''}
              onChange={(e) => onChange({ os_secret_access_key: e.target.value })}
            />
          </Field>
          {isVisible(rules.os_endpoint) && (
            <Field
              label="os_endpoint"
              required={rules.os_endpoint === 'required'}
              hint={
                rules.os_endpoint === 'ignored'
                  ? 'ignored — azure derives it from os_access_key_id'
                  : 'optional override of the derived host'
              }
            >
              <TextInput
                value={draft.os_endpoint ?? ''}
                onChange={(e) => onChange({ os_endpoint: e.target.value })}
                disabled={rules.os_endpoint === 'ignored'}
                placeholder="ex) http://127.0.0.1:9000"
              />
            </Field>
          )}
          <div className="self-end pb-1">
            <Checkbox
              checked={!!draft.os_use_ssl}
              onChange={(v) => onChange({ os_use_ssl: v })}
              label="os_use_ssl"
              // Three states, not two. `provider ?? …` used to leave the name
              // out entirely, because the unpicked provider is '' — which ??
              // passes straight through — and before one is picked there is no
              // provider to name anyway.
              hint={
                rules.os_use_ssl !== 'ignored'
                  ? 'a scheme in os_endpoint overrides this for every provider'
                  : provider
                    ? `ignored for ${provider} — the CSP sets this itself`
                    : 'honored only for openstack / onprem'
              }
            />
          </div>
        </div>
      )}

      {category === 'database' && (
        <>
          {accessType === 'ssh-tunnel' && (
            <Note>
              <code>db_host</code> is resolved on the tunnel host, not here.{' '}
              <code>127.0.0.1</code> is the normal value for a database that listens only on the
              tunnel host loopback.
            </Note>
          )}
          <div className="grid md:grid-cols-2 gap-3">
            <Field label="db_host" required>
              <TextInput
                value={draft.db_host ?? ''}
                onChange={(e) => onChange({ db_host: e.target.value })}
                placeholder="ex) 127.0.0.1"
              />
            </Field>
            <Field label="db_port" required>
              <TextInput
                value={draft.db_port ?? ''}
                onChange={(e) => onChange({ db_port: e.target.value })}
              />
            </Field>
            <Field label="db_username" required>
              <TextInput
                value={draft.db_username ?? ''}
                onChange={(e) => onChange({ db_username: e.target.value })}
              />
            </Field>
            <Field label="db_password" required>
              <TextInput
                type="password"
                value={draft.db_password ?? ''}
                onChange={(e) => onChange({ db_password: e.target.value })}
              />
            </Field>
            <Field
              label="db_name"
              hint="empty = every user database on the server"
              className="md:col-span-2"
            >
              <TextInput
                value={draft.db_name ?? ''}
                onChange={(e) => onChange({ db_name: e.target.value })}
              />
            </Field>
          </div>

          {isVisible(rules.db_auth_source) && (
            <Field label="db_auth_source" hint="mongodb — empty defaults to “admin”">
              <TextInput
                value={draft.db_auth_source ?? ''}
                // No placeholder: "admin" here is the value the backend falls
                // back to when this is empty, not an example to copy, and the
                // hint beside the label already says so.
                onChange={(e) => onChange({ db_auth_source: e.target.value })}
              />
            </Field>
          )}

          {isVisible(rules.db_pg_schema) && (
            <Field label="db_pg_schema" hint="postgresql — comma separated; empty = every schema">
              <TextInput
                value={(draft.db_pg_schema ?? []).join(', ')}
                onChange={(e) =>
                  onChange({
                    db_pg_schema: e.target.value
                      .split(',')
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                }
                placeholder="ex) public, sales"
              />
            </Field>
          )}

          <button
            type="button"
            onClick={() => setAdvanced((a) => !a)}
            className="text-xs font-mono font-bold text-indigo-600 dark:text-indigo-400 cursor-pointer"
          >
            {advanced ? '▾' : '▸'} advanced — db_connect_timeout / db_tls_mode / db_tls_ca_pem
          </button>

          {advanced && (
            <div className="grid md:grid-cols-2 gap-3">
              <Field label="db_connect_timeout" hint="seconds">
                <TextInput
                  type="number"
                  value={draft.db_connect_timeout ?? ''}
                  onChange={(e) =>
                    onChange({ db_connect_timeout: Number(e.target.value) || undefined })
                  }
                />
              </Field>
              <Field label="db_tls_mode">
                <Select
                  value={draft.db_tls_mode ?? ''}
                  onChange={(e) => onChange({ db_tls_mode: e.target.value })}
                >
                  <option value="">(disable)</option>
                  {TLS_MODES.map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label="db_tls_ca_pem"
                hint="verify-ca / verify-full only"
                className="md:col-span-2"
              >
                <TextArea
                  rows={3}
                  value={draft.db_tls_ca_pem ?? ''}
                  onChange={(e) => onChange({ db_tls_ca_pem: e.target.value })}
                />
              </Field>
            </div>
          )}
        </>
      )}

      <ScanTargetFields
        category={category}
        provider={provider}
        rules={rules}
        draft={draft}
        onChange={onChange}
      />
    </div>
  );
}
