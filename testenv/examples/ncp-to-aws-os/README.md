# NCP Object Storage → AWS object storage

A worked example of migrating a bucket from **one cloud to another** — NCP Object
Storage to AWS — **driven entirely over the REST APIs**. Every step below is one
or two `curl` calls, and [`migrate.sh`](migrate.sh) is the same calls in a file.

Three systems each do one thing:

```
   NCP Object Storage              cm-honeybee          inspect the source
   cptf-ncp-bucket-test        ──▶ :8081                what is in it?
        │
        │                          cm-centipede         plan, migrate, validate
        └────────────────────── ──▶ :8085               move it, then check it
                                         │
   AWS bucket                            │              cm-beetle
   cpbt-aws-bucket         ◀─────────────┘              :8056  provisions the bucket
```

cm-honeybee reads the source. cm-centipede moves the objects and verifies them.
cm-beetle creates the bucket they land in.

**The two ends are named in completely different ways, and that is the point of
this example.** The source is reached with S3 credentials this example supplies;
the target is a cm-beetle reference with no key in sight. Both appear in the same
plan request, a few lines apart.

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
curl -s -u "$CP_USER:$CP_PASS" http://localhost:8085/centipede/readyz
```

> This example runs **`tofuenv` and this stack at the same time**, and each
> brings up an OpenBao of its own. They listen on different ports and do not
> collide.

---

## 2. Create the NCP bucket and fill it

[`testenv/tofuenv`](../../tofuenv) creates the source with OpenTofu and loads it
with generated data.

```bash
cd ../../tofuenv
cp .env.example .env && chmod 600 .env    # NCP keys, region, bucket name

./scripts/up.sh                           # OpenBao + the tofu runner
./scripts/provision.sh ncp bucket
./scripts/gen-data.sh --provider ncp --target bucket
./scripts/conn-info.sh ncp bucket
```

`gen-data.sh` uploads one file per format under a `gendata/` prefix:

```
gendata/…/csv/csv_0.csv    json/json_0.json    xml/xml_0.xml    sql/sql_0.sql
          txt/txt_0.txt    png/png_0.png       gif/gif_0.gif    zip/zip_0.zip
```

Each file is **about 1 MiB**, and raising the counts in
`gendata/config/config.json` adds more files rather than bigger ones — so every
object stays well under the 16 MiB at which an upload turns multipart. Step 6
explains why that matters.

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

The last command prints the two identifiers the migration needs:

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
        "name": "ncp-to-aws-os",
        "description": "NCP Object Storage source",
        "type": "minio",
        "provider_name": "ncp",
        "region_name": "kr"
      }'
```

Take **`.id`** from the response — that is the `sgId` below.

> **`"type":"minio"` is not a statement about the software.** An object storage
> inspect is refused for a source group of any other type — `csp` included — and
> `provider_name` is what says which cloud it really is. honeybee accepts `aws`,
> `alibaba`, `tencent`, `ncp`, `nhn`, `ibm`, `gcp`, `kt`, `azure`, `openstack`
> and `onprem`.

**`region_name` is required for NCP, and it builds the endpoint.** honeybee knows
where NCP Object Storage lives and assembles the host itself:

```
<region_name>.object.ncloudstorage.com     →  kr.object.ncloudstorage.com
```

So there is **no `os_endpoint` in the call below** — unlike an on-premises MinIO,
where the caller supplies the host. Use lowercase: tofuenv writes
`TF_VAR_ncp_region=KR`, and the host is `kr.…`.

cm-centipede reads the same two fields off the source group and runs the same
provider table when it comes to move the objects, so the host it dials is the one
honeybee inspected. Stating `os_endpoint` anyway still works — it overrides the
built host on both sides, which is what a private gateway or a VPC endpoint
needs.

**`region_name` is the only region you name.** The connection below carries the
keys and the bucket to read — no endpoint, and no region of its own.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group/$SG_ID/connection_info \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "ncp-to-aws-os-src",
        "os_access_type": "direct",
        "os_access_key_id": "'"$SRC_ACCESS_KEY"'",
        "os_secret_access_key": "'"$SRC_SECRET_KEY"'",
        "os_scan_bucket": "'"$SRC_BUCKET"'"
      }'
```

Take **`.id`** — the `connId`.

**`os_use_ssl` is absent on purpose.** It is honoured only where the caller
supplies the host — `onprem` and `openstack` — and for `ncp` the transport is the
provider's own fixed value. Stating it here would only suggest otherwise.

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
`prefix` is also a request field of its own — set it to `"gendata/"` to scan only
what `gen-data.sh` wrote, instead of the whole bucket.

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
        "path": "cptf-ncp-bucket-test/",
        "folders": [
          { "key": "gendata/" }
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

> Every cm-centipede call needs BasicAuth — `-u "$CP_USER:$CP_PASS"` below, the
> credentials from cm-centipede's own config. Load them from this folder's `.env`
> once per shell: `set -a; . ./.env; set +a`.

### Build the plan

```bash
curl -s -X POST -u "$CP_USER:$CP_PASS" http://localhost:8085/centipede/plans/target \
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
              "srcName": "cptf-ncp-bucket-test/"
            },
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.zip" }
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

**The two ends are named in opposite styles, side by side.** The source arrives
inside `source` as honeybee collected it, carrying a `honeybee` reference that
cm-centipede resolves back into the NCP endpoint and the S3 keys it was given in
step 4. The destination is `beetleObjectStorage`: a bucket that already exists,
reached through cm-beetle and cb-tumblebug, with **no key in this request at
all**. One end is credentials, the other is a reference.

**`targetMapping` carries no `dstName` here**, and the plan fills it in: for a
`beetleObjectStorage` destination the first segment of the destination path is
rewritten to the `osId`, because cm-centipede builds its provider from the
`nsId`/`osId` pair and never passes that segment to it. So the objects land at
the root of the target bucket, keeping their prefixes:

```
cptf-ncp-bucket-test/gendata/csv/csv_0.csv   ──▶   gendata/csv/csv_0.csv
```

To put them under a prefix instead, write the `osId` yourself —
`"dstName": "cpbt-aws-bucket/archive/"`. A `dstName` you supply is left alone.

**`rules` are the filter**, evaluated top to bottom; the first rule that matches
decides, and anything no rule matches is kept. Two types:

| Type | Field | Matches |
|---|---|---|
| `glob` | `pattern` | `*.zip` · `**/*.csv` (any depth) · `gendata/*` (a sub-path, at any depth — write `/gendata/*` to anchor it at the prefix) · `gendata` (a folder, and everything under it) |
| `size` | `op` + `value` | `>` `>=` `<` `<=` `==` against a byte count |

**A pattern is matched against the key with the migrated prefix removed**, which
is the same thing the filesystem example matches a file path against: one pattern
means one thing wherever it is attached. So a pattern naming a folder takes
everything under it — `gendata` would drop `gendata/orders.csv` too — and a
pattern carrying a `/` matches at any depth unless a leading `/` anchors it.

The rule above drops the one `.zip` object and lets the other seven through.
**Validation knows about it** — an excluded object is reported as skipped, not as
missing from the destination.

> **The filter is evaluated exactly as written here.** An object storage transfer
> is S3 calls, and the same rule pipeline that produced this plan decides each
> object. The filesystem example has a caveat about this — its transfer is rsync,
> which re-expresses the rules as native flags and cannot honour every ordering —
> and none of it applies here.

### Run it

```bash
curl -s -X POST -u "$CP_USER:$CP_PASS" http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "ncp-to-aws-os",
        "description": "NCP Object Storage bucket to an AWS bucket",
        "plan": <the plan from above>
      }'
```

Take **`.data.id`** — the `migrationId`, the handle for everything that follows.

Creating the migration starts it, so the call returns immediately. Poll:

```bash
curl -s -u "$CP_USER:$CP_PASS" http://localhost:8085/centipede/migration/$MIG_ID
```

```json
{
  "success": true,
  "data": {
    "status": "running",
    "totalItems": 1,
    "processedItems": 0,
    "failedItems": 0,
    "transferredBytes": 3145728
  }
}
```

`status` settles on `completed`, `failed` or `cancelled`.

> **This transfer crosses two clouds.** Every object is read out of NCP and
> written into AWS through cm-centipede, so it is bounded by whichever link is
> slower — and NCP charges for the egress.

---

## 6. Validate

This is the verdict, and it is cm-centipede's rather than something you assemble.
It re-lists **both buckets** and compares them object by object.

```bash
curl -s -X POST -u "$CP_USER:$CP_PASS" \
  http://localhost:8085/centipede/migration/$MIG_ID/validation
curl -s -u "$CP_USER:$CP_PASS" \
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
        "itemPath": "cptf-ncp-bucket-test/",
        "status": "skipped",
        "message": "1 object(s) excluded by the migration filter"
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
> few example keys.
>
> **This matters more here than in a single-cloud migration**, because the two
> sides are different implementations that made their own part-size choices. The
> generated objects are about 1 MiB each, far below the 16 MiB at which the
> transfer turns multipart, so you will not see that line — a bucket of large
> objects is where it shows up.

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
curl -s -u "$CP_USER:$CP_PASS" \
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
        "itemPath": "cptf-ncp-bucket-test/",
        "durationMs": 18740,
        "sizeBytes": 7340032
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
curl -s -X DELETE -u "$CP_USER:$CP_PASS" \
  http://localhost:8085/centipede/migration/$MIG_ID

# the honeybee registration
curl -s -X DELETE \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID
curl -s -X DELETE http://localhost:8081/honeybee/source_group/$SG_ID
```

Both clouds, then the stack:

```bash
cd ../../beetleenv
./scripts/deprovision.sh aws bucket

cd ../tofuenv
./scripts/deprovision.sh ncp bucket
./scripts/down.sh

cd ../..                            # repo root
make down
```

**Two providers now bill you, so clean up both.** `deprovision.sh ncp bucket`
needs the bucket empty on some paths — `./scripts/gen-data.sh --provider ncp
--target bucket --cleanup` empties it first. In `beetleenv` the namespace is kept,
so another `provision.sh aws bucket` can follow straight on.

---

## Running it as one script

[`migrate.sh`](migrate.sh) is steps 4 to 7 end to end — everything between a
source bucket that holds data and a target bucket that exists.

```bash
cp .env.example .env && chmod 600 .env    # once: change every ChangeMe
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

Everything it needs comes from `.env` (copied from `.env.example`), and a variable
set in the shell wins over the file. It stops before its first call while
`CP_USER`, `CP_PASS`, `SRC_BUCKET`, `SRC_ACCESS_KEY` or `SRC_SECRET_KEY` is still `ChangeMe`.

| Variable | `.env.example` | |
|---|---|---|
| `HB_BASE` | `http://localhost:8081/honeybee` | where cm-honeybee answers |
| `CP_BASE` | `http://localhost:8085/centipede` | where cm-centipede answers |
| `CP_USER` / `CP_PASS` | `ChangeMe` | cm-centipede's BasicAuth — must be changed |
| `SRC_PROVIDER` | `ncp` | the source cloud |
| `SRC_REGION` | `kr` | `region_name` on the source group, and the region in the endpoint host |
| `SRC_BUCKET` | `ChangeMe` | `TF_VAR_ncp_bucket_name` in tofuenv's `.env` |
| `SRC_ACCESS_KEY` | `ChangeMe` | the NCP access key |
| `SRC_SECRET_KEY` | `ChangeMe` | " |
| `NS_ID` | `cpbt01` | from `conn-info.sh aws --ids` |
| `OS_ID` | `cpbt-aws-bucket` | " |

**The two keys have no defaults on purpose.** While one is still `ChangeMe` or
empty, the script stops before its first call rather than at the inspect call
several steps later. Written into `.env` they sit there in plain text, protected
only by its mode; to keep them off disk, leave them empty in `.env` and pass them in:

```bash
SRC_ACCESS_KEY=... SRC_SECRET_KEY=... SRC_BUCKET=my-bucket ./migrate.sh
```
