// cm-beetle REST calls. Read-only: this UI points at target infrastructure that
// cm-beetle already created, it never creates any.

import { arr, http } from './http';
import type {
  BeetleInfraInfoList,
  BeetleObjectStorageListResponse,
  BeetleRDBMSDatabaseListResponse,
  BeetleRDBMSListResponse,
  BeetleSSHReadyResult,
} from '@/types/beetle';

const BASE = '/beetle/migration';

export const beetleApi = {
  listInfra: (nsId: string) =>
    http.get<BeetleInfraInfoList>(`${BASE}/ns/${nsId}/infra`).then((r) => ({
      // Each infra's node list is a nil slice too when it is empty.
      infra: arr(r.infra).map((i) => ({ ...i, node: arr(i.node) })),
    })),

  /** Expensive — it opens real SSH connections. Manual button only. */
  checkSSHReady: (nsId: string, infraId: string) =>
    http.get<BeetleSSHReadyResult>(`${BASE}/ns/${nsId}/infra/${infraId}/ssh-ready`),

  listObjectStorages: (nsId: string) =>
    http
      .get<BeetleObjectStorageListResponse>(`${BASE}/middleware/ns/${nsId}/objectStorage`)
      .then((r) => ({ objectStorage: arr(r.objectStorage) })),

  listRDBMS: (nsId: string) =>
    http
      .get<BeetleRDBMSListResponse>(`${BASE}/middleware/ns/${nsId}/rdbms`)
      .then((r) => ({ rdbms: arr(r.rdbms) })),

  listRDBMSDatabases: (nsId: string, rdbmsId: string) =>
    http
      .get<BeetleRDBMSDatabaseListResponse>(
        `${BASE}/middleware/ns/${nsId}/rdbms/${rdbmsId}/database`,
      )
      .then((r) => ({ databases: arr(r.databases) })),
};
