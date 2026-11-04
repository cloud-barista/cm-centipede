import type { Category } from '@/lib/connectionCategory';
import type { DBMSOnFailure, TargetDataMigrationModel } from '@/types/target';

export type EngineId = 'centipede' | 'cicada';

export interface RunRequest {
  /** Already made unique — see lib/migrationName.ts. */
  name: string;
  description?: string;
  plan: TargetDataMigrationModel;
  dbmsOnFailure?: DBMSOnFailure;
  /** Omitted for the server default. */
  concurrency?: number;
  category: Category;
  targetModelId?: string;
}

export interface RunHandle {
  engine: EngineId;
  /** Migration.Name — the one key both engines share. */
  correlationName: string;
  migrationId?: string;
  workflowId?: string;
}

export interface MigrationRunner {
  id: EngineId;
  label: string;
  isAvailable(): Promise<boolean>;
  start(req: RunRequest): Promise<RunHandle>;
  resolveMigrationId(h: RunHandle): Promise<string | null>;
}
