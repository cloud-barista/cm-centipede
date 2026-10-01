# transx-ex

A Go library for migrating data between clouds — filesystems, S3-compatible object storage, and the MySQL, MariaDB, PostgreSQL and MongoDB databases — behind one API.

## Features

- **One import, one API** — `transxex` re-exports everything; the implementation lives in subpackages you rarely need to touch
- **Three source kinds** — local/remote filesystems, object storage (MinIO, Azure Blob, CB-Spider, CB-Tumblebug), and four database engines
- **Two access modes per domain** — SSH/rsync or direct SDK for storage; direct TCP or SSH tunnel for databases
- **Database TLS** — five modes from plaintext to full verification, spelled the same way on every engine, with a CA for roots the system trust store does not carry
- **Planner-driven** — every migration resolves to an inspectable pipeline of steps before anything runs
- **Async with progress** — poll status, bytes and per-file/per-table counters; cancel mid-flight
- **Inspection** — scan a path, a bucket prefix or a schema without transferring anything
- **PostgreSQL schemas** — migrate a whole database or pick schemas out of it, and rename them on the way
- **Character sets** — reported by inspection at every level, reproduced faithfully in the dump, and checked against the target before a migration reads anything
- **Filtering** — an ordered include/exclude pipeline for files and objects; exclusion rules for tables, rows and schema objects
- **Structured errors** — every failure carries the stage, the endpoint, the command and its output
- **Field encryption** — hybrid AES-256-GCM + RSA-OAEP for credentials in storage migration models

## Installation

```bash
go get github.com/cloud-barista/cm-centipede/transx-ex
```

## Quick Start

```go
import "github.com/cloud-barista/cm-centipede/transx-ex"   // package transxex
```

### Filesystem or object storage

```go
model := transxex.StorageMigrationModel{
    Source: transxex.StorageLocation{
        StorageType: transxex.StorageTypeFilesystem,
        Path:        "/data",
        Filesystem: &transxex.FilesystemAccess{
            AccessType: transxex.AccessTypeSSH,
            SSH:        &transxex.SSHConfig{Host: "10.0.0.1", Username: "ubuntu", PrivateKey: key},
        },
    },
    Destination: transxex.StorageLocation{
        StorageType: transxex.StorageTypeObjectStorage,
        Path:        "my-bucket/backup",
        ObjectStorage: &transxex.ObjectStorageAccess{
            AccessType: transxex.AccessTypeMinio,
            Minio: &transxex.S3MinioConfig{
                Endpoint: "s3.example.com", AccessKeyId: id, SecretAccessKey: secret,
            },
        },
    },
}

if err := transxex.MigrateStorage(model); err != nil {
    log.Error().Err(err).Msg("migration failed")
}
```

### Database

```go
model := transxex.DBMSMigrationModel{
    Source: transxex.DBMSLocation{
        DBMSType:   transxex.DBMSTypeMySQL,
        Database:   "appdb",
        AccessType: transxex.AccessTypeDirect,
        Direct:     &transxex.DirectConfig{Host: "10.0.0.1", Username: "root", Password: pw},
    },
    Destination: transxex.DBMSLocation{
        DBMSType:   transxex.DBMSTypeMySQL,   // both ends must run the same engine
        Database:   "appdb",
        AccessType: transxex.AccessTypeSSHTunnel,
        SSHTunnel: &transxex.SSHTunnelConfig{
            SSH: &transxex.SSHConfig{Host: "10.0.0.2", Username: "ubuntu", PrivateKey: key},
        },
    },
    Scope:             transxex.ScopeFull,
    RollbackOnFailure: true,
}

if err := transxex.MigrateDBMS(model); err != nil {
    log.Error().Err(err).Msg("migration failed")
}
```

### Asynchronous with progress

```go
h := transxex.MigrateDBMSAsync(model)

for h.Status() != transxex.StatusDone && h.Status() != transxex.StatusFailed {
    p := h.Progress()
    fmt.Printf("%s — %d/%d tables, %.1fs\n", p.Status, p.TablesDone, p.TablesTotal, p.ElapsedSec)
    time.Sleep(time.Second)
}
if err := h.Wait(); err != nil {
    log.Error().Err(err).Msg("migration failed")
}

// h.Cancel() stops it; Wait then returns context.Canceled.
```

## Public API

Operations that exist in both domains carry the domain in their name; unambiguous ones do not.

|                    | Storage                                                | Database                                              |
| ------------------ | ------------------------------------------------------ | ----------------------------------------------------- |
| **Migrate**        | `MigrateStorage` · `MigrateStorageAsync`                | `MigrateDBMS` · `MigrateDBMSAsync`                    |
| **Single op**      | —                                                       | `DumpDBMS` · `RestoreDBMS` · `RollbackDropDBMS`       |
| **Database DDL**   | —                                                       | `CreateDatabase` · `DropDatabase`                     |
| **Inspect**        | `InspectFilesystem` · `InspectObjectStorage`            | `InspectDBMS` · `InspectDBMSAll` · `ListDatabases` · `ListSchemas` · `DescribeTable` · `ServerVersion` |
| **Plan / Validate**| `PlanStorage` · `ValidateStorage`                       | `PlanDBMS` · `ValidateDBMS` · `CheckCharset` · `CheckVersion` |
| **Encryption**     | `EncryptStorageModel` · `DecryptStorageModel`           | —                                                     |
| **Model**          | `StorageMigrationModel` · `StorageLocation`             | `DBMSMigrationModel` · `DBMSLocation`                 |
| **Handle**         | `StorageHandle` · `StorageProgress`                     | `DBMSHandle` · `DBMSProgress`                         |
| **Filter**         | `PathFilterOption`                                      | `DBMSFilterOption`                                    |
| **Shared**         | `SSHConfig` · `MigrationStatus` · `MigrationError` · `OperationError` |                                    |

A storage migration runs `Source.PreCmd` (if set), the transfer, then `Destination.PostCmd` (if set); leave both empty to transfer only. Object storage locations accept neither command — validation refuses them before anything runs.

## Layout

```
transx-ex/            package transxex — the public API (re-export only, no logic)
├── core/             SSH, pipeline, handle, errors, staging — shared by both domains
├── storagex/         filesystem + object storage
│   ├── filter/       glob and size matchers (include/exclude pipeline)
│   └── fieldsec/     hybrid field encryption
└── dbmsx/            MySQL, MariaDB, PostgreSQL, MongoDB
    ├── driver/       per-engine implementations over a shared base
    ├── executor/     dump, restore and direct-pipe executors
    └── filter/       table, row and schema-object exclusion
```

Import the root package. Reach into a subpackage only for something the root does not expose — registering a custom filter matcher with `dbmsx/filter.RegisterFilter`, for instance.

## Architecture

Both domains share one shape:

```
Model → Validate() → Plan() → Pipeline{Steps} → Executor.Execute(ctx, src, dst)
```

`PlanStorage`/`PlanDBMS` return that pipeline without running it, so a caller can inspect the steps a migration would take.

### Storage routing

| Source → Destination            | Pipeline                   | How                                  |
| ------------------------------- | -------------------------- | ------------------------------------ |
| filesystem ↔ filesystem         | `filesystem-transfer`      | rsync pull, push, or agent-forward   |
| objectstorage ↔ objectstorage   | `objectstorage-transfer`   | download → local staging → upload    |
| filesystem ↔ objectstorage      | `cross-storage-transfer`   | rsync and S3 combined                |

Object storage access type `minio` covers every S3-compatible service and Azure
Blob Storage alike. Azure does not speak the S3 API, so `NewS3Provider` routes it
to an `azblob`-backed provider whenever the endpoint carries a Blob service
suffix (`blob.core.windows.net` and its China/US-Gov counterparts). The config
block is unchanged — `accessKeyId` is read as the storage account name and
`secretAccessKey` as the account key.

### Database routing

| Source / Destination access     | Pipeline           | How                                       |
| ------------------------------- | ------------------ | ----------------------------------------- |
| ssh-tunnel → ssh-tunnel         | `direct-pipe`      | dump stdout piped straight into restore   |
| anything else                   | `dbms-transfer`    | dump to a staging file, then restore      |

Both ends must run the same engine. A dump is the source engine's own dialect and the restore hands it over verbatim, so a cross-engine pair would fail somewhere mid-transfer; `ValidateDBMS` refuses it up front instead.

Staging goes under `/tmp/transxex-staging`, in a `storage/` or `dbms/` subdirectory so concurrent migrations never share a directory.

`direct-pipe` streams `pg_dump` straight into `psql`, so the schema names are already in the bytes by the time they leave the source host. Selecting schemas works there, but renaming them does not — see [PostgreSQL Schemas](#postgresql-schemas).

## Error Handling

Errors carry context rather than just a message.

```go
// Which stage failed?
var me *transxex.MigrationError
if errors.As(err, &me) {
    log.Error().Str("stage", me.Stage).Msg("migration failed")   // backup|transfer|dump|restore
}

// What exactly failed, and what did the command say?
var oe *transxex.OperationError
if errors.As(err, &oe) {
    log.Error().
        Str("operation", oe.Operation).
        Str("source", oe.Source).
        Str("command", oe.Command).
        Str("output", oe.Output).      // remote stderr, when the command produced any
        Msg("operation failed")
}
```

Typed conditions worth handling on their own: `TargetNotEmptyError`, `TargetDatabaseNotFoundError`, `TargetDatabaseExistsError`, `SystemDatabaseError`, `InvalidDatabaseNameError`, `SchemaNotFoundError`, `CharsetIncompatibleError`, `VersionDowngradeError`, `UnsupportedDBMSError`, `UnsupportedTransferError`.

`DBMSInspectError` is the exception to the shape above: `InspectDBMSAll` collects it per skipped database rather than returning it, and it marshals to JSON (`database`, `error`) so the skipped list can be served as-is. The wrapped cause stays reachable through `errors.As` / `errors.Is`.

## Database TLS

`DirectConfig.TLSMode` decides how a direct connection is secured. The five modes mean the same thing on every engine; two questions separate them.

| Mode | Encrypt | Verify chain | Verify hostname | Server offers no TLS |
| ---- | :-----: | :----------: | :-------------: | -------------------- |
| `disable` (default) | no | — | — | plaintext |
| `prefer` | try | no | no | plaintext |
| `require` | yes | no | no | error |
| `verify-ca` | yes | yes | no | error |
| `verify-full` | yes | yes | yes | error |

Encryption and verification are separate things, and conflating them is the usual source of confusion. A server with `require_secure_transport=ON` demands only the first: it refuses a plaintext connection and does not care whether the client checked its certificate. Verification is what the client does to protect itself, and it is the half that needs a CA.

What each mode defends against:

- `require` — a passive listener, but not an active man in the middle: nothing proves the peer is the server it claims to be
- `prefer` — the same, and less. An attacker who strips the server's TLS capability flag from the unauthenticated handshake makes the client fall back to plaintext, and the caller cannot tell it happened
- `verify-ca` — a man in the middle without the CA's signature, while still accepting any host that CA vouches for
- `verify-full` — both

```go
loc.Direct = &transxex.DirectConfig{
    Host:     "shop.abcdef.ap-northeast-2.rds.amazonaws.com",
    Username: "dbadmin", Password: pw,
    TLSMode:  transxex.TLSModeVerifyFull,
    TLSCAFile: "/etc/ssl/rds-bundle.pem",   // or TLSCAPEM for the content itself
}
```

`TLSCAFile` and `TLSCAPEM` supply the root the verifying modes check against — give one or neither, never both. Empty means the system trust store. A CA on a mode that does not verify is rejected rather than ignored, so a config cannot look like it is checking something it is not.

### Per-engine spelling

| Mode | MySQL / MariaDB | PostgreSQL | MongoDB |
| ---- | --------------- | ---------- | ------- |
| `disable` | `tls=false` | `sslmode=disable` | no TLS |
| `prefer` | `tls=preferred` | `sslmode=prefer` | unsupported |
| `require` | `tls=skip-verify` | `sslmode=require` | `InsecureSkipVerify` |
| `verify-ca` | registered config | `sslmode=verify-ca` | `RootCAs`, no `ServerName` |
| `verify-full` | `tls=true`, or registered | `sslmode=verify-full` | `RootCAs` + `ServerName` |

The mode names are libpq's own, which is why PostgreSQL translates nothing and the others translate into it. Two consequences worth knowing:

- **MongoDB has no `prefer`.** A client either negotiates TLS or it does not, so there is no fallback to describe. It is refused rather than quietly promoted to `require`.
- **`verify-ca` has no counterpart in `crypto/tls`**, whose verifier always checks the hostname once it checks the chain. The MySQL-family and MongoDB drivers verify the chain themselves for that mode, with the hostname left out.

TLS applies to `direct` locations only. An `ssh-tunnel` location runs the engine's CLI tools on the remote host and is handed no TLS options — the connection it describes is already carried inside the SSH session — so `SSHTunnelConfig` has no such field.

## PostgreSQL Schemas

A PostgreSQL database holds schemas, and `DBMSLocation.PgSchema` decides which of them a migration covers. Leaving it empty covers every user schema — `pg_catalog`, `information_schema` and the `pg_*` internals are never included.

The field is PostgreSQL's alone, as the name says: MySQL and MariaDB use "schema" and "database" as synonyms, MongoDB has no equivalent, and setting it on those engines is rejected rather than ignored.

```go
schemas, err := transxex.ListSchemas(loc)   // what may go in PgSchema

src := loc
src.PgSchema = []string{"sales", "hr"}      // migrate two of them; omit for all
```

Naming a schema the database does not have is a `SchemaNotFoundError`, not an empty result — a typo would otherwise migrate nothing and report success.

### Renaming on the way

`Destination.PgSchema` is how a schema arrives under a different name. It is matched to the source list **by position**.

```go
model := transxex.DBMSMigrationModel{
    Source:      transxex.DBMSLocation{..., PgSchema: []string{"sales", "hr"}},
    Destination: transxex.DBMSLocation{..., PgSchema: []string{"sales_prod", "hr"}},
}
// sales → sales_prod, hr → hr   (a name you do not want changed is repeated as-is)
```

| `Destination.PgSchema` | Result |
| ---------------------- | ------ |
| empty | the source schema names are reproduced, however many there are |
| same length as the source list, source listed explicitly | renamed by position |
| one name, source not listed | renamed, provided the database holds exactly one schema |
| several names, source not listed | rejected — position would be assigned against a list the caller never saw |
| fewer names than source schemas | rejected — merging schemas would collide objects of the same name |
| more names than source schemas | rejected — nothing to rename |
| a repeated name | rejected, for the same reason as merging |

`ValidateDBMS` reports all of these before anything runs. A rename is also refused when both ends are `ssh-tunnel`: that route pipes `pg_dump` into `psql` with no point at which the names could be changed, so put one side on `direct` to stage the dump instead.

Renaming is applied to the schema itself once the objects are restored, so views, routines and constraints follow it — PostgreSQL tracks what they depend on by object identity, not by the SQL text that created them. Nothing in a dumped definition is ever rewritten.

### What inspection reports

`DBMSInfo.PgSchema` breaks the four size figures down per schema, and always sums back to the totals above it. A schema holding no tables is listed at zero rather than dropped, so a freshly created target reports its schemas too.

```jsonc
{
  "database": "shop",
  "totalSize": 5242880, "tableCount": 3,   // the sum over the schemas covered,
                                           // not pg_database_size()
  "pgSchema": [
    { "name": "hr",     "totalSize":       0, "tableCount": 0 },
    { "name": "public", "totalSize":  262144, "tableCount": 1 },
    { "name": "sales",  "totalSize": 4980736, "tableCount": 2 }
  ],
  "tables": [
    { "pgSchema": "sales", "name": "orders", "rowCount": 4800 }
  ],
  "views":      ["public.v_active_users", "sales.v_monthly"],
  "extensions": ["pgcrypto"]
}
```

`TableInfo.Name` stays the bare table name and the schema travels beside it in `PgSchema`, so code that keys on the name keeps working. The schema-object lists have no such field, so their entries are schema-qualified and quoted by the server — which also makes each one usable as a DROP target as it stands. `extensions` is the exception: an extension belongs to the database, so the list is neither qualified nor filtered by schema.

### Filter rules

A rule addresses its target by a bare name or by `schema.name`, and both forms work. A bare rule applies in every schema — it never promised to mean one of them — while a qualified rule singles out one of two same-named objects. This is the same spelling `Inspect` reports its schema-object lists in, so an entry can be copied from an inspection straight into a rule.

Which field carries the qualifier depends on the rule:

| Rule | Qualifier goes on | Example |
| --- | --- | --- |
| `table_column` | `table` | `{"type":"table_column","table":"sales.audit"}` |
| `row_exclude` | `table` | `{"type":"row_exclude","table":"sales.events","match":"id < 10"}` |
| `object_exclude` | `name` | `{"type":"object_exclude","kind":"view","name":"sales.v_legacy"}` |
| `object_exclude`, `kind: "index"` | `table` | `{"type":"object_exclude","kind":"index","table":"sales.orders","name":"idx_created"}` |

An index rule is the odd one because an index name is unique per table rather than per schema: the qualifier pins the table, and `name` stays bare.

```go
json.Unmarshal([]byte(`{"rules":[
    {"type":"table_column","table":"users","columns":["ssn"]},
    {"type":"table_column","table":"sales.audit"}
]}`), &f)
```

A qualified rule is matched against the schema its target actually lives in, so it applies whether the dump covers one schema or several. A rule naming a schema outside the dump is inert; it is logged at warn level rather than refused, since one filter config is often shared across migrations that touch different schemas.

## Database Management

`CreateDatabase` and `DropDatabase` are server-level calls, like `ListDatabases`: they connect through a database the engine always provides, since the one named by `loc.Database` does not exist yet — or is about to stop existing. Unlike `ListDatabases`, `loc.Database` is required: it names the target.

```go
loc.Database = "shop"

// Create. Options are per engine; nil means engine defaults.
err := transxex.CreateDatabase(loc, &transxex.CreateDatabaseOption{
    IfNotExists: true,   // an existing database is a no-op instead of TargetDatabaseExistsError
    MySQL: &transxex.MySQLCreateOption{CharacterSet: "utf8mb4", Collate: "utf8mb4_0900_ai_ci"},
})

// Drop. Unconditional by default — it removes the database and its contents.
err = transxex.DropDatabase(loc, &transxex.DropDatabaseOption{
    RequireEmpty: true,  // refuse a database that still holds objects
    IfExists:     true,  // a missing database is a no-op instead of an error
})
```

Both refuse a system database (`mysql`, `template1`, `admin`, …) and a name the engine cannot hold, before any connection is made.

Engine notes:

| Engine | Create | Drop |
| ------ | ------ | ---- |
| MySQL / MariaDB | `CharacterSet`, `Collate` | — |
| PostgreSQL | `Owner`, `Template`, `Encoding`, `LcCollate`, `LcCtype`, `Locale`, and on 15+ `LocaleProvider` / `IcuLocale`; every encoding or locale setting requires `Template` (usually `template0`) | `PostgreSQL.Force` terminates other sessions first — without it the server refuses a database in use |
| MongoDB | no `CREATE DATABASE` exists; the call succeeds without DDL, and materialises the database only when `MongoDB.Collection` is set — `MongoDB.Collation` then gives that collection its default collation | `dropDatabase` needs a dbAdmin-level privilege, unlike `RollbackDropDBMS` |

`DropDatabase` removes the database itself; `RollbackDropDBMS` empties one and leaves it in place.

## Inspection

Scan without transferring. `Metric` selects what is collected; everything off by default is cheap to skip.

```go
yes := true   // metric flags are *bool, so nil keeps the default and only a set field overrides it

res, err := transxex.InspectFilesystem(transxex.StorageLocation{
    StorageType: transxex.StorageTypeFilesystem,
    Path:        "/data",
    Filesystem:  &transxex.FilesystemAccess{AccessType: transxex.AccessTypeLocal},
    Filter:     &transxex.PathFilterOption{MaxDepth: 2},
    Metric: &transxex.StorageMetricOption{
        Filesystem: &transxex.FilesystemMetric{TotalSize: &yes, FileCount: &yes},
    },
})

info, err := transxex.InspectDBMS(loc)    // server version, sizes, character sets, tables, columns, indexes
names, err := transxex.ListDatabases(loc) // server-level; loc.Database not required
schemas, err := transxex.ListSchemas(loc) // PostgreSQL; empty for the other engines

// Whole server at once: every user database, sorted, loc.Metric applied to each.
// A database that fails on its own lands in skipped instead of aborting the call,
// so err is set only when nothing could be inspected at all.
infos, skipped, err := transxex.InspectDBMSAll(loc)
```

## Character Sets and Collations

A dump carries character set and collation names as literal text in the DDL it writes. The target either recognises a name or rejects the statement carrying it, so both parts below are about keeping those names true from one end to the other.

### What inspection reports

`InspectDBMS` fills them at three levels. The database defaults cost one catalog query and the table-level values ride along with the table listing, so both are always collected; the per-column values follow the `Columns` metric like the rest of that scan.

```jsonc
{
  "database": "shop",
  "characterSet": "utf8mb4",              // PostgreSQL reports its encoding here
  "collation": "utf8mb4_general_ci",      // PostgreSQL: datcollate
  "tables": [
    {
      "name": "orders",
      "characterSet": "utf8mb4",
      "collation": "utf8mb4_general_ci",
      "columns": [
        { "name": "note", "characterSet": "utf8mb4", "collation": "utf8mb4_bin" }
      ]
    }
  ]
}
```

Each engine fills what it actually records. PostgreSQL keeps the encoding on the database, has no table-level collation, and reports a column's collation only when that column declares one — an inherited default is already the database's, above. MongoDB stores UTF-8 and nothing else, so it leaves the character sets empty and reports a collection's default collation locale (`"ko"`) as its `collation`.

### The check before a migration

Once the target is known to be empty and before any data is read, a migration profiles both endpoints and refuses to go on if the target cannot accept what the dump will carry:

- a collation or character set the target server does not recognise — the restore would fail on the first object using it
- on PostgreSQL, a target encoding narrower than the source's — the encoding lives on the database, so no dump can carry it and the target's own setting decides

A difference that leaves the migration able to run is logged instead of enforced: a differing database default collation changes how objects created without one of their own compare strings, and that is a call for the operator to make.

The check asks the server which names it knows rather than inferring it from a version. On PostgreSQL that is the only workable question — a libc collation exists only where the host has that locale installed, so the same release answers differently on a full distribution and on a slim container.

```go
// Ask ahead of time, while a migration is still being planned.
issues, err := transxex.CheckCharset(src, dst)
for _, i := range issues {
    fmt.Printf("%s [%s] %s → %s: %s\n", i.Severity, i.Scope, i.Source, i.Target, i.Detail)
}

// Or let the migration do it, and skip it if it is wrong about a particular pair.
model.SkipCharsetCheck = true
```

A blocking difference surfaces as `CharsetIncompatibleError`, carrying every blocker rather than the first. `ScopeDataOnly` skips the check: it emits no DDL, so there is no name for the target to reject.

## Server Version

A migration may go to the same server version or a newer one, never an older one. A dump is written in the source server's dialect, so anything the source added since the target's release reaches the restore as a syntax error — and only once the restore begins, after the whole dump has been taken. Two queries before the dump replace that.

```go
// One endpoint, as the server states it: "8.0.32", "10.6.14-MariaDB-1:10.6.14",
// "14.23 (Ubuntu 14.23-1.pgdg22.04+1)", "7.0.5".
version, err := transxex.ServerVersion(loc)

// Both endpoints, with the verdict.
res, err := transxex.CheckVersion(src, dst)
if res.Comparable && res.Downgrade {
    fmt.Printf("target %s is older than source %s\n", res.Target, res.Source)
}

// Or let the migration do it, and skip it to migrate to an older server anyway.
model.SkipVersionCheck = true
```

A downgrade surfaces as `VersionDowngradeError`. **A patch-level difference never blocks.** Releases are `MAJOR.MINOR.PATCH` everywhere but PostgreSQL, which from 10 onwards numbers them `MAJOR.PATCH` — 14.2 and 14.5 are one feature set, months apart — so only the major counts there; 9.x and earlier used `MAJOR.MAJOR.PATCH`, where the first two do again. `ParseServerVersion` and `CompareServerVersions` expose that rule for a caller that wants to apply it itself.

A version that cannot be read, or that carries no leading number, is logged and waived rather than blocking — a version this library cannot parse is not evidence of anything. `ServerVersion` is a server-level call and `loc.Database` need not exist, so a target can be checked before its first migration has created anything.

## Filtering

**Storage** — an ordered pipeline; the first matching rule decides, and an item no rule matches is kept.

```go
// Simple form: glob patterns, evaluated exclude-first.
loc.Filter = &transxex.PathFilterOption{
    Exclude:  []string{"*.tmp", "cache/*"},
    MaxDepth: 3,
}

// Full form: rules are decoded from JSON through a matcher registry, so a
// server can pass user-defined filters straight through.
var opt transxex.PathFilterOption
json.Unmarshal([]byte(`{"rules":[
    {"action":"exclude","type":"glob","pattern":"*.tmp"},
    {"action":"exclude","type":"size","min":1073741824}
]}`), &opt)
```

**Database** — exclusion only; the source is migrated in full minus what the rules remove. Column, row and schema-object exclusion require `direct` access, because a native CLI dump cannot express them.

```go
var f transxex.DBMSFilterOption
json.Unmarshal([]byte(`{"rules":[
    {"type":"table_column","table":"users","columns":["ssn"]},
    {"type":"row_exclude","table":"events","match":"created_at < '2024-01-01'"}
]}`), &f)
loc.Filter = &f
```

Both registries accept new matcher types — `filter.Register` for storage, `filter.RegisterFilter` for databases — so rules extend without changing the library.

## Sensitive Data Encryption

> [!NOTE]
> Field encryption is a minimal option for callers who need it, not a general secure-transport layer. It covers **storage migration models only** — `DBMSMigrationModel` credentials are not encrypted at this layer.
>
> This is about a model in transit between a client and a server, not about the connections a migration then makes. For the latter, see [Database TLS](#database-tls).

Fields encrypted by `EncryptStorageModel`:

| Field path                                      | Description                   |
| ----------------------------------------------- | ----------------------------- |
| `*.filesystem.ssh.privateKey`                   | SSH private key content       |
| `*.objectStorage.minio.accessKeyId`             | S3 access key ID / Azure storage account name |
| `*.objectStorage.minio.secretAccessKey`         | S3 secret access key / Azure account key      |
| `*.objectStorage.spider.auth.basic.password`    | Spider Basic auth password    |
| `*.objectStorage.spider.auth.jwt.token`         | Spider JWT token              |
| `*.objectStorage.tumblebug.auth.basic.password` | Tumblebug Basic auth password |
| `*.objectStorage.tumblebug.auth.jwt.token`      | Tumblebug JWT token           |

### Algorithm

| Component       | Algorithm            | Purpose                           |
| --------------- | -------------------- | --------------------------------- |
| Key exchange    | RSA-2048-OAEP-SHA256 | Securely transmit the AES key     |
| Data encryption | AES-256-GCM          | Fast encryption for any data size |

RSA alone can only encrypt ~245 bytes with a 2048-bit key, so each field gets its own random AES key, and that key travels with the field encrypted under RSA. No AES key is ever reused.

### Workflow

```
CLIENT                                   SERVER
──────                                   ──────
① request key ─────────────────────────► generate RSA key pair
                                          store the private key in the key store
          ◄───────────────────────────── return {publicKey, keyId, expiresAt}

② EncryptStorageModel()
   per sensitive field:
     random AES-256 key + 12-byte nonce
     AES-256-GCM(data)        → ct
     RSA-OAEP(AES key)        → ek
     Base64(JSON{v, ek, n, ct})

③ send the encrypted model ────────────► ④ DecryptStorageModel()
                                             look up the private key by keyId
                                             decrypt each field
                                             delete the key (single use)
                                          ⑤ run the migration
```

### Server side

```go
// At startup: 30-minute key lifetime, swept every 5 minutes.
transxex.InitKeyStore(30*time.Minute, 5*time.Minute)

// GET /publicKey — hand the client a key to encrypt with.
kp, err := transxex.GetKeyStore().GenerateKeyPair(transxex.GetKeyExpiry())
bundle, err := kp.ExportPublicBundle()
json.NewEncoder(w).Encode(bundle)

// POST /migrate — decrypt, then run.
var model transxex.StorageMigrationModel
json.NewDecoder(r.Body).Decode(&model)

model, err = transxex.DecryptStorageModel(model)   // no-op when the model is plaintext
if err != nil {
    http.Error(w, err.Error(), http.StatusBadRequest)
    return
}
err = transxex.MigrateStorage(model)
```

`DecryptStorageModel` passes a plaintext model through untouched, so the same handler serves both trusted and encrypted callers.

### Client side

```go
bundle := fetchPublicKeyBundle()                        // from GET /publicKey
pub, err := transxex.ParsePublicKeyBundle(bundle)

encrypted, err := transxex.EncryptStorageModel(model, pub, bundle.KeyID)
postJSON("/migrate", encrypted)
```

The encrypted model carries `encryptionKeyId`; which fields are encrypted is not transmitted, because both sides derive it from the same internal list.

### Security considerations

| Aspect                 | Implementation                                            |
| ---------------------- | --------------------------------------------------------- |
| **Algorithm**          | Hybrid: AES-256-GCM (data) + RSA-2048-OAEP (key exchange) |
| **Key validity**       | Default 30 minutes (configurable)                         |
| **One-time use**       | Keys are deleted automatically after decryption           |
| **Large data**         | Hybrid encryption handles data of any size                |
| **Transport security** | Always use HTTPS in production                            |
| **Key storage**        | In memory only — keys do not survive a restart            |

## Examples

| Example                                                        | What it shows                                                                          |
| -------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| [examples/file-migration-async](examples/file-migration-async/) | `MigrateStorageAsync` over an SSH→SSH relay: progress polling and SIGINT cancellation   |
| [examples/inspect](examples/inspect/)                           | `InspectFilesystem`, `InspectObjectStorage` and `InspectDBMS` with metrics and filters  |
| [examples/mysql-migration](examples/mysql-migration/)           | `MigrateDBMS` direct-to-direct: scopes, async progress, cross-version MySQL             |
| [examples/db-ver-matrix](examples/db-ver-matrix/)               | `MigrateDBMS` across every source/target version pair of an engine, as a matrix         |
| [examples/object-storage](examples/object-storage/)             | MinIO / CB-Spider / CB-Tumblebug transfers, including object storage to object storage  |

## Logging

```go
transxex.SetLogger(slog.New(zerologHandler))   // once, at startup
```

The library logs through `log/slog` and defaults to `slog.Default()`. Only the database domain emits records today.

## Version Management and Tagging

This module is independently versioned using Git tags with the `transx-ex/` prefix (e.g. `transx-ex/v0.1.0`).

### Releasing a new version

After your changes have been merged into `upstream/main`, create and push the version tag:

```bash
# 1) Fetch latest upstream and verify the merge
git fetch upstream
git log upstream/main --oneline -5

# 2) Check recent transx-ex tags to determine the next version
git tag -l "transx-ex/*" --sort=-v:refname | head -5

# 3) Set the next version manually, based on the list above
export NEXT_TRANSX_EX_TAG=  # e.g. export NEXT_TRANSX_EX_TAG="transx-ex/v0.1.0"
echo "Next transx-ex tag: $NEXT_TRANSX_EX_TAG"

# 4) Tag the merge result on upstream/main
git tag -a $NEXT_TRANSX_EX_TAG upstream/main -m "transx-ex: release ${NEXT_TRANSX_EX_TAG#transx-ex/}"
# Safer if upstream/main has moved:
# git tag -a $NEXT_TRANSX_EX_TAG <merge_commit_sha> -m "transx-ex: release ${NEXT_TRANSX_EX_TAG#transx-ex/}"

# 5) Push the tag
git push upstream $NEXT_TRANSX_EX_TAG

# 6) Verify
git show $NEXT_TRANSX_EX_TAG
```

> **Note:** Tag `upstream/main` (the merge result), not your old branch commit. If another PR lands before you tag, use the exact merge commit SHA instead of `upstream/main`.

### Updating the dependency in a consumer

```bash
# 1) Start a new branch from the latest upstream/main
git fetch upstream
git checkout upstream/main -b update-transx-ex-${NEXT_TRANSX_EX_TAG#transx-ex/}

# 2) Update the dependency
go get -u github.com/cloud-barista/cm-centipede/transx-ex
# Or pin the exact version:
# go get github.com/cloud-barista/cm-centipede/transx-ex@${NEXT_TRANSX_EX_TAG#transx-ex/}
go mod tidy

# 3) Verify, test, commit, push, open a PR
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
