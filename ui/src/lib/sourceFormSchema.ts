// Field rules for the "New Source" form, kept as data so one component renders
// all three categories. The provider table below follows the rules cm-honeybee
// applies to region and endpoint.

import type { Category } from './connectionCategory';
import type { ProviderName } from '@/types/common';

export type FieldRule = 'required' | 'optional' | 'hidden' | 'ignored';

export type FieldMap = Record<string, FieldRule>;

export type HoneybeeAccess = 'direct' | 'ssh-tunnel';

/** Providers whose hostname or signature carries the region. */
const REGION_REQUIRED = new Set<string>([
  'aws',
  'alibaba',
  'tencent',
  'ncp',
  'nhn',
  'ibm',
  'gcp',
]);

/** Providers where the caller supplies the endpoint host. */
const ENDPOINT_REQUIRED = new Set<string>(['openstack', 'onprem']);

/** Providers where useSSL is honored rather than taken from the provider table. */
const SSL_HONORED = ENDPOINT_REQUIRED;

export function regionRequired(provider?: string): boolean {
  return !!provider && REGION_REQUIRED.has(provider);
}

export function endpointRule(provider?: string): FieldRule {
  if (!provider) return 'optional';
  // azure derives its host from the storage account name in os_access_key_id.
  if (provider === 'azure') return 'ignored';
  return ENDPOINT_REQUIRED.has(provider) ? 'required' : 'optional';
}

export function useSSLRule(provider?: string): FieldRule {
  return provider && SSL_HONORED.has(provider) ? 'optional' : 'ignored';
}

/** Fields of STEP 2 (the source group itself). */
export function groupFields(category: Category, provider?: string): FieldMap {
  switch (category) {
    case 'filesystem':
      // An ssh group derives nothing from a provider or a region.
      return { name: 'required', description: 'optional', provider_name: 'hidden', region_name: 'hidden' };
    case 'objectstorage':
      return {
        name: 'required',
        description: 'optional',
        provider_name: 'required',
        region_name: regionRequired(provider) ? 'required' : 'optional',
      };
    case 'database':
      // provider_name is recorded, never acted on. A db carries its own host:port,
      // so the group has no region field at all.
      return { name: 'required', description: 'optional', provider_name: 'required', region_name: 'hidden' };
  }
}

export interface ConnectionFieldOpts {
  provider?: string;
  dbType?: string;
  dbName?: string;
  accessType?: HoneybeeAccess;
}

const SSH_FIELDSET: FieldMap = {
  ip_address: 'required',
  ssh_port: 'required',
  user: 'required',
  // Key auth only. cm-honeybee refuses a password outright on
  // the fs, db and minio types — the SSH host of one of those is also reached by
  // cm-centipede to move the data, and that side authenticates by key alone, so
  // a password registers a connection honeybee can scan and no migration can
  // use.
  private_key: 'required',
};

const SSH_HIDDEN: FieldMap = {
  ip_address: 'hidden',
  ssh_port: 'hidden',
  user: 'hidden',
  private_key: 'hidden',
};

/** Fields of STEP 3 (one connection under the group). */
export function connectionFields(category: Category, opts: ConnectionFieldOpts = {}): FieldMap {
  const tunneled = opts.accessType === 'ssh-tunnel';

  switch (category) {
    case 'filesystem':
      // fs_scan_path is optional only in the sense that honeybee fills an empty
      // one with "/home". A default nobody chose still decides what gets
      // migrated, so the field says which path it is rather than sitting blank.
      return { name: 'required', ...SSH_FIELDSET, fs_scan_path: 'optional' };

    case 'objectstorage':
      return {
        name: 'required',
        os_access_type: 'optional',
        ...(tunneled ? SSH_FIELDSET : SSH_HIDDEN),
        os_access_key_id: 'required',
        os_secret_access_key: 'required',
        // A tunnel does not change the provider rules: cm-honeybee resolves the
        // endpoint regardless of access type.
        os_endpoint: endpointRule(opts.provider),
        os_use_ssl: useSSLRule(opts.provider),
        // The scan target lives on the connection: one bucket per connection,
        // matching the one saved result a connection has.
        os_scan_bucket: 'required',
        os_scan_prefix: 'optional',
      };

    case 'database':
      return {
        name: 'required',
        db_type: 'required',
        db_access_type: 'optional',
        ...(tunneled ? SSH_FIELDSET : SSH_HIDDEN),
        db_host: 'required',
        db_port: 'required',
        db_username: 'required',
        db_password: 'required',
        // Empty means every user database on the server, which is the normal
        // flow: the wizard's Step 3 filter narrows it down.
        db_name: 'optional',
        db_connect_timeout: 'optional',
        db_tls_mode: 'optional',
        db_tls_ca_pem: 'optional',
        // MongoDB only; empty makes the driver default to "admin".
        db_auth_source: opts.dbType === 'mongodb' ? 'optional' : 'hidden',
        // checkPgSchema rejects a schema list without a database name.
        db_pg_schema:
          opts.dbType === 'postgresql' && !!opts.dbName?.trim() ? 'optional' : 'hidden',
      };
  }
}

export function isVisible(rule: FieldRule | undefined): boolean {
  return rule !== undefined && rule !== 'hidden';
}

export const PROVIDER_OPTIONS: ProviderName[] = [
  'aws',
  'alibaba',
  'tencent',
  'ncp',
  'nhn',
  'ibm',
  'gcp',
  'kt',
  'azure',
  'openstack',
  'onprem',
];
