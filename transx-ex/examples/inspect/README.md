# Inspect Example with transx-ex

This example demonstrates how to use `InspectFilesystem`, `InspectObjectStorage` and `InspectDBMS` to list directories, objects or database schema metadata from a given `StorageLocation` / `DBMSLocation`.

## Overview

`InspectFilesystem` lists directories on a remote filesystem via SSH.  
`InspectObjectStorage` lists objects in an S3-compatible bucket.  
`InspectDBMS` reports server and schema metadata for a database.

The tool reads a single JSON config and routes on its discriminator: `storageType`
selects `InspectFilesystem`/`InspectObjectStorage` (a `StorageLocation`), while
`dbmsType` selects `InspectDBMS` (a `DBMSLocation`).

### Supported Configurations

| Config | Discriminator | Access | Description |
|--------|---------------|--------|-------------|
| `template-config-fs.json` | `storageType: filesystem` | SSH | List directories on a remote server |
| `template-config-minio.json` | `storageType: objectstorage` | MinIO / S3 | List objects in a bucket |
| `template-config-mysql.json` | `dbmsType: mysql` | direct | Server / schema metadata for a database |
| `template-config-fs-metric.json` | `storageType: filesystem` | SSH | Rules filter + filesystem metric |
| `template-config-minio-metric.json` | `storageType: objectstorage` | MinIO / S3 | Rules filter + object storage metric |
| `template-config-mysql-metric.json` | `dbmsType: mysql` | direct | All MySQL metrics (schema objects, exact rows) |

## Prerequisites

- Go 1.26 or higher
- Docker installed and running
- For `accessType: "ssh"` only: the **remote** host must provide GNU findutils
  and coreutils (`find -printf`, `stat -c`) — see [SSH access requirements](#ssh-access-requirements)

> **No additional tools required.** MinIO Client (`mc`) runs inside the MinIO container via `docker exec` — no host installation needed, and the same applies to the `mysql` client used to seed the database.

## SSH access requirements

`InspectFilesystem` with `accessType: "ssh"` drives the scan with two remote
commands, so the remote host must satisfy the following:

| Requirement | Why | Symptom when unmet |
|---|---|---|
| GNU findutils (`find -printf`) | Directory and file records are emitted with `-printf` | Inspect fails outright on BusyBox/Alpine, BSD or macOS hosts |
| GNU coreutils (`stat -c '%d'`) | Device id of the scan root's parent, for `isMount` | `isMount` falls back to `false` |
| `path` must be absolute | A relative path resolves against the remote login directory, breaking the absolute-path contract of `path` in the result | Request is rejected before connecting |
| Read permission over the whole scan tree | `find` exits non-zero on `Permission denied` | Inspect fails, with the remote stderr included in the error |

All commands for one inspect call share a single SSH connection.

## local vs ssh parity

Both access types support **every** `metric.filesystem` flag, and the same rules
apply to each: `maxDepth` and the filter bound the `folders` listing only, while
`totalSize`/`fileCount`/`extensionCounts` aggregate the whole path recursively.
The remaining behavioural notes:

| Aspect | Behaviour |
|---|---|
| What counts as a file | Regular files only, on both paths — symlinks, sockets, FIFOs and device nodes are excluded from every metric |
| `folders` order | Lexical per path segment on both paths (a parent always precedes its children) |
| `permissions` format | `ls` style on both paths (`drwxr-xr-x`, `drwxrwxrwt`) |
| Names with tabs or newlines | Handled: remote records are NUL-terminated and carry the path last |
| Virtual filesystems | `VirtualFSPaths` (`/proc`, `/sys`, `/dev`, `/run`) pruned on both paths, unless the scan root is one of them |

## Quick Start

### 1. Setup Environment

```bash
chmod +x setup_environment.sh inspect.sh

# Start the SSH, MinIO and MySQL containers with test data
./setup_environment.sh all
```

This will:
- Build a lightweight SSH container (Ubuntu 22.04 + openssh-server)
- Generate an SSH keypair in `./ssh_keys/`
- Create test directories under `/data` in the SSH container
- Start a MinIO container and upload test objects via `docker exec` (no extra containers or host mounts)
- Start a MySQL container and seed `inspect_test` with tables, views, functions, procedures, triggers and an event
- Generate ready-to-use `config-fs.json`, `config-minio.json`, `config-mysql.json`, `config-fs-metric.json`, `config-minio-metric.json` and `config-mysql-metric.json`

### 2. Run Inspect

```bash
# Inspect SSH remote directories (table output)
./inspect.sh -c config-fs.json

# Inspect MinIO bucket (table output)
./inspect.sh -c config-minio.json

# Inspect the MySQL database
./inspect.sh -c config-mysql.json

# JSON output
./inspect.sh -c config-minio.json -f json

# Verbose
./inspect.sh -c config-fs.json -v
```

The generated config files include a `filter` block with `maxDepth: 0` (unlimited depth) and folder exclude rules.
Excluded directories (`tmp`, `temp`, `logs` for SSH; `tmp`, `temp`, `backup` for MinIO) are present
in the test data so you can observe the filter in action.

## Configuration Files

### Template vs Generated

| File | Purpose |
|------|---------|
| `template-config-fs.json` | Template with placeholders for your own SSH server |
| `template-config-minio.json` | Template with placeholders for your own S3 / MinIO |
| `template-config-mysql.json` | Template with placeholders for your own MySQL server |
| `template-config-fs-metric.json` | Template — rules filter + filesystem metric |
| `template-config-minio-metric.json` | Template — rules filter + object storage metric |
| `template-config-mysql-metric.json` | Template — every MySQL metric enabled |
| `config-fs.json` | Auto-generated by `setup_environment.sh` — points to the SSH container |
| `config-minio.json` | Auto-generated by `setup_environment.sh` — points to the MinIO container |
| `config-mysql.json` | Auto-generated by `setup_environment.sh` — points to the MySQL container |
| `config-fs-metric.json` | Auto-generated — rules filter + filesystem metric |
| `config-minio-metric.json` | Auto-generated — rules filter + object storage metric |
| `config-mysql-metric.json` | Auto-generated — every MySQL metric enabled |

The generated `config-*.json` files are created by `setup_environment.sh all` and ignored by git.

### SSH Filesystem (`template-config-fs.json`)

```json
{
  "storageType": "filesystem",
  "path": "/home/YOUR_USERNAME/data",
  "filesystem": {
    "accessType": "ssh",
    "ssh": {
      "host": "YOUR_HOST",
      "port": 22,
      "username": "YOUR_USERNAME",
      "privateKeyPath": "/home/YOUR_LOCAL_USERNAME/.ssh/id_rsa"
    }
  },
  "filter": {
    "maxDepth": 0,
    "exclude": ["tmp", "temp", "logs"]
  }
}
```

### MinIO / S3 (`template-config-minio.json`)

```json
{
  "storageType": "objectstorage",
  "path": "YOUR_BUCKET_NAME/",
  "objectStorage": {
    "accessType": "minio",
    "minio": {
      "endpoint": "s3.YOUR_REGION.amazonaws.com",
      "accessKeyId": "YOUR_ACCESS_KEY_ID",
      "secretAccessKey": "YOUR_SECRET_ACCESS_KEY",
      "region": "YOUR_REGION",
      "useSSL": true
    }
  },
  "filter": {
    "maxDepth": 0,
    "exclude": ["tmp", "temp", "backup"]
  }
}
```

### MySQL DBMS (`template-config-mysql.json`)

A database config is a `DBMSLocation`, so it carries `dbmsType` instead of
`storageType` and has no `path`/`filter` — `InspectDBMS` reads the whole schema
and selects detail through `metric` only.

```json
{
  "dbmsType": "mysql",
  "database": "YOUR_DATABASE",
  "accessType": "direct",
  "direct": {
    "host": "YOUR_HOST",
    "port": 3306,
    "username": "YOUR_USERNAME",
    "password": "YOUR_PASSWORD"
  }
}
```

`accessType: "ssh-tunnel"` is also supported — replace `direct` with an
`sshTunnel` block. Supported `dbmsType` values: `mysql`, `mariadb`,
`postgresql`, `mongodb`.

### Filter Options

Filesystem and object storage only; `InspectDBMS` ignores `filter`.

| Field | Type | Description |
|-------|------|-------------|
| `maxDepth` | int | Maximum scan depth relative to root. `0` = unlimited (default) |
| `include` | []string | (simple form) Glob patterns to include (whitelist). |
| `exclude` | []string | (simple form) Glob patterns to exclude. Matched against the folder base name at any depth. |
| `rules` | []Rule | Ordered filter pipeline (first match wins). See below. |

`exclude`/`include` match against the **base name** of each directory, so `"tmp"` excludes `/data/tmp/`, `/data/project/tmp/`, etc. at any depth.

#### Filter pipeline (`rules`)

`rules` is an ordered list of typed filters evaluated top-to-bottom; the first
matching rule decides (include keeps, exclude drops). No match → included.

```json
{
  "filter": {
    "rules": [
      { "action": "exclude", "type": "size", "op": ">", "value": 10485760 },
      { "action": "include", "type": "glob", "pattern": "**/*.go" },
      { "action": "exclude", "type": "glob", "pattern": "*" }
    ]
  }
}
```

Built-in filter types: `glob` (`pattern`) and `size` (`op` one of `> >= < <= ==`, `value` bytes).
The simple `include`/`exclude` form applies when `rules` is empty.

### Metric Options (extracted information)

`metric.filesystem` / `metric.objectStorage` select what inspect extracts.
Fields are `*bool` (partial merge). Filesystem defaults: `folderModTime`/`folderPerms`
on, everything else off. Object storage: all off.

```json
{
  "metric": {
    "filesystem": {
      "totalSize": true, "fileCount": true, "extensionCount": true,
      "folderFileCount": true, "folderFileSize": true
    }
  }
}
```

| filesystem | object storage | Description |
|---|---|---|
| `totalSize` | `totalSize` | Total recursive size |
| `fileCount` | `objectCount` | Total file / object count |
| `extensionCount` | `extensionCount` | Count per extension |
| `folderModTime` | `prefixModTime` | Folder / prefix timestamp |
| `folderPerms` | — | Folder permissions |
| `folderFileCount` | `prefixObjectCount` | Direct (non-recursive) count per folder / prefix |
| `folderFileSize` | `prefixObjectSize` | Direct (non-recursive) size per folder / prefix |

### Metric Options — DBMS

`metric.mysql` (and `metric.mariadb` / `metric.postgresql` / `metric.mongodb`)
selects what `InspectDBMS` extracts beyond the always-collected server version,
size aggregates and table list. All flags default to off.

```json
{
  "metric": {
    "mysql": {
      "rowCountExact": true, "columns": true, "indexes": true,
      "foreignKeys": true, "views": true, "functions": true,
      "procedures": true, "triggers": true, "events": true
    }
  }
}
```

| Flag | Description |
|---|---|
| `rowCountExact` | Exact `SELECT COUNT(*)` per table instead of the statistics estimate |
| `columns` | Per-table column metadata (name, type, nullability, default, PK) |
| `indexes` | Per-table index metadata (name, columns, uniqueness, type) |
| `foreignKeys` | Foreign-key constraint names |
| `views` / `functions` / `procedures` / `triggers` / `events` | Schema-object name inventories |

> Engine-specific inventories exist for the other drivers — `materializedViews`,
> `sequences`, `types`, `extensions`, `rules` (PostgreSQL) and `validators`
> (MongoDB). Unrequested inventories stay `nil` and are omitted from JSON output.

## Environment Details

After running `./setup_environment.sh all`:

### SSH Container

| Item | Value |
|------|-------|
| Container | `transx-inspect-ssh` |
| Host / Port | `localhost:2222` |
| Username | `root` |
| Auth | Public key (`./ssh_keys/id_rsa`) |
| Test directories | `/data/project-alpha/src`, `/data/project-alpha/docs`, `/data/project-alpha/temp` *(excluded)*, `/data/project-beta/archive/2024`, `/data/project-beta/logs` *(excluded)*, `/data/shared/config`, `/data/tmp` *(excluded)* |

### MinIO Container

| Item | Value |
|------|-------|
| Container | `transx-inspect-minio` |
| API Endpoint | `http://localhost:9000` |
| Console | `http://localhost:9001` |
| Access Key | `minioadmin` |
| Secret Key | `minioadmin` |
| Bucket | `inspect-test` |
| Test objects | `sample/hello.txt`, `sample/data.json`, `sample/users.csv`, `sample/logs/app.log`, `sample/archive/old.tar.gz`, `sample/tmp/scratch.txt` *(excluded)*, `sample/temp/work.txt` *(excluded)*, `sample/backup/snapshot.tar.gz` *(excluded)* |

### MySQL Container

| Item | Value |
|------|-------|
| Container | `transx-inspect-mysql` |
| Image | `mysql:latest` |
| Host / Port | `localhost:3308` (3306/3307 are left to the `mysql-migration` example) |
| Credentials | `root` / `inspectpass` |
| Database | `inspect_test` |
| Test objects | 5 tables (`users`, `products`, `orders`, `order_items`, `audit_log`), 2 views, 2 functions, 2 procedures, 2 triggers, 1 event |

## Output Formats

`InspectFilesystem` returns `*FSInspectResult` (aggregate summary + `folders`),
`InspectObjectStorage` returns `*OSInspectResult` and `InspectDBMS` returns
`*DBMSInfo` (server/size summary + `tables`). Per-item metric columns show
`-` when the corresponding metric is not requested.

The table output splits the summary into three blocks so the aggregate and the
per-folder columns cannot be mistaken for each other:

| Block | Meaning |
|---|---|
| `TOTAL` | The original: every file/object under `path`, **ignoring the filter and `maxDepth`**. Printed only for the aggregates the metric requested (`totalSize`, `fileCount`/`objectCount`, `extensionCount`) |
| `FILTERED` | What the filter actually removed, listed by path/key. Requires a second, unfiltered scan — see `-excluded` below |
| `RESULT` | What the table underneath lists and sums to |

`TOTAL = FILTERED + RESULT`, which is why `TOTAL FILES` can exceed the sum of the
table's `FILES` column. `FILTERED` is skipped when the config has no filter, and
`TOTAL` is skipped when no aggregate metric was requested.

### Table (default) — filesystem

Summary blocks first, then the folder table. Directories excluded by filter
(`tmp`, `temp`, `logs`) are listed under `FILTERED`, not in the table.
`FILES`/`FSIZE` are `-` unless the matching metric is enabled.

```
PATH:   /data
MOUNT:  false

FILTERED (removed by the filter — 3 folders)
  /data/project-alpha/temp
  /data/project-beta/logs
  /data/tmp

RESULT (after filtering — 9 folders)

PATH                              MOUNT  FILES  FSIZE  MOD_TIME              PERMISSIONS
/data                             false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-alpha               false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-alpha/docs          false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-alpha/src           false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-beta                false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-beta/archive        false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/project-beta/archive/2024   false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/shared                      false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
/data/shared/config               false  -      -      2024-01-01T00:00:00Z  drwxr-xr-x
```

With `metric.filesystem` aggregates enabled, a `TOTAL` block appears above and
the `FILES`/`FSIZE` columns fill in — see the metric example further down.

### Table (default) — object storage

`InspectObjectStorage` returns unique folder prefixes. Excluded prefixes
(`tmp/`, `temp/`, `backup/`) appear under `FILTERED`, not in the table.
`OBJECTS`/`SIZE`/`LAST_MODIFIED` are populated only when the matching
`metric.objectStorage` flag is set.

```
PATH:   inspect-test/sample/

FILTERED (removed by the filter — 3 prefixes)
  inspect-test/sample/backup/
  inspect-test/sample/temp/
  inspect-test/sample/tmp/

RESULT (after filtering — 3 prefixes)

KEY                           OBJECTS  SIZE  LAST_MODIFIED
inspect-test/sample/          -        -
inspect-test/sample/archive/  -        -
inspect-test/sample/logs/     -        -
```

### Table (default) — DBMS

Summary lines carry the server version and size aggregates, which are always
collected; the `COLUMNS`/`INDEXES` columns count per-table metadata and read `-`
until the matching metric is enabled. Schema-object inventory lines
(`VIEWS:`, `FUNCS:`, …) appear only for the metrics that were requested.

```
DATABASE:     inspect_test
SERVER:       9.4.0
TOTAL_SIZE:   311296 bytes (data 245760, index 65536)
TOTAL_TABLES: 5

NAME         ROWS  DATA   INDEX  TOTAL  ENGINE  COLUMNS  INDEXES
audit_log    3     16384  0      16384  InnoDB  -        -
order_items  4     16384  32768  49152  InnoDB  -        -
orders       3     16384  16384  32768  InnoDB  -        -
products     4     16384  0      16384  InnoDB  -        -
users        3     16384  16384  32768  InnoDB  -        -
```

### JSON (`-f json`)

Default (no metric) — object storage. `size`/`lastModified`/`objectCount` are
omitted (via `omitempty`) until requested; `OSEntry` carries no `ETag`.

```json
{
  "path": "inspect-test/sample/",
  "folders": [
    { "key": "inspect-test/sample/" },
    { "key": "inspect-test/sample/archive/" },
    { "key": "inspect-test/sample/logs/" }
  ]
}
```

## Advanced example: metric + filter rules

`setup_environment.sh` also generates `config-fs-metric.json`,
`config-minio-metric.json` and `config-mysql-metric.json`. The first two combine
a `rules` filter pipeline with all metrics enabled: aggregates
(`SIZE`/`FILES`/`EXT`) cover the whole path **ignoring the filter/maxDepth**,
while the folder listing still respects them. The MySQL one enables every
metric flag.

```bash
./inspect.sh -c config-fs-metric.json
./inspect.sh -c config-minio-metric.json
./inspect.sh -c config-mysql-metric.json
```

### Filesystem (`config-fs-metric.json`)

```
PATH:   /data
MOUNT:  false

TOTAL (original — whole path, filter and maxDepth ignored)
  SIZE:    70 bytes
  FILES:   6
  EXT:     .go=1, .log=1, .md=1, .txt=2, .yaml=1

FILTERED (removed by the filter — 3 folders, 3 files, 39 bytes)
  /data/project-alpha/temp  1  10
  /data/project-beta/logs   1  21
  /data/tmp                 1  8

RESULT (after filtering — 9 folders, 3 files, 31 bytes)

PATH                             MOUNT  FILES  FSIZE  MOD_TIME              PERMISSIONS
/data                            false  0      0      2026-...              drwxr-xr-x
/data/project-alpha              false  0      0      2026-...              drwxr-xr-x
/data/project-alpha/docs         false  1      9      2026-...              drwxr-xr-x
/data/project-alpha/src          false  1      13     2026-...              drwxr-xr-x
/data/project-beta               false  0      0      2026-...              drwxr-xr-x
/data/project-beta/archive       false  0      0      2026-...              drwxr-xr-x
/data/project-beta/archive/2024  false  0      0      2026-...              drwxr-xr-x
/data/shared                     false  0      0      2026-...              drwxr-xr-x
/data/shared/config              false  1      9      2026-...              drwxr-xr-x
```

> `TOTAL` counts every file under `/data`, `RESULT` only the folders the filter
> kept: 6 = 3 (filtered) + 3 (result), 70 bytes = 39 + 31. That is also why
> `TOTAL EXT` includes `.log` and two `.txt` — they live in the excluded
> `logs/`, `temp/` and `tmp/`. `FILES`/`FSIZE` in the table are the direct
> (non-recursive) counts per folder.

### Object storage (`config-minio-metric.json`)

```
PATH:   inspect-test/sample/

TOTAL (original — whole prefix, filter and maxDepth ignored)
  SIZE:     120 bytes
  OBJECTS:  8
  EXT:      .csv=1, .gz=2, .json=1, .log=1, .txt=3

FILTERED (removed by the filter — 3 prefixes, 3 objects, 34 bytes)
  inspect-test/sample/backup/  1  14
  inspect-test/sample/temp/    1  10
  inspect-test/sample/tmp/     1  10

RESULT (after filtering — 3 prefixes, 5 objects, 86 bytes)

KEY                           OBJECTS  SIZE  LAST_MODIFIED
inspect-test/sample/          3        51    2026-...
inspect-test/sample/archive/  1        14    2026-...
inspect-test/sample/logs/     1        21    2026-...
```

### DBMS (`config-mysql-metric.json`)

With every flag on, `ROWS` becomes an exact count and `COLUMNS`/`INDEXES` fill
in, followed by the schema-object inventories.

```
DATABASE:     inspect_test
SERVER:       9.4.0
TOTAL_SIZE:   311296 bytes (data 245760, index 65536)
TOTAL_TABLES: 5
FKEYS:    fk_items_order, fk_items_product, fk_orders_user
VIEWS:    v_order_summary, v_product_stock
FUNCS:    fn_is_in_stock, fn_total_order_amount
PROCS:    sp_get_user_orders, sp_restock_product
TRIGGERS: trg_orders_after_insert, trg_stock_after_order
EVENTS:   evt_cleanup_old_logs

NAME         ROWS  DATA   INDEX  TOTAL  ENGINE  COLUMNS  INDEXES
audit_log    3     16384  0      16384  InnoDB  4        1
order_items  4     16384  32768  49152  InnoDB  5        3
orders       3     16384  16384  32768  InnoDB  4        2
products     4     16384  0      16384  InnoDB  5        1
users        3     16384  16384  32768  InnoDB  4        2
```

> Sizes come from `information_schema` and are allocation-based, so small tables
> all report one 16 KB page. `ROWS` without `rowCountExact` is an InnoDB
> statistics estimate and can read `0` on a freshly loaded table.

### Whitelist with the pipeline (first match wins)

To keep only certain folders, put the includes first and a catch-all exclude last:

```json
{
  "filter": {
    "rules": [
      { "action": "include", "type": "glob", "pattern": "project-*" },
      { "action": "exclude", "type": "glob", "pattern": "*" }
    ]
  }
}
```

> `size` filters apply to file bytes and are most useful for transfers; inspect
> lists directories/prefixes, so prefer `glob` rules here.

## inspect.sh Options

```
-c, --config FILE    StorageLocation / DBMSLocation configuration file (required)
-f, --format FORMAT  Output format: table | json  (default: table)
-v, --verbose        Enable verbose logging
-n, --no-excluded    Skip the FILTERED block, which costs a second unfiltered scan
-h, --help           Show this help message
```

The `FILTERED` block needs to know what the filter removed, and
`InspectFilesystem`/`InspectObjectStorage` only return what survived — so the
example runs the scan a second time with `filter` dropped and diffs the two
folder lists. That doubles the remote work, so pass `-n` (or `-excluded=false`
to the binary) on large trees or buckets. Configs without a filter never trigger
the second scan.

## setup_environment.sh Commands

```
all        - Setup SSH + MinIO + MySQL containers with test data (default)
ssh        - Setup only SSH container
minio      - Setup only MinIO container
mysql      - Setup only MySQL container
cleanup    - Stop containers and remove SSH keys / generated configs
test-ssh   - Test SSH connectivity to the container
test-mysql - Test MySQL connectivity to the container
```

## Troubleshooting

### SSH connection refused

```bash
# Test connectivity
./setup_environment.sh test-ssh

# Manual test
ssh -i ./ssh_keys/id_rsa -o StrictHostKeyChecking=no -p 2222 root@localhost

# Restart SSH container
./setup_environment.sh cleanup
./setup_environment.sh ssh
```

### MinIO not responding

```bash
# Check container status
docker ps -a --filter name=transx-inspect-minio

# Restart MinIO
./setup_environment.sh cleanup
./setup_environment.sh minio
```

### MySQL connection refused

MySQL takes a few seconds to initialise on first start; `setup_environment.sh`
waits up to 120s for it.

```bash
# Test connectivity
./setup_environment.sh test-mysql

# Manual test
docker exec -it transx-inspect-mysql mysql -uroot -pinspectpass inspect_test

# Restart MySQL
./setup_environment.sh cleanup
./setup_environment.sh mysql
```

If port 3308 is already taken, override `MYSQL_PORT` in `setup_environment.sh`
and regenerate the configs.

### Permission denied on SSH key

```bash
chmod 600 ./ssh_keys/id_rsa
```

## Cleanup

```bash
./setup_environment.sh cleanup
```

This will:
- Stop and remove `transx-inspect-ssh`, `transx-inspect-minio` and `transx-inspect-mysql` containers
- Remove generated SSH keys (`./ssh_keys/`)
- Remove generated config files (`config-fs.json`, `config-minio.json`, `config-mysql.json`, `config-fs-metric.json`, `config-minio-metric.json`, `config-mysql-metric.json`)
