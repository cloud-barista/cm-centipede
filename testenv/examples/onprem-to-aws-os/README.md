# On-premises MinIO → AWS object storage

A worked example of migrating a bucket from an on-premises MinIO onto a bucket in
AWS, **driven entirely over the REST APIs**. Every step below is one or two
`curl` calls, and [`migrate.sh`](migrate.sh) is the same calls in a file.

Three systems each do one thing:

```
   on-premises MinIO               cm-honeybee          inspect the source
   bucket "images"             ──▶ :8081                what is in it?
        │
        │                          cm-centipede         plan, migrate, validate
        └────────────────────── ──▶ :8085               move it, then check it
                                         │
   AWS bucket                            │              cm-beetle
   cpbt-aws-bucket         ◀─────────────┘              :8056  provisions the bucket
```

cm-honeybee reads the source. cm-centipede moves the objects and verifies them.
cm-beetle creates the bucket they land in — and cm-centipede reaches that bucket
through cm-beetle, so no S3 key for the target appears anywhere in this example.

The whole run is steps 1 to 8. Steps 1-3 stand up the pieces, 4-7 are the
migration itself, and 8 takes it all down again.

**Steps 4 to 7 are what [`migrate.sh`](migrate.sh) does.** Read them as the
commentary on that script: every call they describe appears in it, in the same
order and with the same request body. Steps 1-3 and 8 stay manual, because they
start and stop things that cost time and money. See
[Running it as one script](#running-it-as-one-script).

---

## 1. Start the stack

From the **repo root**. This brings up cm-honeybee, cm-centipede, cm-beetle,
cb-tumblebug and cb-spider together.

```bash
cd ../../..             # repo root
cp deployments/docker-compose/.env.example deployments/docker-compose/.env
                        # fill in the API credentials, once

make up                 # start every service
make init               # register the CSP credentials — run this once
```

**`make init` is a one-time step.** It decrypts
`~/.cloud-barista/credentials.yaml.enc` — asking for its password once — and
registers those credentials into OpenBao and into cb-tumblebug, which is what
creates the `aws-<region>` connection cm-beetle resolves. They are kept in
`deployments/docker-compose/data/` and survive a `make down`.

`make up` runs in the **foreground**: it builds, starts, then streams the logs,
so run the rest of this example from a second terminal. `make status` lists what
is running.

Check that the two servers this example talks to are answering:

```bash
curl -s http://localhost:8081/honeybee/readyz
curl -s -u default:default http://localhost:8085/centipede/readyz
```

---

## 2. Start the on-premises source

[`testenv/dockerenv`](../../dockerenv) provides the source: a container running
MinIO with six seeded buckets. It stands in for an on-premises object store, and
nothing about the API calls below would change for a real one.

```bash
cd ../../dockerenv
./dockerenv-up.sh
```

The container this example uses is **`minio-source`**:

| | |
|---|---|
| S3 API | `<host>:39000` |
| console | `http://<host>:39001` |
| account | `minioadmin` / `minioadmin123` |
| buckets | `raw-data` · `processed-data` · `images` · `documents` · `backups` · `logs` |

This example migrates **`images`**, which holds nine objects under two prefixes:

```
images/products/*.jpg    6 objects,  51,200 bytes each
images/banners/*.png     3 objects, 102,400 bytes each
```

Two prefixes, two extensions and two sizes, so any filter you write has a visible
effect.

---

## 3. Create the AWS bucket

[`testenv/beetleenv`](../../beetleenv) creates it through the cm-beetle API.

```bash
cd ../beetleenv
cp .env.example .env && chmod 600 .env    # fill in region and zones

./scripts/up.sh                           # check the stack, create the namespace
./scripts/provision.sh aws bucket         # seconds
./scripts/conn-info.sh aws --ids
```

The last command prints the two identifiers the migration needs. They are the
only thing carried forward from this step:

```
nsId        cpbt01
osId        cpbt-aws-bucket
```

`osId` is cm-beetle's handle, not the name AWS shows — cb-tumblebug generates its
own bucket name in the CSP. `./scripts/conn-info.sh aws` prints both.

---

## 4. Inspect the source with cm-honeybee

> **Steps 4 to 7 are the migration, and `migrate.sh` runs exactly these four.**
> Follow them by hand to see each call on its own, or run the script and read
> these sections as its commentary.

A **SourceGroup** holds connections; a **ConnectionInfo** is one source store.
Both are needed before anything can be read.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-os",
        "description": "on-premises MinIO source",
        "type": "minio",
        "provider_name": "onprem"
      }'
```

Take **`.id`** from the response — that is the `sgId` below.

> **`"type":"minio"` is not a choice.** An object storage inspect is refused for
> a source group of any other type. `"provider_name":"onprem"` is what makes
> `os_endpoint` the address honeybee dials, and means no region has to be given.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group/$SG_ID/connection_info \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-os-src",
        "os_access_type": "direct",
        "os_endpoint": "http://172.24.78.163:39000",
        "os_access_key_id": "minioadmin",
        "os_secret_access_key": "minioadmin123",
        "os_use_ssl": false,
        "os_scan_bucket": "images"
      }'
```

Take **`.id`** — the `connId`.

**No agent is installed here.** honeybee speaks S3 to MinIO directly, which is
what `"os_access_type":"direct"` says. That makes this call fast — and it also
means **the S3 credentials travel in the request body**, unlike the target side,
which is reached through cm-beetle with no key in sight.

> **`os_endpoint` carries the address of the host machine the source container
> runs on.** MinIO is reached through the port it publishes — `39000` above — so
> the address has to be the host's, not the container's, and not `127.0.0.1`.
>
> The scheme matters too: honeybee reads a scheme typed into `os_endpoint` as an
> explicit statement about TLS and lets it beat `os_use_ssl`, so `http://` is what
> actually turns TLS off for a local MinIO.

Now read the bucket:

```bash
curl -s -X POST \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/import/objectstorage \
  -H 'Content-Type: application/json' \
  -d '{
        "metric": {
          "total_size": true,
          "object_count": true,
          "extension_count": true,
          "prefix_mod_time": true,
          "prefix_object_count": true,
          "prefix_object_size": true
        }
      }'
```

Every metric is asked for here; each one costs a full pass over the bucket's keys,
so a bucket of any size is a reason to ask for fewer. A **prefix** is object
storage's answer to a folder: the `prefix_*` fields count only the objects
directly under each prefix, while the first three cover the whole scanned bucket.
`prefix` is also a request field of its own — set it to narrow the scan to one
prefix instead of the whole bucket.

> **One connection holds one bucket's result.** It is stored against the
> `connId`, so inspecting a second bucket through the same connection overwrites
> the first. A second bucket means a second `ConnectionInfo`.

Then fetch the result in the shape cm-centipede takes:

```bash
curl -s \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/objectstorage/refined
```

```json
{
  "sourceDataMigrationModel": {
    "objectStorages": [
      {
        "connection": {
          "source": "honeybee",
          "honeybee": {
            "connectionId": "..."
          }
        },
        "path": "images/",
        "folders": [
          { "key": "images/products/" },
          { "key": "images/banners/" }
        ]
      }
    ]
  }
}
```

**Keep this response whole.** It is already a `SourceDataMigrationModel`, which is
exactly what the plan call takes as its `source` — nothing is assembled by hand.
Note `.sourceDataMigrationModel.objectStorages[0].path`, the **scan root**; the
plan has to name it exactly, **trailing slash and all**. honeybee builds it as
`<bucket>/<prefix>` and normalises an empty prefix to `<bucket>/`.

---

## 5. Migrate with cm-centipede

> Every cm-centipede call needs BasicAuth — `-u default:default` by default.

### Build the plan

```bash
curl -s -X POST -u default:default http://localhost:8085/centipede/plans/target \
  -H 'Content-Type: application/json' \
  -d '{
        "source": <the /objectstorage/refined response, unchanged>,
        "plans": [{
          "srcConnection": {
            "source": "honeybee",
            "honeybee": { "connectionId": "<the connection id from step 2>" }
          },
          "dstConnection": {
            "source": "beetleObjectStorage",
            "beetleObjectStorage": {
              "nsId": "cpbt01",
              "osId": "cpbt-aws-bucket"
            }
          },
          "objectStorageFilter": {
            "targetMapping": {
              "srcName": "images/"
            },
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.png" }
            ]
          }
        }]
      }'
```

Take **`.data`** — the plan — and pass it to the next call unchanged.

**`plans` is an array: one entry per (source entry, destination).** This example
has one connection going to one bucket, so it holds a single entry. A source
group with several connections would send several — that is what `srcConnection`
is for, saying which entry of `source` this destination is meant for. Every entry
of `source` has to be named by exactly one of them, and two entries may not write
to the same place.

**`dstConnection` is a reference, not credentials.** `beetleObjectStorage` names
a bucket that already exists, and cm-centipede reaches it through cm-beetle and
cb-tumblebug. That is why step 3 is a prerequisite rather than something this step
does.

**`targetMapping` carries no `dstName` here**, and the plan fills it in: for a
`beetleObjectStorage` destination the first segment of the destination path is
rewritten to the `osId`, because cm-centipede builds its provider from the
`nsId`/`osId` pair and never passes that segment to it. So the objects land at
the root of the target bucket, keeping their prefixes:

```
images/products/logo.jpg   ──▶   products/logo.jpg
```

To put them under a prefix instead, write the `osId` yourself —
`"dstName": "cpbt-aws-bucket/archive/"`. A `dstName` you supply is left alone.

**`rules` are the filter**, evaluated top to bottom; the first rule that matches
decides, and anything no rule matches is kept. Two types:

| Type | Field | Matches |
|---|---|---|
| `glob` | `pattern` | `*.png` · `**/*.jpg` (any depth) · `banners/*` (a sub-path, at any depth — write `/banners/*` to anchor it at the prefix) · `banners` (a folder, and everything under it) |
| `size` | `op` + `value` | `>` `>=` `<` `<=` `==` against a byte count |

**A pattern is matched against the key with the migrated prefix removed**, which
is the same thing the filesystem example matches a file path against: one pattern
means one thing wherever it is attached. So a pattern naming a folder takes
everything under it — `banners` would drop `banners/spring/hero.png` too — and a
pattern carrying a `/` matches at any depth unless a leading `/` anchors it.

The rule above drops the three `.png` banners and lets the six `.jpg` products
through. **Validation knows about it** — an excluded object is reported as
skipped, not as missing from the destination.

> **The filter is evaluated exactly as written here.** An object storage transfer
> is S3 calls, and the same rule pipeline that produced this plan decides each
> object. The filesystem example has a caveat about this — its transfer is rsync,
> which re-expresses the rules as native flags and cannot honour every ordering —
> and none of it applies here.

The response says what was decided:

```json
{
  "targetDataMigrationModel": {
    "objectStorages": [
      {
        "buckets": [
          {
            "order": 1,
            "srcPath": "images/",
            "dstPath": "cpbt-aws-bucket",
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.png" }
            ]
          }
        ]
      }
    ]
  }
}
```

### Run it

```bash
curl -s -X POST -u default:default http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-os",
        "description": "on-premises MinIO bucket to an AWS bucket",
        "plan": <the plan from above>
      }'
```

Take **`.data.id`** — the `migrationId`, the handle for everything that follows.

Creating the migration starts it, so the call returns immediately. Poll:

```bash
curl -s -u default:default http://localhost:8085/centipede/migration/$MIG_ID
```

```json
{
  "success": true,
  "data": {
    "status": "running",
    "totalItems": 1,
    "processedItems": 0,
    "failedItems": 0,
    "transferredBytes": 307200
  }
}
```

`status` settles on `completed`, `failed` or `cancelled`.

---

## 6. Validate

This is the verdict, and it is cm-centipede's rather than something you assemble.
It re-lists **both buckets** and compares them object by object.

```bash
curl -s -X POST -u default:default \
  http://localhost:8085/centipede/migration/$MIG_ID/validation
curl -s -u default:default \
  http://localhost:8085/centipede/migration/$MIG_ID/validation
```

The POST starts it; the GET is polled while `validationStatus` is `running`.

```json
{
  "success": true,
  "data": {
    "validationStatus": "passed",
    "validationMessage": "",
    "details": [
      {
        "itemPath": "images/",
        "status": "skipped",
        "message": "3 object(s) excluded by the migration filter"
      }
    ]
  }
}
```

**Size first, then the ETag.** Both come out of the listing that was already
fetched, so neither costs an extra call. A size that differs is decisive on its
own. When the sizes agree, the ETag decides — but only when it means what it
appears to mean:

> **An ETag is the MD5 of the object only when it was stored in a single part.**
> A multipart upload's ETag is the MD5 of the concatenated part MD5s plus
> `-<partCount>`, so it depends on how the upload was cut up rather than on the
> bytes alone, and two stores that chose different part sizes report different
> ETags for identical content. Objects encrypted with SSE-KMS or SSE-C carry an
> ETag that is not an MD5 at all.
>
> Rather than call those a mismatch, validation reports them as **matched by size
> but not content-verified**, collapsed into one `warning` line per bucket with a
> few example keys. Every object in this seed is at most 512 KB, far below the
> 16 MiB at which the transfer switches to multipart, so you will not see that
> line here — a bucket of large objects is where it shows up.

Three statuses appear:

| Status | Meaning |
|---|---|
| `failed` | An object is missing, unexpected, differs in size, or its ETag differs |
| `warning` | Collapsed, one line per kind: sizes matched but the ETag could not speak for the content, or an excluded object is in the destination anyway |
| `skipped` | One line per bucket, counting what the filter deliberately left behind |

**Findings are budgeted.** At most 20 `failed` items are listed per bucket,
followed by a line saying how many there were in total. A migration that failed
wholesale reports a readable sample instead of one line per object.

---

## 7. Read the migration log

Per-item detail, including anything a failure left behind.

```bash
curl -s -u default:default \
  "http://localhost:8085/centipede/migration/$MIG_ID/logs?page=1&pageSize=100"
```

```json
{
  "success": true,
  "data": {
    "total": 1,
    "page": 1,
    "pageSize": 100,
    "items": [
      {
        "status": "completed",
        "itemPath": "images/",
        "durationMs": 2310,
        "sizeBytes": 307200
      }
    ]
  }
}
```

**It is paginated.** `pageSize` caps at 100 and defaults to 20, and `.data.total`
is the only sign that anything was left out. An object storage migration logs one
entry per migrated bucket, so one page is plenty here.

---

## 8. Stop and clean up

In this order — cm-centipede's record first, then what it referred to.

```bash
# the migration record (the plan and the logs go with it)
curl -s -X DELETE -u default:default \
  http://localhost:8085/centipede/migration/$MIG_ID

# the honeybee registration
curl -s -X DELETE \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID
curl -s -X DELETE http://localhost:8081/honeybee/source_group/$SG_ID
```

```bash
cd ../../beetleenv
./scripts/deprovision.sh aws bucket

cd ../dockerenv
./dockerenv-down.sh

cd ../..                            # repo root
make down
```

The namespace is kept by `deprovision.sh`, so another `provision.sh aws bucket`
can follow straight on. `./scripts/status.sh` in `beetleenv` shows what is still
up.

---

## Running it as one script

[`migrate.sh`](migrate.sh) is steps 4 to 7 end to end — everything between a
source that exists and a bucket that exists.

```bash
./migrate.sh
```

It is the happy path and nothing else: no retries, no error handling, no cleanup.
Each step assumes the one before it worked, which is what keeps it readable as a
description of the API.

It needs `curl` and `jq`, and is written to be read rather than reused: no helper
functions, so every call appears in full at the point it is made, and every
request body is written exactly as it goes on the wire — the same JSON as the
sections above. `jq` appears only to pull a single value out of a response
(an id, a status, the plan), never to build a body.

Everything it needs is a variable at the top, overridable from the environment:

| Variable | Default | |
|---|---|---|
| `HB_BASE` | `http://localhost:8081/honeybee` | where cm-honeybee answers |
| `CP_BASE` | `http://localhost:8085/centipede` | where cm-centipede answers |
| `CP_AUTH` | `default:default` | cm-centipede's BasicAuth |
| `HOST_IP` | `127.0.0.1` | the host machine the source container runs on — set this |
| `SRC_PORT` | `39000` | MinIO's S3 API port |
| `SRC_ACCESS_KEY` | `minioadmin` | the source's access key |
| `SRC_SECRET_KEY` | `minioadmin123` | the source's secret key |
| `SRC_BUCKET` | `images` | the bucket to migrate |
| `NS_ID` | `cpbt01` | from `conn-info.sh aws --ids` |
| `OS_ID` | `cpbt-aws-bucket` | " |

```bash
HOST_IP=172.24.78.163 SRC_BUCKET=documents ./migrate.sh
```
