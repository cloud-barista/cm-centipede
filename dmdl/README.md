# DMDL - Data Migration Models

## Overview

The **data migration models** for cm-centipede: the source-side description of
what exists in a source environment, the target-side migration plan derived from
it, and the property types both sides share.

They live in their own module so that the models carry no REST/GORM/echo baggage
and other Cloud-Barista subsystems can import them without pulling in the whole
server. The naming follows the sibling model modules `cm-beetle/imdl`
(infrastructure) and `cm-grasshopper/smdl` (software).

## Directory Structure

```
dmdl/
├── go.mod                    # Module definition (github.com/cloud-barista/cm-centipede/dmdl)
├── README.md                 # This file
├── common-model/             # Shared property types (source + target)
│   ├── model.go              # DBMSType, transfer strategy, storage type discriminators
│   ├── connection.go         # ConnectionSource, the 4 reference types, the 3 inline configs, ConnectionRef
│   └── filter.go             # PathFilterRule, DBMSFilterRule, DBMSFilterEntry, TargetMapping, PgSchemaMapping, per-resource filters
├── source-model/             # Source-side models (as observed)
│   ├── filesystem.go         # FSEntry, FSInfo, SourceFileSystemModel
│   ├── objectstorage.go      # ObjectEntry, ObjectStorageInfo, SourceObjectStorageModel
│   ├── dbms.go               # DBColumnInfo, DBIndexInfo, DBTableInfo, DBPgSchemaInfo, DBMSInfo, SourceDBModel
│   └── model.go              # SourceDataMigrationProperty, SourceDataMigrationModel
└── target-model/             # Target-side models (migration plan to execute)
    ├── filesystem.go         # FSMigrationInfo, MigrationFileSystemModel
    ├── objectstorage.go      # ObjectMigrationInfo, MigrationObjectStorageModel
    ├── dbms.go               # DBMigrationInfo, MigrationDBModel
    └── model.go              # TargetDataMigrationProperty, TargetDataMigrationModel
```

Dependency direction is one-way: `sourcemodel → commonmodel` and
`targetmodel → commonmodel`. Source and target never import each other.

## Model Categories

### 1. Common Models (`common-model/`)

**Purpose**: Property types used by both the source and the target side.

**Key Types**:

- `ConnectionRef`: Discriminated union over 7 connection sources — reference
  types (`honeybee`, `beetleSsh`, `beetleObjectStorage`, `beetleDb`) and inline
  types (`ssh`, `minio`, `db`). Exactly one sub-field matching `Source` must be
  non-nil. Inline credentials are AES-256-GCM encrypted (`enc:` prefix) by the
  server before storage.
- `SSHConnConfig`, `MinioConnConfig`, `DBConnConfig`: Inline credential shapes
  every connection source converges on before being handed to transx / dbmsx.
- `PathFilterRule`: One ordered include/exclude entry of the transx-ex filter
  pipeline (`glob` or `size`).
- `DBMSFilterRule`: One entry of the dbmsx exclude-only pipeline
  (`table_column`, `row_exclude`, `object_exclude`).
- `TargetMapping`, `FileSystemFilter`, `ObjectStorageFilter`, `DBMSFilter`,
  `DBMSFilterEntry`: Per-resource filter inputs shared by the plan request and
  the plan output.
- `PgSchemaMapping`: One PostgreSQL schema selected, and optionally renamed,
  from source to target. Carried by `TargetMapping.PgSchemas`.
- `DBMSType`, `Strategy*`, `StorageType*`: Enumerations.

### 2. Source Models (`source-model/`)

**Purpose**: Describe data resources as observed in a source environment.

**Key Types**:

- `SourceDataMigrationModel`: Top-level wrapper, the input to
  `POST /centipede/plans/target`
- `SourceFileSystemModel`: Connection + `FSInfo` (aggregates + folder listing)
- `SourceObjectStorageModel`: Connection + `ObjectStorageInfo` (bucket/prefix inventory)
- `SourceDBModel`: Connection + `DBMSInfo` (tables, columns, indexes, schema
  object inventory)

**Single Definition**: These types are shared by both sides of the exchange —
the subsystem that surveys a source builds them, and the plan layer that derives
a migration reads them — so a source description is defined once rather than
copied and kept in step by hand. A producer fills the one `ConnectionRef`
sub-field matching the connection it holds; the sources it cannot produce are
`omitempty` and never appear in its payloads. The discovery/inspect shapes track
dbmsx and transx-ex.

### 3. Target Models (`target-model/`)

**Purpose**: Describe the executable migration plan derived from a source model.

**Key Types**:

- `TargetDataMigrationModel`: Top-level wrapper, produced by
  `POST /centipede/plans/target`, stored in `Migration.Plan` and consumed by the
  migration executor
- `MigrationFileSystemModel`: SSH→SSH by default, or filesystem→objectstorage
  cross-storage when `DstType` is `objectstorage`
- `MigrationObjectStorageModel`: S3→S3 by default, or objectstorage→filesystem
  cross-storage when `DstType` is `filesystem`
- `MigrationDBModel`: src/dst connection pair, engine, and the `DBMigrationInfo`
  list of databases to migrate
- `DBMigrationInfo`: one database migration task — source and target database
  name, an optional exclude-only filter, and the `PgSchemas` selection that
  picks and renames PostgreSQL schemas within it

## Version Management and Tagging

This module is independently versioned using Git tags with the `dmdl/` prefix
(e.g. `dmdl/v0.1.0`), following the same convention as `cm-beetle/imdl` and
`cm-grasshopper/smdl`.

### Releasing a new version

After your changes have been merged into `upstream/main`, create and push the version tag:

```bash
# 1) Fetch latest upstream and verify the merge
git fetch upstream
git log upstream/main --oneline -5

# 2) Check recent dmdl tags to determine the next version
git tag -l "dmdl/*" --sort=-v:refname | head -5

# 3) Set the next version manually, based on the list above
export NEXT_DMDL_TAG=  # e.g. export NEXT_DMDL_TAG="dmdl/v0.1.0"
echo "Next dmdl tag: $NEXT_DMDL_TAG"

# 4) Tag the merge result on upstream/main
git tag -a $NEXT_DMDL_TAG upstream/main -m "dmdl: release ${NEXT_DMDL_TAG#dmdl/}"
# Safer if upstream/main has moved:
# git tag -a $NEXT_DMDL_TAG <merge_commit_sha> -m "dmdl: release ${NEXT_DMDL_TAG#dmdl/}"

# 5) Push the tag
git push upstream $NEXT_DMDL_TAG

# 6) Verify
git show $NEXT_DMDL_TAG
```

> **Note:** Tag `upstream/main` (the merge result), not your old branch commit. If another PR lands before you tag, use the exact merge commit SHA instead of `upstream/main`.

### Updating the dependency in a consumer

```bash
# 1) Start a new branch from the latest upstream/main
git fetch upstream
git checkout upstream/main -b update-dmdl-${NEXT_DMDL_TAG#dmdl/}

# 2) Update the dependency
go get -u github.com/cloud-barista/cm-centipede/dmdl
# Or pin the exact version:
# go get github.com/cloud-barista/cm-centipede/dmdl@${NEXT_DMDL_TAG#dmdl/}
go mod tidy

# 3) Verify, test, commit, push, open a PR
```
