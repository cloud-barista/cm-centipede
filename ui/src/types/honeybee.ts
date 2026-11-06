// Mirrors cm-honeybee's REST models (snake_case, unlike centipede's camelCase).

/**
 * cm-honeybee reports exactly two outcomes. There is no "not applicable":
 * a check that does not apply is reported as `failed` with a message saying so,
 * deliberately — its connectionInfo controller notes that introducing a third
 * value would meet every existing reader unprepared.
 *
 * So whether a check applies at all is not in this field, and cannot be read
 * out of it. See `agentApplies` in lib/connectionCategory.ts.
 */
export type HoneybeeStatus = 'success' | 'failed' | string;

/** "ssh-tunnel" with a hyphen — centipede spells the same concept "sshTunnel". */
export type HoneybeeAccessType = 'direct' | 'ssh-tunnel';

export interface ConnectionInfoStatusCount {
  count_connection_success: number;
  count_connection_failed: number;
  count_agent_success: number;
  count_agent_failed: number;
  connection_info_total: number;
}

export interface SourceGroupRes {
  id: string;
  name: string;
  description: string;
  /**
   * "fs" | "minio" | "db" are the data groups this UI handles. "csp" and
   * "onprem" (legacy spelling "ssh", which is also what an omitted type stores)
   * are infrastructure groups and are filtered out — see connectionCategory.ts.
   */
  type: string;
  provider_name?: string;
  region_name?: string;
  connection_info_status_count: ConnectionInfoStatusCount;
}

export interface ListSourceGroupRes {
  source_group: SourceGroupRes[];
  connection_info_status_count: ConnectionInfoStatusCount;
}

/**
 * Fields cm-honeybee RSA-encrypts on EVERY connection response — the list
 * endpoint included.
 *
 * A 4096-bit key makes each one 684 characters of base64, so none of these may
 * be rendered: they are not values, they are ciphertext, and the private key
 * lives on the server so the browser cannot undo it. Note that `user` and
 * `db_username` are in here too — they are names, not secrets, but they come
 * back encrypted all the same.
 */
export type EncryptedConnectionField =
  | 'user'
  | 'password'
  | 'private_key'
  | 'db_username'
  | 'db_password'
  | 'os_access_key_id'
  | 'os_secret_access_key';

export interface ConnectionInfo {
  id: string;
  name: string;
  description: string;
  source_group_id: string;

  ip_address?: string;
  ssh_port?: string;
  /** RSA ciphertext on read — never render. See EncryptedConnectionField. */
  user?: string;
  /** RSA ciphertext on read — never render. */
  password?: string;
  /** RSA ciphertext on read — never render. */
  private_key?: string;

  /**
   * The one path an fs import walks. cm-honeybee normalizes it on the way in,
   * so a stored value is always absolute and an omitted one becomes "/home".
   *
   * The scan target belongs to the connection, not to the import request: a
   * group-level import walks each connection's own path rather than applying
   * one path to every host.
   */
  fs_scan_path?: string;

  db_type?: string;
  db_name?: string;
  db_access_type?: HoneybeeAccessType;
  db_host?: string;
  db_port?: string;
  /** RSA ciphertext on read — never render. */
  db_username?: string;
  /** RSA ciphertext on read — never render. */
  db_password?: string;
  db_connect_timeout?: number;
  db_tls_mode?: string;
  db_tls_ca_pem?: string;
  db_auth_source?: string;
  db_pg_schema?: string[];

  os_access_type?: HoneybeeAccessType;
  os_endpoint?: string;
  /** RSA ciphertext on read — never render. */
  os_access_key_id?: string;
  /** RSA ciphertext on read — never render. */
  os_secret_access_key?: string;
  os_use_ssl?: boolean;

  /**
   * The one bucket an object storage import inspects. Required on a minio
   * connection, and never carries a "/" — the scan root is built as
   * bucket + "/" + prefix, so a slash inside the name yields a path that is
   * neither, and an inspect reports that as an empty result rather than an
   * error. On Tencent it must carry the AppId suffix ("<name>-<AppId>").
   */
  os_scan_bucket?: string;
  /** Narrows the scan to one key prefix. Empty → the whole bucket. */
  os_scan_prefix?: string;

  connection_status: HoneybeeStatus;
  connection_failed_message: string;
  agent_status: HoneybeeStatus;
  agent_failed_message: string;
}

export interface ListConnectionInfoRes {
  connection_info: ConnectionInfo[];
  connection_info_status_count: ConnectionInfoStatusCount;
}

export interface CreateConnectionInfoReq {
  name: string;
  description?: string;

  ip_address?: string;
  ssh_port?: string;
  user?: string;
  /**
   * The only SSH credential. There is no password field: cm-honeybee
   * refuses one on the fs, db and minio types, and sending an
   * empty one would be a field the form cannot fill.
   */
  private_key?: string;

  /**
   * The one path an fs import walks. cm-honeybee normalizes it on the way in,
   * so a stored value is always absolute and an omitted one becomes "/home".
   *
   * The scan target belongs to the connection, not to the import request: a
   * group-level import walks each connection's own path rather than applying
   * one path to every host.
   */
  fs_scan_path?: string;

  db_type?: string;
  db_name?: string;
  db_access_type?: HoneybeeAccessType;
  db_host?: string;
  db_port?: string;
  db_username?: string;
  db_password?: string;
  db_connect_timeout?: number;
  db_tls_mode?: string;
  db_tls_ca_pem?: string;
  db_auth_source?: string;
  db_pg_schema?: string[];

  os_access_type?: HoneybeeAccessType;
  os_endpoint?: string;
  os_access_key_id?: string;
  os_secret_access_key?: string;
  os_use_ssl?: boolean;

  /**
   * The one bucket an object storage import inspects. Required on a minio
   * connection, and never carries a "/" — the scan root is built as
   * bucket + "/" + prefix, so a slash inside the name yields a path that is
   * neither, and an inspect reports that as an empty result rather than an
   * error. On Tencent it must carry the AppId suffix ("<name>-<AppId>").
   */
  os_scan_bucket?: string;
  /** Narrows the scan to one key prefix. Empty → the whole bucket. */
  os_scan_prefix?: string;
}

export interface CreateSourceGroupReq {
  name: string;
  description?: string;
  connection_info?: CreateConnectionInfoReq[];
  type: string;
  provider_name?: string;
  region_name?: string;
}

// ── Inspect requests ────────────────────────────────────────────────────────
// Each metric costs a full walk of the source.

export interface FSMetricReq {
  total_size?: boolean;
  file_count?: boolean;
  extension_count?: boolean;
  folder_mod_time?: boolean;
  folder_perms?: boolean;
  folder_file_count?: boolean;
  folder_file_size?: boolean;
}

/**
 * How detailed a filesystem collection should be, and nothing about what to
 * collect: the path comes from the connection's fs_scan_path.
 */
export interface ImportFSReq {
  /** 0 → unlimited. */
  max_depth?: number;
  metric?: FSMetricReq;
}

export interface OSMetricReq {
  total_size?: boolean;
  object_count?: boolean;
  extension_count?: boolean;
  prefix_mod_time?: boolean;
  prefix_object_count?: boolean;
  prefix_object_size?: boolean;
}

/** The bucket and prefix come from the connection, so the whole body is optional. */
export interface ImportObjectStorageReq {
  metric?: OSMetricReq;
}

/**
 * Every field is engine-scoped. cm-honeybee routes this to the engine-specific
 * data-mig type and silently DROPS what that engine has no equivalent for, so
 * sending a postgres-only flag to MySQL is harmless — but offering it is not,
 * which is why the form asks the connection's db_type first (DB_METRICS in
 * components/domains/adapters.ts).
 *
 * Server version, database size and the table list are always collected;
 * everything here is opt-in.
 */
export interface DBMetricReq {
  // All engines.
  row_count_exact?: boolean;
  /** Turning these on can make the response several MB. */
  columns?: boolean;
  indexes?: boolean;
  views?: boolean;

  // MySQL, MariaDB and PostgreSQL.
  foreign_keys?: boolean;
  functions?: boolean;
  procedures?: boolean;
  triggers?: boolean;

  // MySQL and MariaDB only.
  events?: boolean;

  // PostgreSQL only.
  materialized_views?: boolean;
  sequences?: boolean;
  types?: boolean;
  extensions?: boolean;
  rules?: boolean;

  // MongoDB only.
  validators?: boolean;
  /**
   * How many documents are sampled to infer fields when `columns` is on.
   * Omitted → the driver's own default of 100.
   *
   * The metric form offers no control for it: every other entry here is a
   * checkbox, this one is a count, and the default is the right answer until
   * someone has a reason it is not.
   */
  sample_size?: number;
}

export interface ImportDBReq {
  metric?: DBMetricReq;
}
