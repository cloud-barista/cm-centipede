// cm-honeybee REST calls — source groups, connections and inspection.

import { arr, http } from './http';
import type { SourceDataMigrationModel } from '@/types/source';
import type {
  ConnectionInfo,
  CreateConnectionInfoReq,
  CreateSourceGroupReq,
  ImportDBReq,
  ImportFSReq,
  ImportObjectStorageReq,
  ListConnectionInfoRes,
  ListSourceGroupRes,
  SourceGroupRes,
} from '@/types/honeybee';
import type { Category } from '@/lib/connectionCategory';

const BASE = '/honeybee';

/** The path segment each category's inspect and refined calls live under. */
const KIND_PATH: Record<Category, { import: string; refined: string }> = {
  filesystem: { import: 'import/fs', refined: 'fs/refined' },
  objectstorage: { import: 'import/objectstorage', refined: 'objectstorage/refined' },
  database: { import: 'import/db', refined: 'db/refined' },
};

export type ImportReq = ImportFSReq | ImportObjectStorageReq | ImportDBReq;

export const honeybeeApi = {
  listSourceGroups: () =>
    http
      .get<ListSourceGroupRes>(`${BASE}/source_group`)
      .then((r) => ({ ...r, source_group: arr(r.source_group) })),

  getSourceGroup: (sgId: string) => http.get<SourceGroupRes>(`${BASE}/source_group/${sgId}`),

  /** Creates the group and its connections in one POST. */
  createSourceGroup: (req: CreateSourceGroupReq) =>
    http.post<SourceGroupRes>(`${BASE}/source_group`, req),

  deleteSourceGroup: (sgId: string) => http.del<unknown>(`${BASE}/source_group/${sgId}`),

  listConnections: (sgId: string) =>
    http
      .get<ListConnectionInfoRes>(`${BASE}/source_group/${sgId}/connection_info`)
      .then((r) => ({ ...r, connection_info: arr(r.connection_info) })),

  /**
   * Answers 200 whether or not the connection worked: the outcome is read from
   * connection_status / agent_status on the response. For an SSH
   * connection this installs the agent and takes tens of seconds.
   */
  createConnection: (sgId: string, req: CreateConnectionInfoReq) =>
    http.post<ConnectionInfo>(`${BASE}/source_group/${sgId}/connection_info`, req),

  deleteConnection: (sgId: string, connId: string) =>
    http.del<unknown>(`${BASE}/source_group/${sgId}/connection_info/${connId}`),

  refreshConnection: (sgId: string, connId: string) =>
    http.put<ConnectionInfo>(`${BASE}/source_group/${sgId}/connection_info/${connId}/refresh`),

  /** Runs the inspect. Every selected metric costs a full walk. */
  runImport: (category: Category, sgId: string, connId: string, req: ImportReq) =>
    http.post<unknown>(
      `${BASE}/source_group/${sgId}/connection_info/${connId}/${KIND_PATH[category].import}`,
      req,
    ),

  /**
   * The SourceDataMigrationModel that POST /plans/target takes untouched.
   *
   * Only the nested lists are filled in — the wrapper itself is forwarded to the
   * plan endpoint as it came.
   *
   * `connId` null reads the whole source group: one entry per connection, in
   * the same shape. It leaves out, without saying so, every connection that has
   * not been scanned or whose scan is stale; the caller compares the entries
   * with the group's connection list to find them.
   */
  getRefinedScope: (category: Category, sgId: string, connId: string | null) =>
    http
      .get<SourceDataMigrationModel>(
        connId
          ? `${BASE}/source_group/${sgId}/connection_info/${connId}/${KIND_PATH[category].refined}`
          : `${BASE}/source_group/${sgId}/${KIND_PATH[category].refined}`,
      )
      .then(normalizeRefined),
};

/**
 * Whether a refined read failed because the saved scan is STALE — collected
 * under a scan target the connection no longer has.
 *
 * It has to be told from the message. cm-honeybee answers 400 to everything,
 * so "nothing collected yet" and
 * "collected, but for somewhere else" arrive with the same status, and the UI
 * would otherwise report the second as the first: a screen saying "run the
 * inspect" about a scan that already ran, and whose result is being withheld.
 *
 * honeybee refuses rather than flagging because the refined models carry no
 * status field, which leaves a consumer no way to notice. The row is kept and
 * the next import clears the state.
 */
export function isStaleScanError(e: unknown): boolean {
  const message = e instanceof Error ? e.message : '';
  return /was collected for a different/.test(message);
}

/**
 * The same message without honeybee's trailing "(ConnectionID = …)".
 *
 * The id is what makes the line useful in a server log and noise on a screen
 * already showing that one connection.
 */
export function scanErrorMessage(e: unknown): string {
  const message = e instanceof Error ? e.message : String(e);
  return message.replace(/\s*\(ConnectionID = [^)]*\)\s*$/, '');
}

/** Fills in the nil slices inside a refined response (see arr() in http.ts). */
function normalizeRefined(m: SourceDataMigrationModel): SourceDataMigrationModel {
  const p = m?.sourceDataMigrationModel;
  if (!p) return m;
  return {
    sourceDataMigrationModel: {
      ...p,
      fileSystems: p.fileSystems?.map((fs) => ({ ...fs, folders: arr(fs.folders) })),
      objectStorages: p.objectStorages?.map((os) => ({ ...os, folders: arr(os.folders) })),
      databases: p.databases?.map((server) => ({
        ...server,
        databases: arr(server.databases).map((db) => ({ ...db, tables: arr(db.tables) })),
      })),
    },
  };
}
