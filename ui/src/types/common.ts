// Mirrors dmdl/common-model. Field names and spellings follow the Go JSON tags
// exactly — notably `sshTunnel` (camelCase) here versus cm-honeybee's
// `ssh-tunnel` (hyphen).

export type ConnectionSource =
  | 'honeybee'
  | 'beetleSsh'
  | 'beetleObjectStorage'
  | 'beetleDb'
  | 'ssh'
  | 'minio'
  | 'db';

export const PROVIDER_NAMES = [
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
] as const;

export type ProviderName = (typeof PROVIDER_NAMES)[number];

export type DBMSType = 'mysql' | 'mariadb' | 'postgresql' | 'mongodb';

export const DBMS_TYPES: DBMSType[] = ['mysql', 'mariadb', 'postgresql', 'mongodb'];

/** The port each engine listens on out of the box. */
export const DBMS_DEFAULT_PORT: Record<DBMSType, number> = {
  mysql: 3306,
  mariadb: 3306,
  postgresql: 5432,
  mongodb: 27017,
};

/**
 * Whether a db form's port is still the one the form put there — empty, or the
 * default of the type it was chosen for — and so may follow a change of type.
 * A port typed by hand is never replaced.
 */
export function isDefaultDbPort(
  port: string | number | undefined,
  type: string | undefined,
): boolean {
  if (port === undefined || port === '') return true;
  const def = DBMS_DEFAULT_PORT[type as DBMSType];
  return def !== undefined && String(def) === String(port).trim();
}

export type TLSMode = 'disable' | 'prefer' | 'require' | 'verify-ca' | 'verify-full';

export const TLS_MODES: TLSMode[] = ['disable', 'prefer', 'require', 'verify-ca', 'verify-full'];

/**
 * Whether the mode checks the server's certificate chain, which is the only
 * thing a CA is consulted for. `tlsCAPem` belongs to these two modes and to no
 * other: the other three encrypt without verifying, so a root supplied there
 * would be carried and never used.
 */
export function tlsModeVerifies(mode?: TLSMode): boolean {
  return mode === 'verify-ca' || mode === 'verify-full';
}

/** data-mig transfer strategy. Only meaningful for SSH→SSH file system jobs. */
export type Strategy = 'auto' | 'direct' | 'relay';

/** Cross-storage destination discriminator, derived server-side from dstConnection. */
export type StorageType = 'filesystem' | 'objectstorage';

// ── Reference types ─────────────────────────────────────────────────────────

export interface HoneybeeRef {
  connectionId: string;
}

export interface BeetleSSHRef {
  nsId: string;
  infraId: string;
  nodeId: string;
}

export interface BeetleOSRef {
  nsId: string;
  osId: string;
}

export interface BeetleDBRef {
  nsId: string;
  rdbmsId: string;
  /**
   * The instance admin. Required by the server, but cm-beetle does not return it
   * (since v0.6.2), so like password it is filled in from wizard Step 2.
   */
  username?: string;
  /** cm-beetle never returns this; it is collected in wizard Step 2 every time. */
  password: string;
  tlsMode?: TLSMode;
  tlsCAPem?: string;
}

// ── Inline types ────────────────────────────────────────────────────────────

/**
 * Key auth only. The server's SSHConnConfig has no password field, so one sent
 * from here would be dropped on decode — for ssh and for db.sshTunnel alike.
 */
export interface SSHConnConfig {
  host: string;
  port?: number;
  username: string;
  privateKey?: string;
}

/** No access type: inline object storage is always direct. */
export interface MinioConnConfig {
  providerName: ProviderName | '';
  endpoint?: string;
  accessKeyId: string;
  secretAccessKey: string;
  region?: string;
  useSSL?: boolean;
  bucketLookup?: '' | 'dns' | 'path';
}

/** Carries server-level access only — it holds no database name. */
export interface DBConnConfig {
  providerName: ProviderName | '';
  dbmsType: DBMSType;
  accessType: 'direct' | 'sshTunnel';
  host: string;
  port?: number;
  username?: string;
  password?: string;
  connectTimeout?: number;
  tlsMode?: TLSMode;
  tlsCAPem?: string;
  authSource?: string;
  sshTunnel?: SSHConnConfig;
}

/** Exactly one sub-field matching `source` is set. */
export interface ConnectionRef {
  source: ConnectionSource;
  honeybee?: HoneybeeRef;
  beetleSsh?: BeetleSSHRef;
  beetleObjectStorage?: BeetleOSRef;
  beetleDb?: BeetleDBRef;
  ssh?: SSHConnConfig;
  minio?: MinioConnConfig;
  db?: DBConnConfig;
}

// ── Filters ─────────────────────────────────────────────────────────────────

export type FilterAction = 'include' | 'exclude';
export type PathRuleType = 'glob' | 'size';
export type SizeOp = '>' | '>=' | '<' | '<=' | '==';

/** Ordered include/exclude entry; first match wins, unmatched items are kept. */
export interface PathFilterRule {
  action: FilterAction;
  type: PathRuleType;
  pattern?: string;
  op?: SizeOp;
  value?: number;
}

export type DBMSRuleType = 'table_column' | 'row_exclude' | 'object_exclude';

/** The dbmsx pipeline is exclude-only: there is no include action and no order. */
export interface DBMSFilterRule {
  type: DBMSRuleType;
  table?: string;
  columns?: string[];
  match?: string;
  kind?: string;
  name?: string;
}

export interface PgSchemaMapping {
  srcName: string;
  dstName?: string;
}

export interface TargetMapping {
  srcName: string;
  dstName?: string;
  pgSchemas?: PgSchemaMapping[];
}

export interface FileSystemFilter {
  rules?: PathFilterRule[];
  targetMapping: TargetMapping;
}

export interface ObjectStorageFilter {
  rules?: PathFilterRule[];
  targetMapping: TargetMapping;
}

export interface DBMSFilterEntry {
  targetMapping: TargetMapping;
  rules?: DBMSFilterRule[];
}

export interface DBMSFilter {
  databases?: DBMSFilterEntry[];
}
