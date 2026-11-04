// One wizard shell, three domain adapters. The pipeline is identical across
// categories; only the unit's meaning, the inspect parameters and which filter
// field the plan request carries differ.

import type { Category } from '@/lib/connectionCategory';
import type { ImportReq } from '@/api/honeybee';
import type {
  SourceDataMigrationModel,
  SourceDBModel,
  SourceFileSystemModel,
  SourceObjectStorageModel,
} from '@/types/source';
import type { TargetTab } from '@/components/connections/TargetPicker';

/** One selectable unit inside a scan result: a folder, a prefix or a database. */
export interface SourceUnit {
  name: string;
  /** A short right-hand summary — counts and sizes where the scan has them. */
  detail: string;
  /**
   * The unit this one sits under, named exactly as that unit's `name`.
   *
   * For a filesystem or object storage listing this is left unset: those names
   * ARE paths, and the tree derives the nesting from them. A database listing
   * has no such luck — an identifier may itself contain "/" — so it says where
   * each row belongs instead of encoding it in a string to be parsed back.
   */
  parent?: string;
  /** What the row shows, when that is not derivable from `name`. */
  label?: string;
}

/**
 * How detailed an inspect should be — never what it should read.
 *
 * The scan target used to live here, as a path or a bucket the wizard asked
 * for: cm-honeybee moved it onto the connection (fs_scan_path, os_scan_bucket
 * + os_scan_prefix, db_name) and its import requests carry only these. The
 * wizard shows the connection's target and does not offer to change it.
 */
export interface InspectParams {
  /** Filesystem only — 0 walks the whole tree. The other two have no depth. */
  maxDepth: number;
  metrics: Record<string, boolean>;
}

/** One connection's part of a refined response. */
export type SourceEntry = SourceFileSystemModel | SourceObjectStorageModel | SourceDBModel;

/** A refined entry and the honeybee connection it was scanned from. */
export interface ScopedEntry<E extends SourceEntry = SourceEntry> {
  connId: string;
  entry: E;
}

/**
 * Per-entry rather than per-response: a source group's refined response holds
 * one entry per scanned connection, and a single connection's holds one. The
 * methods below read a single entry, so the same code serves both — reading
 * `[0]` of the response would silently describe one host of a group as all of it.
 *
 * `E` narrows the entry type inside each adapter. The methods are declared as
 * methods (bivariant parameters), which is what lets the three adapters sit in
 * one `Record<Category, DomainAdapter>`.
 */
export interface DomainAdapter<E extends SourceEntry = SourceEntry> {
  category: Category;
  /** What a TargetMapping name points at in this domain. */
  unitLabel: string;
  /**
   * One line under the wizard's title, saying what it moves and between what.
   *
   * It used to read "unit: Folder · source cm-honeybee → plan and run
   * cm-centipede": a label with no sentence around it, followed by which service
   * owns which half — the system's business, not the reader's.
   */
  summary: string;
  /** Which cm-beetle lists can be a destination here. */
  targetTabs: TargetTab[];
  /** Inline target sources usable as a destination here. */
  inlineSources: Array<'ssh' | 'minio' | 'db'>;
  /** Defaults for the inspect form. */
  defaultInspect: InspectParams;
  /**
   * Metric checkboxes offered, in display order.
   *
   * `dbType` is the selected connection's engine, and only the database
   * adapter reads it: half of cm-honeybee's database metrics are engine-specific, and
   * the server drops what the engine has no equivalent for. Dropping silently
   * is the right server behaviour and the wrong thing to build a form on — a
   * checkbox that does nothing is worse than one that is absent.
   */
  metricKeys(dbType?: string): string[];
  buildImportReq(p: InspectParams, dbType?: string): ImportReq;
  /** This domain's entries of a refined response, each with its connection id. */
  entriesOf(model: SourceDataMigrationModel | undefined): ScopedEntry<E>[];
  /**
   * The scan root, read out of a refined entry. A person typing this makes
   * the plan fail silently — object storage roots are normalized to "bucket/"
   * with a trailing slash.
   */
  srcNameOf(entry: E | undefined): string;
  /** The units the filter step can select from. */
  unitsOf(entry: E | undefined): SourceUnit[];
  /**
   * The scan-wide figures, as one line. Empty when the metrics that produce
   * them were not asked for.
   *
   * Deliberately not a row in unitsOf. They do not mean what a row means:
   * data-mig computes these recursively over the whole scan, while a folder or
   * prefix entry carries only its own direct contents (its FSEntry and OSEntry
   * both say so). A synthesised root row put the two in one column, where the
   * scan root read as the largest folder in a list it was not comparable to.
   */
  totalsOf(entry: E | undefined): string;
}

/** The honeybee connection a refined entry was scanned from — '' when it names none. */
function connIdOf(e: SourceEntry): string {
  return e.connection?.honeybee?.connectionId ?? '';
}

function scoped<E extends SourceEntry>(list: E[] | undefined): ScopedEntry<E>[] {
  return (list ?? []).map((entry) => ({ connId: connIdOf(entry), entry }));
}

/**
 * The entry a refined response holds for one connection, or undefined when the
 * scan has none for it.
 *
 * Matched by connection id, never by position: a group response leaves out the
 * connections that were not scanned, so the n-th entry is not the n-th
 * connection of the group.
 */
export function entryOf(
  adapter: DomainAdapter,
  model: SourceDataMigrationModel | undefined,
  connId: string | null | undefined,
): SourceEntry | undefined {
  if (!connId) return undefined;
  return adapter.entriesOf(model).find((e) => e.connId === connId)?.entry;
}

function pickMetrics(metrics: Record<string, boolean>, keys: string[]): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  for (const k of keys) out[k] = !!metrics[k];
  return out;
}

const FS_METRICS = [
  'total_size',
  'file_count',
  'extension_count',
  'folder_mod_time',
  'folder_perms',
  'folder_file_count',
  'folder_file_size',
];

const OS_METRICS = [
  'total_size',
  'object_count',
  'extension_count',
  'prefix_mod_time',
  'prefix_object_count',
  'prefix_object_size',
];

// cm-honeybee's database metrics, split into common, relational and per-engine.
const DB_METRICS_ALL = ['row_count_exact', 'columns', 'indexes', 'views'];
const DB_METRICS_RELATIONAL = ['foreign_keys', 'functions', 'procedures', 'triggers'];
const DB_METRICS_BY_ENGINE: Record<string, string[]> = {
  mysql: [...DB_METRICS_ALL, ...DB_METRICS_RELATIONAL, 'events'],
  mariadb: [...DB_METRICS_ALL, ...DB_METRICS_RELATIONAL, 'events'],
  postgresql: [
    ...DB_METRICS_ALL,
    ...DB_METRICS_RELATIONAL,
    'materialized_views',
    'sequences',
    'types',
    'extensions',
    'rules',
  ],
  mongodb: [...DB_METRICS_ALL, 'validators'],
};

/**
 * Before a connection is picked there is no engine to ask about, so the common
 * four stand in. They are the intersection of every engine's list, which makes
 * this the one answer that is never wrong — the form fills out once the
 * connection is chosen.
 *
 * Step 1 has to SAY which of the two it is showing. Silently, this reads as a
 * database inspect that collects four things; the metric field therefore names
 * the engine when it has one and says the list is the common subset when it
 * does not.
 */
function dbMetricKeys(dbType?: string): string[] {
  return DB_METRICS_BY_ENGINE[dbType ?? ''] ?? DB_METRICS_ALL;
}

function allOn(keys: string[]): Record<string, boolean> {
  return Object.fromEntries(keys.map((k) => [k, true]));
}

/**
 * Every database metric any engine has.
 *
 * The default turns all of them on, matching the other two domains. Only the
 * selected engine's keys ever reach the request — buildImportReq filters
 * through dbMetricKeys — so the entries for the other engines sit in this
 * record unused rather than being sent and dropped.
 */
const DB_METRICS_EVERY = [...new Set(Object.values(DB_METRICS_BY_ENGINE).flat())];

const filesystemAdapter: DomainAdapter<SourceFileSystemModel> = {
  category: 'filesystem',
  unitLabel: 'Folder',
  summary: 'Copy folders from a scanned SSH host to a destination you choose',
  // A file system source can land on object storage — cross-storage.
  targetTabs: ['infra', 'objectStorage'],
  inlineSources: ['ssh', 'minio'],
  // maxDepth 3, not 0. A whole-tree walk of a real host answers with tens of
  // thousands of folders, and the reader needs the shape of the scan root to
  // pick what to migrate, not every leaf under it. It bounds the LISTING only:
  // the folder chosen in Step 3 is copied in full whatever depth was scanned.
  defaultInspect: { maxDepth: 3, metrics: allOn(FS_METRICS) },
  metricKeys: () => FS_METRICS,
  buildImportReq: (p) => ({
    max_depth: p.maxDepth,
    metric: pickMetrics(p.metrics, FS_METRICS),
  }),
  entriesOf: (m) => scoped(m?.sourceDataMigrationModel.fileSystems),
  srcNameOf: (fs) => fs?.path ?? '',
  unitsOf: (fs) => {
    if (!fs) return [];
    // The walk lists the scan root itself, so folders already holds a row for
    // it carrying the direct counts every other row carries. Passed through as
    // it comes rather than replaced by a summary of the whole tree.
    const folders = (fs.folders ?? []).map((f) => ({
      name: f.path,
      detail: summarize(f.fileCount, 'files', f.fileSize),
    }));
    // Only if a filter left the listing empty: the step still needs something
    // to select, and then there is no column of direct counts to be unlike.
    return folders.length > 0
      ? folders
      : [{ name: fs.path, detail: summarize(fs.fileCount, 'files', fs.totalSize) }];
  },
  totalsOf: (fs) => (fs ? summarize(fs.fileCount, 'files', fs.totalSize) : ''),
};

const objectStorageAdapter: DomainAdapter<SourceObjectStorageModel> = {
  category: 'objectstorage',
  unitLabel: 'Bucket',
  summary: 'Copy buckets from scanned object storage to a destination you choose',
  targetTabs: ['objectStorage', 'infra'],
  inlineSources: ['minio', 'ssh'],
  defaultInspect: { maxDepth: 0, metrics: allOn(OS_METRICS) },
  metricKeys: () => OS_METRICS,
  buildImportReq: (p) => ({ metric: pickMetrics(p.metrics, OS_METRICS) }),
  entriesOf: (m) => scoped(m?.sourceDataMigrationModel.objectStorages),
  srcNameOf: (os) => os?.path ?? '',
  unitsOf: (os) => {
    if (!os) return [];
    // As for filesystem: the prefix listing already includes the scan root.
    const prefixes = (os.folders ?? []).map((f) => ({
      name: f.key,
      detail: summarize(f.objectCount, 'objects', f.size),
    }));
    return prefixes.length > 0
      ? prefixes
      : [{ name: os.path, detail: summarize(os.objectCount, 'objects', os.totalSize) }];
  },
  totalsOf: (os) => (os ? summarize(os.objectCount, 'objects', os.totalSize) : ''),
};

const dbmsAdapter: DomainAdapter<SourceDBModel> = {
  category: 'database',
  unitLabel: 'Database',
  summary: 'Copy databases from a scanned server to a destination you choose',
  targetTabs: ['rdbms'],
  inlineSources: ['db'],
  // Everything on, as for filesystem and object storage. It is the costly
  // default of the three: columns and indexes can each run to megabytes and
  // row_count_exact walks every table. The Inspect step says so, and a metric
  // is one checkbox away from off.
  defaultInspect: { maxDepth: 0, metrics: allOn(DB_METRICS_EVERY) },
  metricKeys: dbMetricKeys,
  // Only the engine's own keys are sent. The server would drop the others
  // anyway; leaving them out means the request says what was actually asked.
  buildImportReq: (p, dbType) => ({ metric: pickMetrics(p.metrics, dbMetricKeys(dbType)) }),
  entriesOf: (m) => scoped(m?.sourceDataMigrationModel.databases),
  // A DBMS source has no single scan root: the databases themselves are the units.
  /**
   * What the scan covered, which for a database source is a SET.
   *
   * There is no single root here the way a folder or a prefix is one: honeybee
   * reads the connection's db_name, and an empty one means every user database
   * on the server. This used to name the first database alone, which put "hr_db" next
   * to a total reading "2 databases" — one of the two, presented as the whole.
   *
   * Two names, then a count: the full list is the unit rows below, and this line
   * also carries the totals, so it has to stay one line.
   */
  srcNameOf: (server) => {
    const names = (server?.databases ?? [])
      .map((d) => d.database)
      .filter(Boolean);
    if (names.length <= 2) return names.join(', ');
    return `${names.slice(0, 2).join(', ')} +${names.length - 2}`;
  },
  /**
   * Databases, and what is inside them — the shape the object storage listing
   * has, where a bucket opens into its prefixes.
   *
   * A database was one flat row carrying "12 tables", which said how many there
   * were and not which, although the scan had already read every one of them.
   * The tables are in the response; this lists them under the database they
   * belong to, and under their schema where the engine has schemas.
   *
   * Nesting is stated with `parent`, not spelled into the name: an SQL
   * identifier may contain a slash, and a name parsed as a path would put such a
   * table under a database that does not exist.
   */
  unitsOf: (server) => {
    if (!server?.databases) return [];

    const units: SourceUnit[] = [];
    for (const d of server.databases) {
      units.push({ name: d.database, detail: summarize(d.tableCount, 'tables', d.totalSize) });

      // PostgreSQL only: the level between a database and its tables. The other
      // engines report none, and their tables hang off the database directly.
      for (const schema of d.pgSchema ?? []) {
        units.push({
          name: `${d.database}/${schema.name}`,
          parent: d.database,
          label: schema.name,
          detail: summarize(schema.tableCount, 'tables', schema.totalSize),
        });
      }

      for (const t of d.tables ?? []) {
        const parent = t.pgSchema ? `${d.database}/${t.pgSchema}` : d.database;
        units.push({
          name: `${parent}/${t.name}`,
          parent,
          label: t.name,
          detail: summarize(t.rowCount, 'rows', t.totalSize),
        });
      }
    }
    return units;
  },
  totalsOf: (server) => {
    if (!server?.databases?.length) return '';
    const size = server.databases.reduce((acc, d) => acc + (d.totalSize ?? 0), 0);
    const n = server.databases.length;
    // The server version used to be repeated on every database row, where it was
    // the same string every time: one server answers one version. It belongs to
    // the scan as a whole, which is this line.
    const version = server.databases[0]?.serverVersion;
    const parts = [`${n} database${n === 1 ? '' : 's'}`, formatSize(size)];
    if (version) parts.push(`server ${version}`);
    return parts.join(' · ');
  },
};

export const ADAPTERS: Record<Category, DomainAdapter> = {
  filesystem: filesystemAdapter,
  objectstorage: objectStorageAdapter,
  database: dbmsAdapter,
};

/**
 * Empty rather than an em dash when nothing was collected: the caller decides
 * how absence reads. A table cell wants a dash to keep the column; a summary
 * line wants not to be there at all.
 */
function summarize(count: number | undefined, noun: string, size: number | undefined): string {
  const parts: string[] = [];
  // "1 table", not "1 tables". Every noun passed here is a regular plural, and
  // the totals line beside these rows has always made the distinction.
  if (count !== undefined) {
    parts.push(`${count.toLocaleString()} ${count === 1 ? noun.replace(/s$/, '') : noun}`);
  }
  if (size !== undefined) parts.push(formatSize(size));
  return parts.join(' · ');
}

function formatSize(bytes: number | undefined): string {
  if (bytes === undefined) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  if (bytes === 0) return '0 B';
  const i = Math.min(Math.floor(Math.log(Math.abs(bytes)) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
