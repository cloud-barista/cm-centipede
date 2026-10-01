# Object Storage Migration Matrix — cm-honeybee + cm-centipede over cm-beetle

Migrates **every source bucket into a managed bucket on every configured CSP**,
where the source is a MinIO container this folder builds and seeds, and the
target is object storage provisioned with **cm-beetle**. The outcome is reported
as a matrix.

```
  bucket \ csp   │ aws          ncp
  ───────────────┼──────────────────────────
  raw-data       │ PASS 12s     PASS 18s
  images         │ PASS 21s     FAIL 4s
  logs           │ PASS 7s      PASS 9s
```

The migration itself is **cm-centipede**, driven over its REST API, with
**cm-honeybee** inspecting the source. This folder provisions, prepares and
judges; it does not migrate anything itself.

> **Four servers must already be running** — cm-honeybee and cm-centipede for the
> migration, cm-beetle and cb-tumblebug for the provisioning (with cb-spider
> behind them). This folder starts none of them, and steps 1 and 2 of every run
> check that all four answer before a single bucket is created. `.env` points at
> them with `HB_BASE`, `CP_BASE`, `BEETLE_URL` and `TUMBLEBUG_URL`.

This is the object storage sibling of
[`beetle-db-test`](../beetle-db-test), and it follows the same shape. What
changes is the data: MinIO instead of a database, a CSP bucket instead of a
managed RDBMS instance. A bucket is not an instance, so that one change removes a
great deal — no network, no master password, no version axis, and one source
container for the whole run instead of one per cell.

### Starting that stack — `make up`, `make init`, `make down`

The backing servers come up in one command, from the **repo root**. The Compose
stack in [`deployments/docker-compose`](../../deployments) runs **cm-honeybee on
8081, cm-beetle on 8056 and cb-tumblebug on 1323** — exactly what the defaults in
`.env.example` point at, so a stack started this way needs no URL change here.

```bash
cd ../..                # repo root
cp deployments/docker-compose/.env.example deployments/docker-compose/.env
                        # fill in the API credentials, once

make up                 # start every service (beetle, tumblebug, spider, ...)
make init               # register the CSP credentials — run this once
make down               # stop and remove the containers
```

**`make init` is a one-time step**, and on this path it is the whole of the CSP
credential story. It decrypts `~/.cloud-barista/credentials.yaml.enc` — asking
for its password once — and registers those credentials into OpenBao and into
cb-tumblebug, which is what creates the `<csp>-<region>` connections beetle
resolves. Both stores keep them in `deployments/docker-compose/data/`, so they
survive a `make down` and every later `make up`.

---

## Quick start

```bash
cd testenv/beetle-os-test

cp .env.example .env && chmod 600 .env
# the defaults work against a deployments stack; set the regions you use, then:

./scripts/os-support.sh                     # (optional) is everything registered
./scripts/os-matrix.sh --only aws:raw-data  # one cell
```

Start with a single cell. One cell exercises the whole path — image build, seed,
inspection, bucket creation, plan, migration, validation, teardown — and costs
one bucket that lives for seconds. Widen once it goes green.

```bash
./scripts/os-matrix.sh                             # everything in OS_CSPS x OS_SRC_BUCKETS
./scripts/os-matrix.sh --csp aws
./scripts/os-matrix.sh --buckets "raw-data images"
./scripts/os-matrix.sh --keep-bucket               # leave the targets in place
./scripts/os-matrix.sh --cleanup                   # reclaim what an interrupted run left
```

---

## Support matrix

The source is MinIO, so every bucket works there. The **target** has to clear one
bar: cm-beetle must be able to create object storage on that CSP.

**Nothing about that is hard-coded here.** It is read at run time from
`GET /recommendation/middleware/objectStorage/support`, and `OS_CSPS` is checked
against the answer before anything is created. `./scripts/os-support.sh` prints
it on its own.

That is the one place this folder is simpler than `beetle-db-test`, which has to
keep a hand-written `BEETLE_ENGINES_<CSP>` table because cb-tumblebug's capability
endpoint pins `dbEngine` to `mysql` and cannot be asked the question at all.

`.env.example` ships `OS_CSPS="aws ncp"`. cb-tumblebug's own table
(`cspSupportingObjectStorage`) also lists azure, gcp, alibaba, tencent, ibm,
openstack, nhn and kt; whether beetle can reach them is what the support endpoint
answers on the day.

---

## How it fits together

```
 Step 0  Prerequisites      docker, jq, curl
            |               + cm-honeybee and cm-centipede running
            |               + cm-beetle, cb-tumblebug and cb-spider running,
            |                 with the <csp>-<region> connection registered
 Step 1  .env               cp from .example, chmod 600, set the regions
            |
 Step 2  ./scripts/os-support.sh          (optional) what can beetle do, and where
            |
 Step 3  ./scripts/os-matrix.sh           the matrix itself
            |    ├ pre-flight   honeybee, centipede, beetle, tumblebug,
            |    │              object storage support, connection, namespace
            |    ├ source       build -> start -> seed -> read the buckets back
            |    ├ collect      one honeybee inspect per bucket, cached
            |    └ per cell     create bucket -> plan -> migrate -> validate -> delete
            |
 Step 4  logs/os-matrix.log
         logs/os-matrix-api.log
         logs/os-matrix-result.json
            |
 Step 5  ./scripts/os-matrix.sh --cleanup   only if a run was killed outright
```

---

## The source container

A source is **a machine, not a process**. `src/Dockerfile.minio` builds Ubuntu
22.04 with systemd as PID 1, so `systemctl` works, sshd is a unit, and MinIO is a
service that starts at boot — which is what an on-premises source is. It is
[`dockerenv`](../dockerenv)'s `compose/Dockerfile.minio` without the `ROLE`
argument: this folder only ever builds a source, because the target is a CSP
bucket that cm-beetle creates.

**Six things are required to run systemd in a container**, and `src_start` in
`lib/source.sh` supplies all of them:

```
--privileged                          cgroup manipulation
--cgroupns=host                       compose's `cgroup: host`
-v /sys/fs/cgroup:/sys/fs/cgroup:rw   systemd reads and writes it
--tmpfs /run                          units' runtime directory
--tmpfs /tmp:exec                     an executable scratch space
--stop-signal SIGRTMIN+3              systemd's graceful shutdown signal
```

> ⚠ `--privileged` and a host cgroup mount. **Local testing only, never
> production** — the same warning `dockerenv` carries, for the same reason.

**One container serves the whole run.** The managed-DB matrix rebuilds its source
per cell because its row axis is the source engine version; here the row axis is
the bucket and one MinIO holds all six. Image built once, container started once,
each bucket inspected once.

**Readiness is one signal.** `matrix-init.service` is `Type=oneshot` with
`RemainAfterExit=yes`, so the unit stays `active` after `init-minio.sh` returns.
That makes `systemctl is-active matrix-init.service` mean "MinIO is up, the
buckets exist and every object is uploaded" — one question instead of three.

**Settings reach the container as a file, not as environment variables.** systemd
does not hand its own environment to the units it starts, so anything passed with
`docker run -e` would be invisible to the init script. `lib/source.sh` writes a
per-run env file on the host and bind-mounts it at `/opt/matrix/matrix.env`; the
image ships `defaults.env` so it also runs on its own.

> ⚠ Both files quote their values, because `init-minio.sh` sources them. An
> unquoted `OS_SRC_BUCKETS` makes the shell try to run `processed-data` as a
> command, and `matrix-init.service` then reports `failed` with that as its only
> clue.

**A bind mount that does not land is caught.** docker creates a bind mount whose
source path does not exist as an empty **directory**, silently, and the seed then
falls back to the image defaults — building six buckets when three were asked
for. `assert_env_mounted` checks for a regular file inside the container and
stops the run with the reason. This is what happens when the matrix itself runs
inside a container against a shared docker daemon.

**The MinIO root account is baked into the unit.** `minio.service` carries
`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` as `Environment=`, and systemd reads a
unit's environment when the unit loads — so changing them in `.env` alone would
leave the server on the old pair while honeybee is handed the new one, failing as
a 403 several steps later. `assert_minio_account` refuses the mismatch up front.

Container name `beetle-os-test-minio-src`, published ports in the **33xxx** band,
resource prefix `cpbos` — so this folder, `beetle-db-test`, `beetleenv`,
`tofu-db-test` and `dockerenv` can all be up at once.

### The seed

`src/scripts/init-minio.sh` is dockerenv's seed, object for object, so a result
here is comparable with a docker-to-docker run.

| Bucket | Objects | What is in it |
|---|--:|---|
| `raw-data` | 8 | sensor CSV x3, sales CSV x3, JSONL events, **the UTF-8 canary** |
| `processed-data` | 2 | JSON aggregate reports |
| `images` | 9 | binary — jpg 50KB x6, png 100KB x3 |
| `documents` | 8 | contracts x3, invoices x5 (text) |
| `backups` | 5 | binary — 200KB x4, 500KB x1 |
| `logs` | 4 | application logs x3, error log x1 |

36 objects, about 1.9 MB. Text, binary, nested prefixes (`raw-data/sensors/…`)
and one deliberately multibyte object
(`raw-data/utf8_encoding_check.json`, Korean/Japanese/emoji) so a migration that
mangles a body or its encoding shows up there and nowhere else. **Every other
object is ASCII-only on purpose.**

`OS_SRC_BUCKETS` controls both what is seeded and what is migrated, so trimming
it makes the run shorter *and* the seed cheaper. A name with no `seed_<name>`
function is refused before the first bucket is created, rather than producing a
bucket the matrix would then report as empty.

---

## One cell, step by step

| Step | What happens |
|---|---|
| 1 | Create the target bucket — cm-beetle recommends, this folder names it, `POST`, wait |
| 2 | `POST /plans/target` — the source model from honeybee, the target as a `beetleObjectStorage` ref |
| 3 | `POST /migration`, then poll until it settles |
| 4 | `POST /migration/{id}/validation`, then poll — this is the verdict |
| 5 | `GET /migration/{id}/logs` — the per-item migration log |
| 6 | Delete the target bucket, record the result |

**Step 1 comes first for a reason.** centipede creates no bucket of its own:
`MigrateObjectStorage` resolves both ends and hands them to transx-ex. The bucket
has to be there before the plan runs.

**One `osId` is one bucket.** For a `beetleObjectStorage` destination the plan
rewrites `dstPath`'s first segment to the `osId`
(`pkg/core/plan/target.go`, `resolveObjectStorageDstPath`), because transx-ex
builds its Tumblebug provider from the `nsId`/`osId` pair and never sees that
segment. Two source buckets in one plan would therefore both land in the same
bucket — which is why a cell carries exactly one, and gets a bucket of its own.

**No `objectStorageFilter` is sent.** Sending one would mean matching
`targetMapping.srcName` against honeybee's scan root exactly — `raw-data/`,
trailing slash and all. Leaving it out makes `selectMigrationPath` take the scan
root as both ends, and the destination is rewritten to the `osId` anyway. Whole
buckets are what this folder migrates, so the filter has nothing to say.

**No `dbmsOnFailure` either.** It is DBMS-only, and `CreateMigrationReq` says
why: a filesystem or object storage transfer leaves whatever it had already
written, with or without the field.

**Validation is centipede's, not this folder's.** It re-lists both buckets,
strips each side's prefix and compares the resulting relative-key → ETag maps,
reporting three kinds of finding: an object missing in the destination, an object
in the destination the source does not have, and an ETag that differs.

> ⚠ ETag equality is only meaningful while both sides upload in a single part.
> Every object in this seed is at most 512 KB, so it is. A much larger seed would
> start producing multipart ETags, which are not comparable across
> implementations, and this verdict would stop meaning what it says.

---

## Verdicts and results

| Verdict | Meaning |
|---|---|
| **PASS** | Migration `completed` and validation `passed` |
| **FAIL** | Anything else |
| **SKIP** | Filtered out, or the source has no bucket by that name |

There is no **BLOCK**. In the managed-DB matrix that verdict means "a
higher-to-lower version pair the plan refused up front", and object storage has
no version to compare.

Outputs land in `logs/`:

- `os-matrix.log` — the full run, with colour stripped
- `os-matrix-api.log` — every beetle, honeybee and centipede call as a runnable
  `curl` line plus the response, with credentials masked
- `os-matrix-result.json`:

```json
{
  "namePrefix": "cpbos",
  "nsId": "cpbos01",
  "provisioner": "cm-beetle",
  "migrator": "cm-centipede",
  "collector": "cm-honeybee",
  "srcMode": "direct",
  "dstMode": "direct",
  "srcType": "minio",
  "dstType": "beetleObjectStorage",
  "finishedAt": "2026-09-10 18:20:11",
  "csps": [
    { "csp": "aws", "region": "ap-northeast-2",
      "cells": [
        { "bucket": "raw-data", "status": "PASS", "elapsedSec": 12,
          "osId": "cpbos-aws-os-raw-data", "cspBucketName": "tb-os-4kq2n7",
          "srcScanRoot": "raw-data/", "srcObjectCount": 8, "srcSizeBytes": 5275,
          "migrationId": "01J...", "validationStatus": "passed",
          "detail": "validation passed (8 objects)" }
      ] }
  ]
}
```

`cspBucketName` is the bucket's real name in the CSP — cb-tumblebug generates it
from a uid of its own, which is why there is no bucket-name setting anywhere here
and why S3's global namespace is not something this folder has to think about.
`migrationId` is the handle for reading a cell back out of centipede afterwards,
which is why `KEEP_MIGRATION=1` is the default.

The two `.log` files **accumulate**. A run appends to whatever is already there
and opens its section with a separator carrying the timestamp and the command
line, so searching for `=== run` steps between runs. Nothing rotates them; set
`NO_LOG=1` to write none. The result JSON is a single document rather than a log,
so it is **replaced** each run.

---

## Configuration reference

Everything lives in `.env`. Precedence:

```
CLI option  >  real shell variable  >  .env  >  script default
```

`.env.example` documents every key. The ones that decide behaviour:

| Key | Default | What it does |
|---|---|---|
| `OS_CSPS` | `aws ncp` | the columns — checked against beetle's support matrix before anything is created |
| `OS_SRC_BUCKETS` | the six above | the rows, and what the source seeds |
| `BEETLE_URL` / `TUMBLEBUG_URL` | `:8056/beetle` / `:1323/tumblebug` | where the provisioner answers |
| `HB_BASE` / `CP_BASE` | `:8081` / `:8085` | where cm-honeybee and cm-centipede answer |
| `MATRIX_NS` | `cpbos01` | the cb-tumblebug namespace — this folder's own |
| `MATRIX_NAME_PREFIX` | `cpbos` | 2-6 chars; every bucket is `<prefix>-<csp>-os-<bucket>`, and `--cleanup` matches on it |
| `<CSP>_REGION` | | must match the region the connection was registered under |
| `HOST_IP` | `127.0.0.1` | how honeybee reaches the source container's published port |
| `MINIO_SRC_API_PORT` | `33900` | published S3 port (console `33901`, SSH `33922`) |
| `MINIO_ROOT_USER` / `_PASSWORD` | `minioadmin` / `minioadmin123` | must match `src/services/minio.service` |
| `MINIO_VERSION` | *(empty)* | empty takes the current release; pin a `RELEASE.*` tag for a reproducible result |
| `KEEP_BUCKET` | `0` | `1` leaves every target bucket in place (they hold objects) |
| `KEEP_ON_FAIL` | `0` | `1` leaves a failed cell's bucket — **partial data** |
| `KEEP_SOURCE` | `0` | `1` leaves the MinIO container running |
| `BUCKET_TIMEOUT` | `300` | waiting for cm-beetle to create a bucket |
| `POLL_INTERVAL` | `5` | centipede's polling |

### Reclaiming what a run left

```bash
./scripts/os-matrix.sh --cleanup
```

cb-tumblebug's namespace is the state, so this needs nothing from a previous run:
buckets are found by listing the namespace and matching `<prefix>-<csp>-os-`.
A create whose request reached the CSP but whose record never reached tumblebug
is in no list — those are recorded in `logs/.inflight-<csp>.json` while the create
is in flight, and reported (never deleted) so a person can check the console.
