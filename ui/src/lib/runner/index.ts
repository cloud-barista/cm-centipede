import { centipedeRunner } from './centipede';
import type { MigrationRunner } from './types';

export const runners: MigrationRunner[] = [centipedeRunner];

export const defaultRunner = runners[0];

export function runnerById(id: string): MigrationRunner | undefined {
  return runners.find((r) => r.id === id);
}

export type { EngineId, MigrationRunner, RunHandle, RunRequest } from './types';
