// Mirrors dmdl/source-model. A */refined response is a SourceDataMigrationModel
// and is forwarded to POST /plans/target untouched.

import type { ConnectionRef } from './common';

export interface FSEntry {
  path: string;
  isMount: boolean;
  modTime?: string;
  permissions?: string;
  fileCount?: number;
  fileSize?: number;
}

export interface FSInfo {
  path: string;
  isMount: boolean;
  totalSize?: number;
  fileCount?: number;
  extensionCounts?: Record<string, number>;
  folders: FSEntry[];
}

export interface SourceFileSystemModel extends FSInfo {
  connection: ConnectionRef;
}

export interface ObjectEntry {
  key: string;
  lastModified?: string;
  objectCount?: number;
  size?: number;
}

export interface ObjectStorageInfo {
  path: string;
  totalSize?: number;
  objectCount?: number;
  extensionCounts?: Record<string, number>;
  folders: ObjectEntry[];
}

export interface SourceObjectStorageModel extends ObjectStorageInfo {
  connection: ConnectionRef;
}

export interface DBPgSchemaInfo {
  name: string;
  totalSize: number;
  dataSize: number;
  indexSize: number;
  tableCount: number;
}

export interface DBTableInfo {
  pgSchema?: string;
  name: string;
  rowCount: number;
  dataSize: number;
  indexSize: number;
  totalSize: number;
  engine?: string;
}

export interface DBMSInfo {
  serverVersion: string;
  database: string;
  characterSet?: string;
  collation?: string;
  totalSize: number;
  dataSize: number;
  indexSize: number;
  tableCount: number;
  pgSchema?: DBPgSchemaInfo[];
  tables: DBTableInfo[];
  views?: string[];
  functions?: string[];
  procedures?: string[];
  triggers?: string[];
  events?: string[];
  materializedViews?: string[];
  sequences?: string[];
  types?: string[];
}

export interface SourceDBModel {
  connection: ConnectionRef;
  databases: DBMSInfo[];
}

export interface SourceDataMigrationProperty {
  fileSystems?: SourceFileSystemModel[];
  objectStorages?: SourceObjectStorageModel[];
  databases?: SourceDBModel[];
}

export interface SourceDataMigrationModel {
  sourceDataMigrationModel: SourceDataMigrationProperty;
}
