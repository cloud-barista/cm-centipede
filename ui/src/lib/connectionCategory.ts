// The single source of truth for category ↔ source-group-type mapping.
// Nothing else in the UI may hard-code these strings.

import type { ConnectionInfo, ConnectionInfoStatusCount, SourceGroupRes } from '@/types/honeybee';
import type { ConnectionRef } from '@/types/common';

export type Category = 'filesystem' | 'objectstorage' | 'database';

export const CATEGORIES: Category[] = ['filesystem', 'objectstorage', 'database'];

/**
 * cm-honeybee source_group.type — fixed at creation and never changeable.
 *
 * These are honeybee's three DATA group types, and the list is exhaustive on
 * purpose. Its other types collect infrastructure, not data: "csp" goes through
 * cb-spider, and "onprem" — whose earlier spelling was "ssh", which an omitted
 * type still stores — collects infra, software, kubernetes and helm from a
 * host. honeybee answers 400 when asked to inspect a filesystem, a bucket or a
 * database through either, and cm-centipede's own client rejects them by name
 * (pkg/client/honeybee/client.go), so a group of those types has nothing behind
 * it for this UI to migrate.
 *
 * "ssh" used to be mapped here to filesystem. That was not a spelling
 * difference: it made this UI register its file system sources as on-prem
 * infrastructure groups, which the migration path then refused. A host whose
 * filesystem is to be collected belongs in an "fs" group.
 */
const CATEGORY_OF_TYPE: Record<string, Category> = {
  fs: 'filesystem',
  minio: 'objectstorage',
  db: 'database',
};

const TYPE_OF_CATEGORY: Record<Category, string> = {
  filesystem: 'fs',
  objectstorage: 'minio',
  database: 'db',
};

export const CATEGORY_LABEL: Record<Category, string> = {
  filesystem: 'File System',
  objectstorage: 'Object Storage',
  database: 'Database',
};

/** The unit a TargetMapping name points at, per domain. */
export const CATEGORY_UNIT: Record<Category, string> = {
  filesystem: 'Folder',
  objectstorage: 'Bucket',
  database: 'Database',
};

export function groupTypeOf(category: Category): string {
  return TYPE_OF_CATEGORY[category];
}

/** Whether this UI handles the group at all. csp, onprem and the rest: false. */
export function isSupportedGroup(g: SourceGroupRes): boolean {
  return g.type in CATEGORY_OF_TYPE;
}

export function categoryOfGroup(g: SourceGroupRes): Category | null {
  return CATEGORY_OF_TYPE[g.type] ?? null;
}

/**
 * The kind of storage a destination is — what cross-storage compares, and what
 * decides which Step 3 inputs mean anything for it.
 */
export function categoryOfRef(ref: ConnectionRef | null | undefined): Category | null {
  switch (ref?.source) {
    case 'beetleSsh':
    case 'ssh':
      return 'filesystem';
    case 'beetleObjectStorage':
    case 'minio':
      return 'objectstorage';
    case 'beetleDb':
    case 'db':
      return 'database';
    default:
      return null;
  }
}

/** Where a connection is reached, as one short line. */
export function connectionAddress(conn: ConnectionInfo, category: string): string {
  if (category === 'filesystem') return `${conn.ip_address ?? '-'}:${conn.ssh_port ?? '22'}`;
  if (category === 'database') return `${conn.db_host ?? '-'}:${conn.db_port ?? '-'}`;
  return conn.os_endpoint || 'endpoint derived from provider';
}

/**
 * What a connection collects — the target its import walks.
 *
 * Read here rather than asked for at inspect time: cm-honeybee moved the scan
 * target onto the connection (fs_scan_path, os_scan_bucket + os_scan_prefix,
 * db_name), and the import request now says only how detailed the collection
 * should be. A group-level import walks each connection's own target.
 */
export function scanTargetOf(conn: ConnectionInfo, category: Category | null): string | null {
  if (category === 'filesystem') return conn.fs_scan_path || '/';
  if (category === 'objectstorage') {
    // honeybee builds the scan root as bucket + "/" + prefix, so it is shown
    // the same way rather than as two values a reader has to join.
    return conn.os_scan_bucket ? `${conn.os_scan_bucket}/${conn.os_scan_prefix ?? ''}` : null;
  }
  // An empty db_name means every user database on the server. That is a real
  // answer to "what does this collect", not a missing value.
  if (category === 'database') return conn.db_name || 'every user database';
  return null;
}

export type ConnectionHealth = 'ok' | 'failed' | 'installing';

export function connectionHealth(status: string | undefined): ConnectionHealth {
  switch (status) {
    case 'success':
      return 'ok';
    case 'failed':
      return 'failed';
    default:
      // Empty or anything else means the check has not settled yet.
      return 'installing';
  }
}

/**
 * Whether an agent is part of this connection at all.
 *
 * cm-honeybee installs one only where it reaches the source over SSH: always
 * for a filesystem connection, and for a db or object storage connection only
 * when its access type is "ssh-tunnel". A direct one is inspected by the server
 * itself, over TCP, and no agent is ever involved.
 *
 * It reports that case as `agent_status: "failed"`, with the message "agent-
 * based collection is not applicable to direct db connections". That is a
 * considered decision on its side — a third status value would meet every
 * existing reader unprepared — but read literally it makes a healthy direct
 * connection show an agent FAILED badge, which is the one thing about these
 * rows that people asked about. So the question "does an agent apply here" is
 * answered from the connection's own shape, and the badge is only rendered
 * when the answer is yes.
 */
export function agentApplies(conn: ConnectionInfo, category: Category | null): boolean {
  switch (category) {
    case 'filesystem':
      return true;
    case 'objectstorage':
      return conn.os_access_type === 'ssh-tunnel';
    case 'database':
      return conn.db_access_type === 'ssh-tunnel';
    default:
      return false;
  }
}

/**
 * True while any connection is still waiting for a check to settle.
 *
 * The agent status is read for every connection, applicable or not, and that is
 * safe rather than sloppy: honeybee settles it before the create call returns
 * (doCreateConnectionInfo refreshes before responding), so a non-applicable one
 * is already "failed" here and never counts as unsettled.
 */
export function hasPendingAgent(conns: ConnectionInfo[]): boolean {
  return conns.some(
    (c) =>
      connectionHealth(c.connection_status) === 'installing' ||
      connectionHealth(c.agent_status) === 'installing',
  );
}

export const EMPTY_STATUS_COUNT: ConnectionInfoStatusCount = {
  count_connection_success: 0,
  count_connection_failed: 0,
  count_agent_success: 0,
  count_agent_failed: 0,
  connection_info_total: 0,
};

/**
 * Header roll-up over the supported groups only.
 *
 * A faithful sum of what the server computed, agent counts included. Whether
 * those are worth showing is StatusRollup's decision, not this function's —
 * it is the one that has to be read, and it does not show them.
 *
 * GET /source_group returns csp groups too, and its top-level
 * connection_info_status_count counts their connections. Filtering csp out of
 * the list while showing that top-level number would make the header disagree
 * with the list, so the per-group counts the server computed are summed instead.
 */
export function rollUpSupported(groups: SourceGroupRes[]): ConnectionInfoStatusCount {
  return groups.filter(isSupportedGroup).reduce<ConnectionInfoStatusCount>(
    (acc, g) => {
      const c = g.connection_info_status_count;
      return {
        count_connection_success: acc.count_connection_success + c.count_connection_success,
        count_connection_failed: acc.count_connection_failed + c.count_connection_failed,
        count_agent_success: acc.count_agent_success + c.count_agent_success,
        count_agent_failed: acc.count_agent_failed + c.count_agent_failed,
        connection_info_total: acc.connection_info_total + c.connection_info_total,
      };
    },
    { ...EMPTY_STATUS_COUNT },
  );
}
