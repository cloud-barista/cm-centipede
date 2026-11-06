import type { TargetDataMigrationModel, TargetDataMigrationProperty } from '@/types/target';

export function unwrapPlan(plan: TargetDataMigrationModel): TargetDataMigrationProperty {
  return plan.targetDataMigrationModel;
}

export function wrapPlan(property: TargetDataMigrationProperty): TargetDataMigrationModel {
  return { targetDataMigrationModel: property };
}

/** Every errors[] the planner reported, across all three resource types. */
export function planErrors(plan: TargetDataMigrationModel | null): string[] {
  if (!plan) return [];
  const p = unwrapPlan(plan);
  return [
    ...(p.fileSystems ?? []).flatMap((m) => (m.errors ?? []).map((e) => `filesystem: ${e}`)),
    ...(p.objectStorages ?? []).flatMap((m) => (m.errors ?? []).map((e) => `objectstorage: ${e}`)),
    ...(p.databases ?? []).flatMap((m) => (m.errors ?? []).map((e) => `database: ${e}`)),
  ];
}

/** True when the plan carries no migratable unit at all. */
export function planIsEmpty(plan: TargetDataMigrationModel | null): boolean {
  if (!plan) return true;
  const p = unwrapPlan(plan);
  return (
    (p.fileSystems?.length ?? 0) === 0 &&
    (p.objectStorages?.length ?? 0) === 0 &&
    (p.databases?.length ?? 0) === 0
  );
}
