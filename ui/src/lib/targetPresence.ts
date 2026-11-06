// Whether a shortlisted cm-beetle target is still there.
//
// cm-beetle does offer a single-item GET for each of the three kinds, but this
// asks the LIST endpoint for the reference's namespace and looks the id up in
// the answer. Three reasons, in order of weight:
//
//   1. Its three single GETs do not agree on what "not found" is. GetInfra and
//      GetObjectStorage answer 404; GetRDBMS has no 404 branch at all and folds
//      every failure into a 500, so an rdbms that is gone is indistinguishable
//      from cm-beetle being unwell. Absence from a list that loaded is the same
//      unambiguous answer for all three.
//   2. Every one of these calls reaches the cloud provider through cb-tumblebug,
//      so the count matters. One list per (namespace, kind) is fewer calls than
//      one GET per shortlisted item, and the shortlist is deliberately spread
//      across namespaces.
//   3. They are the queries the target picker already runs, under the same keys
//      — so while the picker is showing a namespace, checking the entries from
//      it costs nothing at all.
//
// A beetleSsh reference names a node, so the list is read two levels deep.

import type { ConnectionRef } from '@/types/common';
import type {
  BeetleInfraInfoList,
  BeetleObjectStorageListResponse,
  BeetleRDBMSListResponse,
} from '@/types/beetle';

/** Which of cm-beetle's three target lists a reference lives in. */
export type TargetKind = 'infra' | 'objectStorage' | 'rdbms';

export interface TargetLocator {
  nsId: string;
  kind: TargetKind;
}

/**
 * Three states, not two.
 *
 * `unknown` is the one that has to exist. cm-beetle is unreachable, the proxy
 * answers 502, the provider rate-limits it into a 503, the namespace itself is
 * gone — all of those are read failures, and reporting them as `gone` would
 * turn every hiccup into "your saved targets have disappeared". Saying nothing
 * is better than saying something untrue.
 */
export type PresenceState = 'present' | 'gone' | 'unknown';

export interface TargetPresence {
  state: PresenceState;
  /**
   * cm-beetle's own word for the resource's condition, passed through and never
   * judged. The node vocabulary is defined (Running, Suspended, Failed,
   * Terminated, Creating …) but object storage and rdbms carry whatever
   * cb-tumblebug reports, so classifying any of it here would be guessing.
   *
   * For a beetleSsh reference this is the NODE's status, not its infra's. An
   * infra states itself as a count — "Partial-Running:3 (R:3/5)" is the dominant
   * node status, how many nodes hold it, and how many are Running out of the
   * total — which answers nothing about the one node a reference points at, and
   * on a five-node infra can read "Running" beside a target whose node is
   * stopped. On a single-node infra it was three ways of saying "Running".
   */
  status?: string;
}

/** Where to look for this reference, or null when it is not a cm-beetle one. */
export function locatorOf(ref: ConnectionRef): TargetLocator | null {
  if (ref.beetleSsh) return { nsId: ref.beetleSsh.nsId, kind: 'infra' };
  if (ref.beetleObjectStorage) return { nsId: ref.beetleObjectStorage.nsId, kind: 'objectStorage' };
  if (ref.beetleDb) return { nsId: ref.beetleDb.nsId, kind: 'rdbms' };
  // An inline target, or a honeybee source used as one. Nothing in cm-beetle to
  // look for, so nothing to report.
  return null;
}

export function locatorKey(l: TargetLocator): string {
  return `${l.kind}:${l.nsId}`;
}

/** The distinct lists a set of references needs, so each is fetched once. */
export function locatorsOf(refs: ConnectionRef[]): TargetLocator[] {
  const seen = new Map<string, TargetLocator>();
  for (const ref of refs) {
    const l = locatorOf(ref);
    if (l) seen.set(locatorKey(l), l);
  }
  return [...seen.values()];
}

/**
 * Looks the reference up in the list for its own locator.
 *
 * `data` is whatever that list query returned; the reference decides how to
 * read it, which is why the shapes are asserted rather than discriminated.
 */
export function presenceIn(ref: ConnectionRef, data: unknown): TargetPresence {
  const ssh = ref.beetleSsh;
  if (ssh) {
    const infra = (data as BeetleInfraInfoList).infra ?? [];
    const found = infra.find((i) => i.id === ssh.infraId);
    if (!found) return { state: 'gone' };
    // The reference names one node. An infra that is still listed can have lost
    // the node this target points at, and that is just as gone.
    const node = (found.node ?? []).find((n) => n.id === ssh.nodeId);
    if (!node) return { state: 'gone' };
    // The node's word, not the infra's. Nothing falls back to the infra's
    // aggregate when a node carries no status: the aggregate is what this
    // stopped reporting, and repeating it as a substitute would put the same
    // wrong answer back under a different condition.
    return { state: 'present', status: node.status };
  }

  const os = ref.beetleObjectStorage;
  if (os) {
    const list = (data as BeetleObjectStorageListResponse).objectStorage ?? [];
    const found = list.find((o) => o.id === os.osId);
    return found ? { state: 'present', status: found.status } : { state: 'gone' };
  }

  const db = ref.beetleDb;
  if (db) {
    const list = (data as BeetleRDBMSListResponse).rdbms ?? [];
    const found = list.find((r) => r.id === db.rdbmsId);
    return found ? { state: 'present', status: found.status } : { state: 'gone' };
  }

  return { state: 'unknown' };
}
