# On-premises filesystem → AWS VM

A worked example of migrating a directory from an on-premises machine onto a VM
in AWS, **driven entirely over the REST APIs**. Every step below is one or two
`curl` calls, and [`migrate.sh`](migrate.sh) is the same calls in a file.

Three systems each do one thing:

```
   on-premises host                cm-honeybee          inspect the source
   /testdata                   ──▶ :8081                what is there?
        │
        │                          cm-centipede         plan, migrate, validate
        └────────────────────── ──▶ :8085               move it, then check it
                                         │
   AWS VM                                │              cm-beetle
   /home/cb-user/testdata  ◀─────────────┘              :8056  provisions the VM
```

cm-honeybee reads the source. cm-centipede moves the data and verifies it.
cm-beetle creates the VM the data lands on — and cm-centipede reaches that VM
through cm-beetle, so no SSH key for the target appears anywhere in this example.

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

---

## 2. Start the on-premises source

[`testenv/dockerenv`](../../dockerenv) provides the source: a container running
Ubuntu with systemd and sshd, with a populated `/testdata` tree. It stands in for
an on-premises machine, and nothing about the API calls below would change for a
real one.

```bash
cd ../../dockerenv
./dockerenv-up.sh
```

The container this example uses is **`fs-source`**:

| | |
|---|---|
| SSH | `localhost:32210`, user `root` |
| key | `testenv/dockerenv/ssh_keys/id_rsa` (generated on first run) |
| data | `/testdata` — documents, csv/json, media, logs, scripts |

**Key authentication, not a password.** cm-honeybee refuses a password on an fs
connection (HTTP 400), and cm-centipede authenticates SSH by key only, so a
source it cannot reach by key cannot even be registered.

The source also needs **outbound internet access** and **rsync** — see step 4 for
why.

---

## 3. Create the AWS VM

[`testenv/beetleenv`](../../beetleenv) creates it through the cm-beetle API.

```bash
cd ../beetleenv
cp .env.example .env && chmod 600 .env    # fill in region and zones

./scripts/up.sh                           # check the stack, create the namespace
./scripts/provision.sh aws vm             # 2-5 minutes
./scripts/conn-info.sh aws --ids
```

The last command prints the three identifiers the migration needs. They are the
only thing carried forward from this step:

```
nsId        cpbt01
infraId     cpbt-aws-infra
nodeId      vm-beetleenv-source-01-1
```

**rsync has to be on the VM too.** The transfer shells out to `rsync -e ssh`,
which needs it on both ends. `./scripts/conn-info.sh aws --ssh` prints a ready-made
`ssh` command if you want to check or install it.

---

## 4. Inspect the source with cm-honeybee

> **Steps 4 to 7 are the migration, and `migrate.sh` runs exactly these four.**
> Follow them by hand to see each call on its own, or run the script and read
> these sections as its commentary.

A **SourceGroup** holds connections; a **ConnectionInfo** is one source machine.
Both are needed before anything can be read.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-fs",
        "description": "on-premises filesystem source",
        "type": "fs"
      }'
```

Take **`.id`** from the response — that is the `sgId` below.

> **`"type":"fs"` is not a choice.** A filesystem inspect is refused for a source
> group of any other type.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group/$SG_ID/connection_info \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-fs-src",
        "ip_address": "'"$HOST_IP"'",
        "ssh_port": "32210",
        "user": "root",
        "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...",
        "fs_scan_path": "/testdata"
      }'
```

Take **`.id`** — the `connId`.

Two things about this call are worth knowing before the first slow run.

**Registering the connection installs the honeybee agent.** cm-honeybee does not
read a remote filesystem itself; it runs its agent on the source over SSH, and
the agent is installed by this very call. The install script downloads a binary
from the internet, so the source needs outbound access and the call takes tens of
seconds.

**It answers `200` whether or not that worked.** The two status fields in the
response are the only report:

```json
{
  "connection_status": "success",
  "agent_status": "success"
}
```

Anything else means the inspect below will fail, and these fields say why —
`connection_failed_message` and `agent_failed_message` carry the reason.

> **`ip_address` is the address of the host machine the source container runs
> on.** The source is reached through the port it publishes — `32210` above — so
> the address has to be the host's, not the container's, and not `127.0.0.1`.

Now read the tree:

```bash
curl -s -X POST \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/import/fs \
  -H 'Content-Type: application/json' \
  -d '{
        "max_depth": 0,
        "metric": {
          "total_size": true,
          "file_count": true,
          "extension_count": true,
          "folder_mod_time": true,
          "folder_perms": true,
          "folder_file_count": true,
          "folder_file_size": true
        }
      }'
```

`max_depth: 0` lists the whole tree. Every metric is asked for here; each one
costs a full walk of the source, so a large dataset is a reason to ask for fewer.

Then fetch the result in the shape cm-centipede takes:

```bash
curl -s \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/fs/refined
```

```json
{
  "sourceDataMigrationModel": {
    "fileSystems": [
      {
        "connection": {
          "source": "honeybee",
          "honeybee": {
            "connectionId": "..."
          }
        },
        "path": "/testdata",
        "folders": [
          { "path": "/testdata/documents" },
          { "path": "/testdata/data" },
          { "path": "/testdata/logs" }
        ]
      }
    ]
  }
}
```

**Keep this response whole.** It is already a `SourceDataMigrationModel`, which is
exactly what the plan call takes as its `source` — nothing is assembled by hand.
Note `.sourceDataMigrationModel.fileSystems[0].path`, the **scan root**; the plan
has to name it exactly.

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
        "source": <the /fs/refined response, unchanged>,
        "plans": [{
          "srcConnection": {
            "source": "honeybee",
            "honeybee": { "connectionId": "<the connection id from step 2>" }
          },
          "dstConnection": {
            "source": "beetleSsh",
            "beetleSsh": {
              "nsId": "cpbt01",
              "infraId": "cpbt-aws-infra",
              "nodeId": "vm-beetleenv-source-01-1"
            }
          },
          "fileSystemFilter": {
            "targetMapping": {
              "srcName": "/testdata",
              "dstName": "/home/cb-user/testdata"
            },
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.log" },
              { "action": "exclude", "type": "size", "op": ">=", "value": 1048576 }
            ]
          }
        }]
      }'
```

Take **`.data`** — the plan — and pass it to the next call unchanged.

**`plans` is an array: one entry per (source entry, destination).** This example
has one connection going to one node, so it holds a single entry. A source group
with several connections would send several — that is what `srcConnection` is
for, saying which entry of `source` this destination is meant for. Every entry of
`source` has to be named by exactly one of them, and two entries may not write to
the same place.

**`dstConnection` is a reference, not credentials.** `beetleSsh` names a node that
already exists, and cm-centipede resolves the address, the login account and the
private key through cm-beetle. That is why step 3 is a prerequisite rather than
something this step does.

**`targetMapping` is not optional.** Leave it out and the destination path equals
the source path, so the transfer tries to write `/testdata` at the **root** of the
target VM — a permission denied for an ordinary user. `dstName` puts the tree
under the node's home instead. `srcName` must equal the scan root exactly, which
is why it is read back from the inspect rather than assumed.

**`rules` are the filter**, evaluated top to bottom; the first rule that matches
decides, and anything no rule matches is kept. Two types:

| Type | Field | Matches |
|---|---|---|
| `glob` | `pattern` | `*.log` · `**/*.txt` (any depth) · `logs` (a directory, and its whole subtree) |
| `size` | `op` + `value` | `>` `>=` `<` `<=` `==` against a byte count |

The two rules above drop the log files and anything of 1 MB or more. **Validation
knows about them** — an excluded file is reported as skipped, not as missing from
the destination.

> **`>=` rather than `>` is deliberate here.** A size bound is exact to the byte,
> and the two `.mp4` files in this dataset are exactly 1048576 bytes — so `>`
> would keep both and the size rule would exclude nothing at all. Reach for `>=`
> when the threshold itself should go.

The response says what was decided:

```json
{
  "targetDataMigrationModel": {
    "fileSystems": [
      {
        "strategy": "relay",
        "dstType": "filesystem",
        "folders": [
          {
            "order": 1,
            "srcPath": "/testdata",
            "dstPath": "/home/cb-user/testdata",
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.log" },
              { "action": "exclude", "type": "size", "op": ">=", "value": 1048576 }
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
curl -s -X POST -u "$CP_USER:$CP_PASS" http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-fs",
        "description": "on-premises filesystem to an AWS VM",
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
    "transferredBytes": 1048576
  }
}
```

`status` settles on `completed`, `failed` or `cancelled`.

---

## 6. Validate

This is the verdict, and it is cm-centipede's rather than something you assemble.
It re-reads **both ends over SSH** and compares a SHA256 per file.

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
        "itemPath": "/testdata",
        "status": "skipped",
        "message": "9 file(s) excluded by the migration filter"
      }
    ]
  }
}
```

Three statuses appear per item:

| Status | Meaning |
|---|---|
| `failed` | A file is missing, unexpected, or its checksum differs |
| `warning` | A file the filter excluded is nonetheless present in the destination — a leftover from an earlier run, not a fault of this one |
| `skipped` | One line per folder, counting what the filter deliberately left behind |

With the rules from step 5 the seven `.log` files and the two 1 MB `.mp4` files
are excluded, so the run reports **`passed`** with a single `skipped` line
counting all nine — the filter and the verdict agree.

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
        "itemPath": "/testdata",
        "durationMs": 8421,
        "sizeBytes": 2752512
      }
    ]
  }
}
```

**It is paginated.** `pageSize` caps at 100 and defaults to 20, and `.data.total`
is the only sign that anything was left out. A filesystem migration logs one entry
per migrated folder, so one page is plenty here.

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

```bash
cd ../../beetleenv
./scripts/deprovision.sh aws vm     # ← the one that costs money

cd ../dockerenv
./dockerenv-down.sh

cd ../..                            # repo root
make down
```

**Delete the VM even if you skip the rest.** The namespace is kept by
`deprovision.sh`, so another `provision.sh aws vm` can follow straight on.
`./scripts/status.sh` in `beetleenv` shows what is still up.

---

## Running it as one script

[`migrate.sh`](migrate.sh) is steps 4 to 7 end to end — everything between a
source that exists and a VM that exists.

```bash
cp .env.example .env && chmod 600 .env    # once: change CP_USER, CP_PASS and HOST_IP
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
`CP_USER`, `CP_PASS` or `HOST_IP` is still `ChangeMe`.

| Variable | `.env.example` | |
|---|---|---|
| `HB_BASE` | `http://localhost:8081/honeybee` | where cm-honeybee answers |
| `CP_BASE` | `http://localhost:8085/centipede` | where cm-centipede answers |
| `CP_USER` / `CP_PASS` | `ChangeMe` | cm-centipede's BasicAuth — must be changed |
| `HOST_IP` | `ChangeMe` | the host machine the source container runs on — must be changed |
| `SRC_PORT` | `32210` | the source's SSH port |
| `SRC_USER` | `root` | the source's account |
| `SRC_KEY` | `../../dockerenv/ssh_keys/id_rsa` | its private key |
| `SRC_PATH` | `/testdata` | the directory to migrate |
| `NS_ID` | `cpbt01` | from `conn-info.sh aws --ids` |
| `INFRA_ID` | `cpbt-aws-infra` | " |
| `NODE_ID` | `vm-beetleenv-source-01-1` | " |
| `DST_PATH` | `/home/cb-user/testdata` | where it lands on the VM |

```bash
HOST_IP=172.24.78.163 SRC_PATH=/var/data ./migrate.sh
```
