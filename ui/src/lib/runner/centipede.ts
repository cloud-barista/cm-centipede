import { centipedeApi } from '@/api/centipede';
import type { MigrationRunner, RunHandle, RunRequest } from './types';

/** Direct execution: POST /centipede/migration creates the record and starts it. */
export const centipedeRunner: MigrationRunner = {
  id: 'centipede',
  label: 'Centipede direct',

  async isAvailable() {
    try {
      await centipedeApi.readyz();
      return true;
    } catch {
      return false;
    }
  },

  async start(req: RunRequest): Promise<RunHandle> {
    const created = await centipedeApi.createMigration({
      name: req.name,
      description: req.description,
      plan: req.plan,
      // Only the database category accepts this; the server rejects a bad value
      // rather than defaulting it.
      ...(req.category === 'database' && req.dbmsOnFailure
        ? { dbmsOnFailure: req.dbmsOnFailure }
        : {}),
      // Left out rather than sent as a default, so the server's configured
      // centipede.migration.concurrency still applies.
      ...(req.concurrency ? { concurrency: req.concurrency } : {}),
    });

    return {
      engine: 'centipede',
      correlationName: req.name,
      migrationId: created.id,
    };
  },

  async resolveMigrationId(h: RunHandle) {
    return h.migrationId ?? null;
  },
};
