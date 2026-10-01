# On-premises MySQL → AWS RDS

A worked example of migrating a database from an on-premises MySQL onto a managed
RDS instance in AWS, **driven entirely over the REST APIs**. Every step below is
one or two `curl` calls, and [`migrate.sh`](migrate.sh) is the same calls in a
file.

Three systems each do one thing:

```
   on-premises MySQL               cm-honeybee          inspect the source
   shop_db, hr_db              ──▶ :8081                what is in them?
        │
        │                          cm-centipede         plan, migrate, validate
        └────────────────────── ──▶ :8085               move it, then check it
                                         │
   AWS RDS for MySQL                     │              cm-beetle
   cpbt-aws-db-mysql       ◀─────────────┘              :8056  provisions the instance
```

cm-honeybee reads the source. cm-centipede moves the data and verifies it.
cm-beetle creates the instance it lands on — and cm-centipede reaches that
instance through cm-beetle, so it is never told the host or the admin account.

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

> **cm-centipede has to reach the same cm-beetle.** The target is named as a
> cm-beetle reference, so cm-centipede calls cm-beetle itself — its
> `centipede.beetle.endpoint` (in `conf/cm-centipede.yaml`, or
> `CENTIPEDE_BEETLE_ENDPOINT`) must point at the beetle that provisions the
> instance in step 3. A mismatch fails at `POST /plans/target`.

---

## 2. Start the on-premises source

[`testenv/dockerenv`](../../dockerenv) provides the source: a container running
MySQL with two seeded databases. It stands in for an on-premises server, and
nothing about the API calls below would change for a real one.

```bash
cd ../../dockerenv
./dockerenv-up.sh
```

The container this example uses is **`mysql-source`**:

| | |
|---|---|
| MySQL | `<host>:33406` |
| accounts | `root` / `testpass123` · `centipede` / `centipede_pass` |
| databases | `shop_db` · `hr_db` |

This example migrates **`shop_db`**, which is deliberately more than tables:

```
10 tables   4 views   4 functions   5 procedures   5 triggers   3 events
```

The schema objects are the point. Rows travel easily; a view whose definer does
not exist on the target, or a function the target refuses to create, is what
fails quietly — and validation compares all of it.

---

## 3. Create the AWS RDS instance

[`testenv/beetleenv`](../../beetleenv) creates it through the cm-beetle API.

```bash
cd ../beetleenv
cp .env.example .env && chmod 600 .env    # region, zones, and the DB password

./scripts/up.sh                           # check the stack, create the namespace
./scripts/catalog.sh aws rdbms            # what MySQL versions this region offers
./scripts/provision.sh aws database --engine mysql
./scripts/conn-info.sh aws --ids
```

**The MySQL version is a `.env` key, not a flag.** `provision.sh` takes only
`--engine`; the version travels in the recommendation request cm-beetle answers:

```dotenv
BEETLEENV_AWS_DB_SRC_ENGINE_VERSION_MYSQL=8.4     # ships pinned
```

Empty it and cm-beetle picks the newest version the CSP supports, which makes a
run today and a run next month build different things. Write the value exactly as
`./scripts/catalog.sh aws rdbms` lists it — a version matching nothing is
answered with a **warning and a version of beetle's choosing, not an error**. The
CSPs disagree on the format: AWS reports `major.minor` (`8.0`, `8.4`), NCP the
full string.

The version also has to be **at least the source's**. `mysql-source` runs 8.0 by
default (`MYSQL_VERSION` in `testenv/dockerenv/versions.env`), so 8.0 and 8.4
both work; asking for a target older than the source is refused in step 5 — see
the downgrade note there.

**This one takes 5 to 30 minutes** and costs real money while it is up. It is
also the only step here with a secret of its own:
`BEETLEENV_AWS_DB_PASSWORD` in `.env` becomes the instance's master password, and
step 5 needs the same value again.

The last command prints the two identifiers the migration needs:

```
nsId        cpbt01
rdbmsId     cpbt-aws-db-mysql
```

> **Leave `BEETLEENV_AWS_DB_BACKUP_RETENTION_DAYS` at `0`.** On RDS, automated
> backups are what turn binary logging on, and with the binary log on a managed
> instance refuses to create a stored function:
>
> ```
> Error 1419 (HY000): You do not have the SUPER privilege and binary logging is
> enabled (you *might* want to use the less safe log_bin_trust_function_creators
> variable)
> ```
>
> `shop_db` carries four stored functions, so raising the retention turns this
> example red at the restore stage. The functions themselves are fine — they
> declare `DETERMINISTIC` or `READS SQL DATA`, which is the first of MySQL's two
> gates; the second wants `SUPER`, and a managed service never grants it.

---

## 4. Inspect the source with cm-honeybee

> **Steps 4 to 7 are the migration, and `migrate.sh` runs exactly these four.**
> Follow them by hand to see each call on its own, or run the script and read
> these sections as its commentary.

A **SourceGroup** holds connections; a **ConnectionInfo** is one source server.
Both are needed before anything can be read.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-mysql",
        "description": "on-premises MySQL source",
        "type": "db",
        "provider_name": "onprem"
      }'
```

Take **`.id`** from the response — that is the `sgId` below.

> **`"type":"db"` is not a choice.** A database inspect is refused for a source
> group of any other type.
>
> **`provider_name` is required too**, and `onprem` is a database the operator
> runs themselves. Nothing is derived from it here — a db connection carries its
> own host and port, so there is no region or endpoint rule to apply — but
> honeybee asks for it under the same rule a `minio` group follows, and the value
> records where the data lives for whoever migrates it. Omitting it answers
> `provider_name is required for db source group.`

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group/$SG_ID/connection_info \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-mysql-src",
        "db_type": "mysql",
        "db_access_type": "direct",
        "db_host": "172.24.78.163",
        "db_port": "33406",
        "db_username": "centipede",
        "db_password": "centipede_pass"
      }'
```

Take **`.id`** — the `connId`.

**No agent is installed here.** honeybee connects to MySQL over the wire, which
is what `"db_access_type":"direct"` says.

**`db_name` is deliberately left out.** Empty means *every user database on this
server*, so honeybee collects `shop_db` **and** `hr_db`, and the filter in step 5
is what narrows the migration to one of them.

> **`db_host` is the address of the host machine the source container runs on.**
> MySQL is reached through the port it publishes — `33406` above — so the address
> has to be the host's, not the container's, and not `127.0.0.1`.

Now read the databases:

```bash
curl -s -X POST \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/import/db \
  -H 'Content-Type: application/json' \
  -d '{
        "metric": {
          "row_count_exact": true,
          "columns": true,
          "indexes": true,
          "views": true,
          "foreign_keys": true,
          "functions": true,
          "procedures": true,
          "triggers": true,
          "events": true
        }
      }'
```

The server version, the database size and the table list are always collected;
everything in `metric` is opt-in and defaults to off. Every option MySQL has is
asked for here, because **the schema objects are what validation compares later**
— leaving them out makes the verdict weaker rather than the run faster.

The remaining fields of `metric` belong to other engines —
`materialized_views`, `sequences`, `types`, `extensions` and `rules` to
PostgreSQL, `validators` and `sample_size` to MongoDB — and honeybee drops
whatever the connection's `db_type` has no equivalent for.

Then fetch the result in the shape cm-centipede takes:

```bash
curl -s \
  http://localhost:8081/honeybee/source_group/$SG_ID/connection_info/$CONN_ID/db/refined
```

```json
{
  "sourceDataMigrationModel": {
    "databases": [
      {
        "connection": {
          "source": "honeybee",
          "honeybee": {
            "connectionId": "..."
          }
        },
        "databases": [
          {
            "serverVersion": "8.0.43",
            "database": "shop_db",
            "characterSet": "utf8mb4",
            "collation": "utf8mb4_0900_ai_ci",
            "tables": [],
            "views": [],
            "functions": []
          },
          {
            "database": "hr_db"
          }
        ]
      }
    ]
  }
}
```

**Keep this response whole.** It is already a `SourceDataMigrationModel`, which is
exactly what the plan call takes as its `source` — nothing is assembled by hand.
The nesting reads oddly at first: the outer `databases` is one entry per
*connection*, and the inner one is the databases that connection found.

---

## 5. Migrate with cm-centipede

> Every cm-centipede call needs BasicAuth — `-u default:default` by default.

### Build the plan

```bash
curl -s -X POST -u default:default http://localhost:8085/centipede/plans/target \
  -H 'Content-Type: application/json' \
  -d '{
        "source": <the /db/refined response, unchanged>,
        "plans": [{
          "srcConnection": {
            "source": "honeybee",
            "honeybee": { "connectionId": "<the connection id from step 2>" }
          },
          "dstConnection": {
            "source": "beetleDb",
            "beetleDb": {
              "nsId": "cpbt01",
              "rdbmsId": "cpbt-aws-db-mysql",
              "password": "<the instance master password>",
              "tlsMode": "prefer"
            }
          },
          "dbmsFilter": {
            "databases": [
              {
                "targetMapping": { "srcName": "shop_db", "dstName": "shop_db" },
                "rules": [
                  { "type": "object_exclude", "kind": "view", "name": "v_low_stock_alert" }
                ]
              }
            ]
          }
        }]
      }'
```

Take **`.data`** — the plan — and pass it to the next call unchanged.

**`plans` is an array: one entry per (source entry, destination).** This example
has one connection going to one instance, so it holds a single entry. A source
group with several connections would send several — that is what `srcConnection`
is for, saying which entry of `source` this destination is meant for. Every entry
of `source` has to be named by exactly one of them, and two entries may not write
to the same place.

**`dstConnection` is a reference plus one secret.** `beetleDb` names an instance
that already exists, and cm-centipede reads its host, port and admin account from
cm-beetle. The password is the exception: cm-beetle takes one when the instance
is created and **never hands it back**, so it travels here. `username` is
deliberately absent — empty means "the admin as cm-beetle reports it", and naming
it as well only creates a way for the two to disagree.

**cm-centipede creates the target database itself**, using that same password. A
managed instance's master account is granted rights on the databases the service
knows about and cannot create new ones, so the instance's owner is asked instead,
through cm-beetle. That call takes a name and a password and nothing else, so
**no character set can be given** and the database follows the instance defaults
— which is why a character-set difference is a warning in step 6 rather than a
failure.

**`tlsMode`** is `prefer` here: encrypt where the instance offers TLS, and still
connect where it does not. The other values are `disable`, `require`,
`verify-ca` and `verify-full`; the last two use the system trust store only, and
RDS signs with its own CA, so they need a bundle this example has no way to
supply.

**`dbmsFilter` is a selection as much as a rule set.** Naming `shop_db` migrates
that database and leaves `hr_db` behind. Its rules are **exclude-only** — the
database migrates in full except for what they remove — and there are three
kinds:

| Type | Removes |
|---|---|
| `table_column` | a whole table (no `columns`), or just the `columns` named within it |
| `row_exclude` | the rows of `table` for which the SQL predicate `match` is TRUE |
| `object_exclude` | the schema object of `kind` named `name` — `view`, `function`, `trigger`, … |

The rule above drops one of the four views. **Validation knows about all three
kinds**, so an excluded object is reported as excluded rather than as missing.

> **A downgrade is refused here.** If the target engine version is older than the
> source's, `POST /plans/target` answers 400 rather than building a plan that
> would fail later. Nothing in the API skips that check.

### Run it

```bash
curl -s -X POST -u default:default http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "onprem-to-aws-mysql",
        "description": "on-premises MySQL to AWS RDS",
        "plan": <the plan from above>,
        "dbmsOnFailure": "cleanup"
      }'
```

Take **`.data.id`** — the `migrationId`, the handle for everything that follows.

**`dbmsOnFailure` is DBMS-only**, and it decides what happens to a target
database a failed run left behind: `cleanup` drops it, `keep` leaves it to be
inspected. A filesystem or object storage transfer has no equivalent, because it
leaves whatever it had already written either way.

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
    "transferredBytes": 0
  }
}
```

`status` settles on `completed`, `failed` or `cancelled`.

---

## 6. Validate

This is the verdict, and it is cm-centipede's rather than something you assemble.
It re-reads **both databases** and compares three things.

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
        "itemPath": "shop_db",
        "status": "warning",
        "message": "collation differs: src=utf8mb4_0900_ai_ci dst=utf8mb4_general_ci"
      }
    ]
  }
}
```

| Compared | Reported as |
|---|---|
| **Row counts**, exact, per table | `failed` on a mismatch, a missing table, or an unexpected one |
| **Schema objects**, twelve kinds | `failed` when one is missing or unexpected |
| **Character set and collation** | `warning` — the target database was created without a character set, so a difference is expected |

The filter changes what these mean, and validation accounts for it:

- an `object_exclude`d view is not a missing object;
- a table excluded whole is counted into one `skipped` line, not reported missing;
- a table under a `row_exclude` rule holds fewer rows on purpose, and **how many
  fewer is only knowable by running the predicate** — which validation does not
  do — so a lower count is reported as *not comparable* rather than as a loss. A
  count *higher* than the source still fails: no exclude rule explains that.

**Findings are budgeted.** At most 20 `failed` items are listed per database,
followed by a line saying how many there were in total, and warnings of one kind
collapse into a single line with a few example names. A migration that failed
wholesale reports a readable sample instead of one line per table.

---

## 7. Read the migration log

Per-item detail, including anything a rollback did.

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
        "itemPath": "shop_db",
        "durationMs": 41230
      }
    ]
  }
}
```

**It is paginated.** `pageSize` caps at 100 and defaults to 20, and `.data.total`
is the only sign that anything was left out. A DBMS migration logs one entry per
database, so one page is plenty here.

**Read it after the verdict, not before.** By then the entries also carry what
any rollback did, which is the part worth reading when a run failed.

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
./scripts/deprovision.sh aws database     # ← the one that costs money

cd ../dockerenv
./dockerenv-down.sh

cd ../..                                  # repo root
make down
```

**Delete the instance even if you skip the rest.** The namespace is kept by
`deprovision.sh`, so another `provision.sh aws database` can follow straight on.
`./scripts/status.sh` in `beetleenv` shows what is still up.

---

## Running it as one script

[`migrate.sh`](migrate.sh) is steps 4 to 7 end to end — everything between a
source that exists and an instance that exists.

```bash
DB_PASSWORD=... ./migrate.sh
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
| `SRC_PORT` | `33406` | MySQL's port on that host |
| `SRC_DB_USER` | `centipede` | the source account |
| `SRC_DB_PASS` | `centipede_pass` | its password |
| `SRC_DB` | `shop_db` | the database to migrate |
| `NS_ID` | `cpbt01` | from `conn-info.sh aws --ids` |
| `RDBMS_ID` | `cpbt-aws-db-mysql` | " |
| `DB_PASSWORD` | **none — required** | the RDS instance's master password |
| `DST_DB` | `shop_db` | what to call the database on the target |

**`DB_PASSWORD` has no default on purpose.** Unset, the script stops on its first
line rather than at the plan call several steps later. It is the same value as
`BEETLEENV_AWS_DB_PASSWORD` in `testenv/beetleenv/.env`.

```bash
DB_PASSWORD=... HOST_IP=172.24.78.163 SRC_DB=hr_db DST_DB=hr_db ./migrate.sh
```
