// Mirrors the cm-beetle responses this UI reads. Target references are
// read-only here: creating target infrastructure is cm-beetle UI's job.

export interface BeetleNode {
  id: string;
  name: string;
  publicIP: string;
  sshPort: number;
  nodeUserName: string;
  /**
   * This node's own condition — a plain word (Running, Suspended, Failed,
   * Terminated, Creating …), unlike the counted aggregate its infra carries.
   *
   * Optional because cm-beetle declares it on cloudmodel.NodeInfo without
   * promising it on every path, and a target whose status did not arrive should
   * read as unknown rather than as a claim.
   */
  status?: string;
}

export interface BeetleInfraInfo {
  id: string;
  name: string;
  status: string;
  description?: string;
  node: BeetleNode[];
}

export interface BeetleInfraInfoList {
  infra: BeetleInfraInfo[];
}

export interface BeetleObjectStorageInfo {
  id: string;
  name: string;
  status: string;
  description?: string;
  connectionName?: string;
}

export interface BeetleObjectStorageListResponse {
  objectStorage: BeetleObjectStorageInfo[];
}

/** No password field: cm-beetle returns one only at creation time. */
export interface BeetleRDBMSInfo {
  id: string;
  name: string;
  status: string;
  dbEngine: string;
  dbEngineVersion?: string;
  adminUserName: string;
  endpoint?: string;
  publicAccess: boolean;
}

export interface BeetleRDBMSListResponse {
  rdbms: BeetleRDBMSInfo[];
}

export interface BeetleRDBMSDatabaseListResponse {
  databases?: string[];
}

export interface BeetleSSHReadyResult {
  [key: string]: unknown;
}
