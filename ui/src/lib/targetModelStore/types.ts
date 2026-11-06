import type { Category } from '@/lib/connectionCategory';
import type { TargetDataMigrationModel } from '@/types/target';

export interface TargetModelRef {
  id: string;
  name: string;
  version: string;
  description?: string;
}

export interface SaveTargetModelReq {
  name: string;
  version: string;
  description?: string;
  category: Category;
  plan: TargetDataMigrationModel;
}

export interface TargetModelStore {
  /** Sends the Property, not the wrapper — see lib/planEnvelope.ts. */
  save(req: SaveTargetModelReq): Promise<{ id: string }>;
  list(category: Category): Promise<TargetModelRef[]>;
  /** Wraps the stored Property back up before returning it. */
  get(id: string): Promise<TargetDataMigrationModel>;
}
