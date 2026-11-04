'use client';

import React from 'react';
import type { ConnectionInfoStatusCount } from '@/types/honeybee';

/**
 * How many connections there are, and what is wrong with them. Nothing else.
 *
 * It used to spell out every state — `conn 1/3 OK · agent 0/1 OK · 2 n/a` —
 * which put two different denominators on one line, because the conn ratio
 * counts every connection and the agent ratio only the ones an agent applies
 * to. Naming just the exceptions leaves one number to anchor on and drops the
 * second denominator with them.
 *
 * The agent is not counted here at all, in any group. `count_agent_failed`
 * cannot be read from this response: honeybee counts a connection the agent
 * does not apply to as an agent FAILURE (see `agentApplies`), and the group
 * listing carries no access type to tell those apart — a db group of three
 * healthy direct connections arrives as `3 agent failed`. Only a filesystem
 * group's number is unambiguous, and counting it for that one group type put
 * two meanings behind one line with nothing on screen to separate them.
 *
 * The cost is that an agent which failed to install on an SSH host no longer
 * announces itself here, so finding it means opening the group. That is where
 * the detail has always lived — every connection reports its own agent in its
 * own block — and it is the better half of a trade against a number that is
 * wrong for every direct connection.
 *
 * One line of plain text at the colour of whatever it sits in: no badge, no
 * red. The brackets do the grouping a chip's border used to do, and a count
 * printed only when it is non-zero needs no colour to say it is an exception —
 * its presence already says that.
 */
export function StatusRollup({ count }: { count: ConnectionInfoStatusCount }) {
  const total = count.connection_info_total;
  const failed = count.count_connection_failed;

  return (
    <span className="font-mono">
      {total} {total === 1 ? 'connection' : 'connections'}
      {/* Still "conn failed" rather than bare "failed", though it is the only
          count left: each connection's own badge says conn OK / conn FAILED,
          and the two should be read in the same words. */}
      {failed > 0 && <span className="ml-4 whitespace-nowrap">[ {failed} conn failed ]</span>}
    </span>
  );
}
