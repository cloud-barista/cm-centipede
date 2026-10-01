# DB Version Matrix Migration

Runs a migration for **every combination of a source version and a target version**
of a database engine, and reports the outcome as a matrix.

Each engine has its own script. A script starts a fresh pair of containers from the
official images for one cell, seeds the source, migrates it with `transx-ex`, checks
that the target matches, throws the containers away, and moves to the next cell.

```
mysql-ver-matrix-migration.sh        8.0 · 8.4
mariadb-ver-matrix-migration.sh      10.6 · 10.11 · 11.4 · 11.8
postgresql-ver-matrix-migration.sh   13 · 14 · 15 · 16 · 17
mongodb-ver-matrix-migration.sh      6.0 · 7.0 · 8.0
```

## Prerequisites

- Go 1.26 or higher
- Docker installed and running
- `jq`
- Internet access — the official images are pulled on first use

> **No database client needed on the host, and no server either.** Seeding and
> verification run inside the containers through `docker exec`, and the migration
> calls `transx-ex` as a library — neither cm-centipede nor cm-honeybee is involved.

---

## Overview

```
Host Machine
┌────────────────────────────────────────────────────────────────────────┐
│                                                                        │
│   ┌──────────────────────┐            ┌──────────────────────┐         │
│   │ transxex-vermatrix-  │            │ transxex-vermatrix-  │         │
│   │   <engine>-src       │            │   <engine>-dst       │         │
│   │ official image :SRC  │            │ official image :DST  │         │
│   │ db: matrix_db        │            │ db: matrix_db (empty)│         │
│   └──────────┬───────────┘            └──────────▲───────────┘         │
│              │                                   │                     │
│              │    runner/runner                  │                     │
│              │    └─ transxex.MigrateDBMS() ─────┘                     │
│              │                                                         │
│   <engine>-ver-matrix-migration.sh                                     │
│     seeds the source, then compares snapshots of both sides            │
└────────────────────────────────────────────────────────────────────────┘
```

`runner/` holds a single Go program shared by all four scripts. It reads a
`DBMSMigrationModel` from JSON, runs the migration, and writes a small result file
the script turns into one cell. Each script keeps its own config and result under
`.run/<engine>/`, so two engines can run at the same time.

---

## How one cell works

| Step | What happens |
|------|--------------|
| 1 | Pull the official image. In `ssh` mode, build an sshd-derived image once per version. |
| 2 | Start fresh source and target containers and wait until the DB accepts queries. |
| 3 | Create both databases with the character set from the env file, then seed the source. (MongoDB creates a database on first write, so there is nothing to create.) |
| 4 | Write `config.json` and run the runner → `transxex.MigrateDBMS`. |
| 5 | Snapshot both sides and compare — see [Verification](#verification). |
| 6 | Remove the containers and record the verdict. |

The matrix always migrates a whole database; there is no scope option.

## Verdicts

| Verdict | Meaning |
|---------|---------|
| `PASS` | The migration succeeded and the source/target snapshots agree. |
| `BLOCK` | A downgrade `transx-ex` refuses up front with `VersionDowngradeError`. **This is the expected result**, not a failure — pass `--skip-version-check` to attempt the transfer anyway. |
| `FAIL` | Anything else: the migration failed, or the snapshots disagree. |
| `SKIP` | A cell excluded by `--only` / `ONLY_CELLS`. |

The script exits non-zero when any cell is `FAIL`.

---

## Configuration

Copy the template and edit it:

```bash
cp db-ver-matrix.env.example db-ver-matrix.env
```

Precedence, strongest first:

```
CLI option  >  real shell variable  >  db-ver-matrix.env  >  script default
```

So `SKIP_VERSION_CHECK=1 ./mysql-ver-matrix-migration.sh` wins over the env file.

| Variable | Default | Meaning |
|----------|---------|---------|
| `MODE` | `direct` | Access mode for both sides: `direct` or `ssh` |
| `SRC_MODE` / `DST_MODE` | *(empty)* | Per-side access mode; empty falls back to `MODE` |
| `HOST_IP` | `127.0.0.1` | Address used to reach the published container ports |
| `DB_ROOT_PASS` | `testpass123` | Password of the image's admin account |
| `SRC_DB` / `DST_DB` | `matrix_db` | Name of the database being migrated |
| `<ENGINE>_SRC_VERSIONS` | per engine | Matrix rows |
| `<ENGINE>_DST_VERSIONS` | per engine | Matrix columns |
| `SKIP_VERSION_CHECK` | `0` | `1` attempts a downgrade instead of blocking it |
| `ASYNC` | `0` | `1` uses `MigrateDBMSAsync` and polls progress |
| `ROLLBACK_ON_FAILURE` | `1` | Undo objects created on the target when a migration fails |
| `ONLY_CELLS` | *(empty)* | Run only these cells, e.g. `"8.0:8.4 8.4:8.4"` |
| `STOP_ON_FAIL` | `0` | `1` stops at the first `FAIL` |
| `KEEP_ON_FAIL` | `0` | `1` keeps the containers of a failed cell |
| `READY_TIMEOUT` | `180` | Seconds to wait for a container DB to come up |
| `NO_PAUSE` | `1` | `0` waits for a key between cells |
| `NO_LOG` | `0` | `1` writes no log file |

### Character sets

Databases are created by the script with `CREATE DATABASE`, not by the image init
variables, so the character set is configurable. Tables name no character set of
their own and inherit the database default.

| Engine | Source variables | Target variables |
|--------|------------------|------------------|
| MySQL / MariaDB | `*_SRC_CHARSET`, `*_SRC_COLLATION` | `*_DST_CHARSET`, `*_DST_COLLATION` |
| PostgreSQL | `POSTGRESQL_SRC_ENCODING`, `POSTGRESQL_SRC_LOCALE` | `POSTGRESQL_DST_*` |
| MongoDB | `MONGODB_SRC_COLLATION_LOCALE` | `MONGODB_DST_COLLATION_LOCALE` |

- An empty target value falls back to the source value, so the target is built to
  match the source, and the snapshots are compared.
- Setting the two sides differently is treated as intentional: the difference is
  printed but not counted as a mismatch.
- An empty value omits the clause and takes the server default.
- A collation the version does not know makes the cell `FAIL` at database creation —
  it is never silently replaced with the server default.

### Ports

The scripts publish container ports in the `45xxx` band. Override any of them
through the env file when a port is already taken.

| Engine | src DB | dst DB | src SSH | dst SSH |
|--------|--------|--------|---------|---------|
| MySQL | 45406 | 45407 | 45240 | 45241 |
| MariaDB | 45306 | 45307 | 45230 | 45231 |
| PostgreSQL | 45432 | 45433 | 45250 | 45251 |
| MongoDB | 45017 | 45018 | 45260 | 45261 |

---

## Access modes

| Mode | How `transx-ex` reaches the database |
|------|--------------------------------------|
| `direct` | A pure Go driver connects to the published container port. Nothing to install. |
| `ssh` | The script builds an sshd-derived image, installs a generated key pair, and `transx-ex` runs the dump tools (`mysqldump`, `mariadb-dump`, `pg_dump`, `mongodump`) inside the container over SSH. The derived image is built once per version. |

The two sides are independent:

```bash
./mysql-ver-matrix-migration.sh --mode ssh                     # both sides over SSH
./mysql-ver-matrix-migration.sh --src-mode ssh --dst-mode direct   # mixed
```

With both sides on `ssh` the dump is piped straight from the source tool to the
target tool. Every other combination, mixed included, stages through a local file.

> **MongoDB cannot mix the two.** `direct` produces JSON and `ssh` produces a
> `mongodump` archive, and neither can restore the other. The script refuses a mixed
> pair during preflight, as `transx-ex` does in `Validate`.

---

## Seed objects

The source is seeded with a small fixed data set that exercises schema objects as
well as rows, including Korean, Japanese and emoji values that make an encoding
problem visible.

| Engine | Objects |
|--------|---------|
| MySQL / MariaDB | 4 tables (3 FKs) · 1 view · 1 function · 1 procedure · 1 trigger · 1 event |
| PostgreSQL | 4 tables (3 FKs) · 1 view · 2 functions · 1 procedure · 1 trigger · 4 sequences |
| MongoDB | 4 collections · 1 view · 2 indexes · 1 validator |

---

## Verification

A cell is judged by reducing each side to three normalised lines and comparing them
as plain strings. The first line that differs becomes the cell's failure reason.
Both snapshots are printed in the log, so a mismatch shows the two side by side.

| Line | Built by | What it catches |
|------|----------|-----------------|
| `tables=4 views=1 funcs=1 procs=1 triggers=1 events=1 fks=3` | `snapshot_schema` | a schema object that did not survive |
| `rows=5/5/5/8 utf8=… sum=3369.88` | `snapshot_data` | missing rows, mangled characters, changed values |
| `db=utf8mb4/utf8mb4_general_ci tables=customers:…` | `snapshot_charset` | a character set the target did not reproduce |

### The data line

```
rows=5/5/5/8 utf8=김철수 (한글)|노트북 거치대 🖥 sum=3369.88
```

| Field | Where it comes from | Why it is there |
|-------|---------------------|-----------------|
| `rows=5/5/5/8` | `COUNT(*)` of `customers` / `products` / `orders` / `order_items`, always in that order | Rows lost or duplicated. The order is fixed, so the position of the slash names the table that drifted. |
| `utf8=김철수 (한글)\|노트북 거치대 🖥` | `customers.name` where `id = 4`, then `products.name` where `id = 5`, joined by `\|` | Encoding damage. These are the two seeded rows carrying non-ASCII text; a bad dump or restore turns them into `???` or `ê¹€ì² ìˆ˜` while the counts still look right. |
| `sum=3369.88` | `SUM(qty * unit_price)` over `order_items` | Values changed or precision lost. Counts alone would not notice a `DECIMAL(10,2)` column restored as a float. |

The checksum is just the seed's own arithmetic, so it is the same in every cell:

```
1×1299.99 + 2×29.99 + 1×49.99 + 1×399.99
          + 2×39.99 + 1×29.99 + 1×1299.99 + 3×49.99 = 3369.88
```

Engines differ in the details — MongoDB counts documents per collection in name
order and sums with `$multiply`, PostgreSQL fixes the decimals with `to_char` — but
the three lines mean the same thing everywhere.

---

## Usage

```bash
# Whole matrix, direct access
./mysql-ver-matrix-migration.sh

# A narrower matrix
./mysql-ver-matrix-migration.sh --src-versions "8.0" --dst-versions "8.0 8.4"

# One cell, attempting the downgrade instead of blocking it
./mysql-ver-matrix-migration.sh --only 8.4:8.0 --skip-version-check

# Over SSH, keeping the containers of any failed cell
./postgresql-ver-matrix-migration.sh --mode ssh --keep-on-fail

# Environment variables work too
MODE=ssh ./mariadb-ver-matrix-migration.sh
SRC_MODE=ssh DST_MODE=direct ./mysql-ver-matrix-migration.sh
```

Options:

```
--src-versions "V1 V2 ..."   source version list (matrix rows)
--dst-versions "V1 V2 ..."   target version list (matrix columns)
--only "SRC:DST ..."         run only these cells
--mode direct|ssh            access mode for both sides
--src-mode direct|ssh        access mode for the source only
--dst-mode direct|ssh        access mode for the target only
--skip-version-check         attempt the transfer even for a downgrade
--async                      run asynchronously and poll progress
--keep-on-fail               keep the containers of a failed cell
--stop-on-fail               stop at the first FAIL
-h, --help                   help
```

---

## Output

```
logs/<script name>.log           the whole screen output, colours stripped
logs/<script name>-result.json   the per-cell result matrix
```

The matrix table is printed at the end:

```
  src \ dst   | 8.0         8.4
  ------------+------------------------
  8.0         | PASS 42s    PASS 45s
  8.4         | BLOCK 3s    PASS 44s

  PASS 3 / BLOCKED(expected) 1 / FAIL 0 / SKIP 0   4 cells   took 2m14s
```

`logs/<script name>-result.json`:

```json
{
  "engine": "mysql",
  "srcMode": "direct",
  "dstMode": "direct",
  "skipVersionCheck": false,
  "finishedAt": "2026-09-03 15:04:05",
  "cells": [
    {
      "srcVersion": "8.0",
      "dstVersion": "8.4",
      "status": "PASS",
      "elapsedSec": 45,
      "srcServerVersion": "8.0.43",
      "dstServerVersion": "8.4.6",
      "detail": "tables=4 views=1 funcs=1 procs=1 triggers=1 events=1 fks=3  rows=5/5/5/8 ..."
    }
  ]
}
```
