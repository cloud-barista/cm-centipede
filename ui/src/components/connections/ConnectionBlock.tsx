'use client';

// A connection is drawn as a three-line block rather than a table row: the
// column is full-width, so the agent failure message and the last scan can be
// shown without folding them away.

import React from 'react';
import { Badge, Button, ErrorNote, Note } from '@/components/ui/primitives';
import { HealthBadge } from '@/components/ui/status';
import { ScanSummary } from './ScanSummary';
import { useDeleteConnection, useRefined, useRefreshConnection } from '@/queries/honeybee';
import { isStaleScanError, scanErrorMessage } from '@/api/honeybee';
import {
  agentApplies,
  categoryOfGroup,
  connectionAddress,
  scanTargetOf,
  type Category,
} from '@/lib/connectionCategory';
import type { ConnectionInfo, SourceGroupRes } from '@/types/honeybee';

/**
 * How this connection is reached. An empty value means "direct" (cm-honeybee
 * spells the tunnelled one with a hyphen, unlike centipede's `sshTunnel`).
 *
 * An fs group has no access-type field at all — the connection *is* the SSH
 * host — so it gets no chip.
 */
function accessTypeOf(
  conn: ConnectionInfo,
  category: Category | null,
): 'direct' | 'ssh-tunnel' | null {
  if (category === 'objectstorage') {
    return conn.os_access_type === 'ssh-tunnel' ? 'ssh-tunnel' : 'direct';
  }
  if (category === 'database') {
    return conn.db_access_type === 'ssh-tunnel' ? 'ssh-tunnel' : 'direct';
  }
  return null;
}

export function ConnectionBlock({
  connection,
  group,
}: {
  connection: ConnectionInfo;
  group: SourceGroupRes;
}) {
  const category = categoryOfGroup(group);
  const del = useDeleteConnection(group.id);
  const refresh = useRefreshConnection(group.id);

  // A refined read is cheap and cached for five minutes, and a failure usually
  // just means nothing has been scanned yet — so it is not retried and not
  // surfaced. Except when it is stale: that one is about a scan that DID run,
  // and saying nothing would leave the row looking unscanned forever.
  const refined = useRefined(category, group.id, connection.id, !!category);
  const staleScan = isStaleScanError(refined.error);
  const accessType = accessTypeOf(connection, category);
  const scanTarget = scanTargetOf(connection, category);

  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-bold text-sm">{connection.name}</span>
        <span className="text-xs font-mono text-text-muted">
          {connectionAddress(connection, category ?? '')}
        </span>
        {/* user / db_username are NOT shown: cm-honeybee RSA-encrypts seven
            fields on every connection response — user, password, private_key,
            db_username, db_password, os_access_key_id, os_secret_access_key —
            so rendering them prints 684 characters of base64, not a name. */}
        {accessType && (
          <Badge tone={accessType === 'ssh-tunnel' ? 'info' : 'neutral'}>{accessType}</Badge>
        )}
        {accessType === 'ssh-tunnel' && connection.ip_address && (
          <span className="text-xs font-mono text-text-muted">
            via {connection.ip_address}:{connection.ssh_port ?? '22'}
          </span>
        )}
        <HealthBadge label="conn" status={connection.connection_status} />
        {/* Rendered only where an agent is part of the connection. A direct db
            or object storage connection has none, and honeybee reports that
            absence as a failure — so the badge is left out rather than shown
            with a softer word: there is no agent here whose health to report. */}
        {agentApplies(connection, category) && (
          <HealthBadge label="agent" status={connection.agent_status} />
        )}
      </div>

      {scanTarget && (
        <div className="text-xs font-mono">
          <span className="text-text-muted">scan target </span>
          <span>{scanTarget}</span>
        </div>
      )}

      {staleScan ? (
        <Note tone="warn">{scanErrorMessage(refined.error)}</Note>
      ) : (
        category && <ScanSummary category={category} model={refined.data} />
      )}

      {connection.connection_failed_message && (
        <Note tone="bad">{connection.connection_failed_message}</Note>
      )}
      {/* Same rule for the message. On a direct connection it reads "agent-based
          collection is not applicable to direct db connections", which is a
          description of the design and not something gone wrong. */}
      {agentApplies(connection, category) &&
        connection.agent_status === 'failed' &&
        connection.agent_failed_message && (
          <Note tone="bad">{connection.agent_failed_message}</Note>
        )}

      <div className="flex flex-wrap gap-2 pt-1">
        <Button onClick={() => refresh.mutate(connection.id)} disabled={refresh.isPending}>
          {refresh.isPending ? 'checking…' : 'recheck'}
        </Button>
        <Button
          variant="danger"
          onClick={() => {
            if (confirm(`Delete connection "${connection.name}"?`)) del.mutate(connection.id);
          }}
          disabled={del.isPending}
        >
          delete
        </Button>
      </div>

      <ErrorNote error={del.error} />
      <ErrorNote error={refresh.error} />
    </div>
  );
}
