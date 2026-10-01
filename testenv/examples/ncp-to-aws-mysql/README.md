# NCP Cloud DB for MySQL → AWS RDS

A worked example of migrating a database from **one cloud to another** — NCP's
managed MySQL to AWS RDS — **driven entirely over the REST APIs**. Every step
below is one or two `curl` calls, and [`migrate.sh`](migrate.sh) is the same
calls in a file.

Three systems each do one thing:

```
   NCP Cloud DB for MySQL          cm-honeybee          inspect the source
   testdb                      ──▶ :8081                what is in it?
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

**Both ends are managed services, and they are named in opposite ways.** The NCP
side is a host and an account this example supplies; the AWS side is a cm-beetle
reference with no host in sight. Both appear in the same plan request, a few
lines apart.

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

## 2. Create the NCP database and fill it

[`testenv/tofuenv`](../../tofuenv) creates the source with OpenTofu and loads it
with generated data. **This is the longest step in the example by a wide margin**
— and it is the only one with a stop in the middle that no API can do for you.

```bash
cd ../../tofuenv
cp .env.example .env && chmod 600 .env    # NCP keys, region, DB password

./scripts/up.sh                           # OpenBao + the tofu runner
./scripts/provision.sh ncp database       # ~30 minutes
```

> **Issue a public domain before going further.** A managed NCP DB is not
> reachable from outside its VPC until one is requested, and there is **no API
> action and no provider argument for it** — only the console:
>
> ```
> Database > Cloud DB for MySQL > select the DB server
>   > DB Management > Public domain > request
> ```

```bash
./scripts/ncp-db-domain.sh                          # refresh state, verify the domain
./scripts/gen-data.sh --provider ncp --target database --engine mysql
./scripts/conn-info.sh ncp database --reveal        # host, port, and the password
```

The last command prints what step 4 needs:

```
mysql_host          <the public domain>
mysql_port          3306
db_username         dbadmin
db_password         <from OpenBao, shown only with --reveal>
```

The database is **`testdb`** (`TF_VAR_ncp_db_name`), and what `gen-data.sh` loads
into it is deliberately more than tables:

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

The version also has to be **at least the source's**. The NCP side ships pinned
at `8.0.36` (`TF_VAR_ncp_mysql_version` in tofuenv's `.env`), so the pinned `8.4`
here is an upgrade and both ends are happy; asking for a target older than the
source is refused in step 5 — see the downgrade note there.

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
> The fixture carries four stored functions, so raising the retention turns this
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
        "name": "ncp-to-aws-mysql",
        "description": "NCP Cloud DB for MySQL source",
        "type": "db",
        "provider_name": "ncp"
      }'
```

Take **`.id`** from the response — that is the `sgId` below.

> **`"type":"db"` is not a choice.** A database inspect is refused for a source
> group of any other type.
>
> **`provider_name` is required too**, and it is validated against the same table
> a `minio` group uses — honeybee accepts `aws`, `alibaba`, `tencent`, `ncp`,
> `nhn`, `ibm`, `gcp`, `kt`, `azure`, `openstack` and `onprem`. Omitting it
> answers `provider_name is required for db source group.`

**There is no `region_name` here**, and that is the whole difference between a
`db` group and a `minio` one. An object storage group's region *builds the
endpoint*, so NCP needs it and honeybee refuses without it. A db connection
carries its own host and port, so nothing is derived from `provider_name` at all
— it records where the data lives, for whoever migrates it.

```bash
curl -s -X POST http://localhost:8081/honeybee/source_group/$SG_ID/connection_info \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "ncp-to-aws-mysql-src",
        "db_type": "mysql",
        "db_access_type": "direct",
        "db_host": "<the public domain from step 2>",
        "db_port": "3306",
        "db_username": "dbadmin",
        "db_password": "<NCP_DB_PASSWORD>",
        "db_tls_mode": "prefer"
      }'
```

Take **`.id`** — the `connId`.

**No agent is installed here.** honeybee connects to MySQL over the wire, which
is what `"db_access_type":"direct"` says.

**`db_tls_mode` is stated here and not in the on-premises example**, because this
connection crosses the public internet: a managed NCP DB is reached through its
public domain, and the credentials above go with it. `prefer` encrypts where the
server offers TLS and still connects where it does not. The other values are
`disable`, `require`, `verify-ca` and `verify-full`; the last two check against
the system trust store unless `db_tls_ca_pem` supplies a root.

**`db_name` is deliberately left out.** Empty means *every user database on this
server*, and honeybee hides the four MySQL ships with (`information_schema`,
`mysql`, `performance_schema`, `sys`) — so on this server it collects `testdb`
and nothing else. The filter in step 5 names it anyway, which is what a server
holding more than one would need.

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
            "serverVersion": "8.0.36",
            "database": "testdb",
            "characterSet": "utf8mb4",
            "collation": "utf8mb4_0900_ai_ci",
            "tables": [],
            "views": [],
            "functions": []
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
                "targetMapping": { "srcName": "testdb", "dstName": "testdb" },
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

**Two managed databases, named in opposite styles, side by side.** The source
arrives inside `source` as honeybee collected it, carrying a `honeybee` reference
that cm-centipede resolves back into the NCP host and the account it was given in
step 4. The destination is `beetleDb`: an instance that already exists, whose
host, port and admin account cm-centipede reads from cm-beetle. The password is
the exception — cm-beetle takes one when the instance is created and **never
hands it back**, so it travels here. `username` is deliberately absent: empty
means "the admin as cm-beetle reports it", and naming it as well only creates a
way for the two to disagree.

**cm-centipede creates the target database itself**, using that same password. A
managed instance's master account is granted rights on the databases the service
knows about and cannot create new ones, so the instance's owner is asked instead,
through cm-beetle. That call takes a name and a password and nothing else, so
**no character set can be given** and the database follows the instance defaults
— which is why a character-set difference is a warning in step 6 rather than a
failure.

**`tlsMode`** is `prefer` on both ends here. The other values are `disable`,
`require`, `verify-ca` and `verify-full`; the last two use the system trust store
only, and RDS signs with its own CA, so they need a bundle this example has no
way to supply.

**`dbmsFilter` selects and then subtracts.** `srcName` names the database to
migrate — the only one on this server, but a server holding several would be
narrowed here. Its rules are **exclude-only** — the database migrates in full
except for what they remove — and there are three kinds:

| Type | Removes |
|---|---|
| `table_column` | a whole table (no `columns`), or just the `columns` named within it |
| `row_exclude` | the rows of `table` for which the SQL predicate `match` is TRUE |
| `object_exclude` | the schema object of `kind` named `name` — `view`, `function`, `trigger`, … |

The rule above drops one of the four views. **Validation knows about all three
kinds**, so an excluded object is reported as excluded rather than as missing.

**`dstName` may differ from `srcName`** — the database is created under whatever
you name it. It is the same value here because that is the honest default: the
source is called `testdb` and there is no reason for the copy to be called
anything else.

> **The source account does not exist on the target, and that is fine.** Every
> view, function, procedure and trigger in `testdb` is stamped
> `DEFINER=dbadmin@%`, an account RDS has never heard of. cm-centipede strips the
> `DEFINER` clause as it reads each object, so they are created owned by the
> account doing the restore.

> **A downgrade is refused here.** If the target engine version is older than the
> source's, `POST /plans/target` answers 400 rather than building a plan that
> would fail later. Nothing in the API skips that check.

### Run it

```bash
curl -s -X POST -u default:default http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "ncp-to-aws-mysql",
        "description": "NCP Cloud DB for MySQL to AWS RDS",
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

> **This transfer crosses two clouds.** Every row is read out of NCP and written
> into AWS through cm-centipede, so it is bounded by whichever link is slower —
> and NCP charges for the egress.

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
        "itemPath": "testdb",
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

That warning is the one to expect here, and **crossing clouds makes it more
likely, not less**: the two services ship different server defaults, so the
collation the copy inherits is whatever RDS hands out rather than what NCP had.

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
        "itemPath": "testdb",
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

Both clouds, then the stack:

```bash
cd ../../beetleenv
./scripts/deprovision.sh aws database     # ← the one that costs money

cd ../tofuenv
./scripts/deprovision.sh ncp database     # ← so does this one, three times over
./scripts/down.sh

cd ../..                                  # repo root
make down
```

**Two providers now bill you, so clean up both.** The NCP side is the more
expensive half: `provision.sh ncp database` created MySQL, PostgreSQL and MongoDB
together, and all three have been running since step 2. `deprovision.sh ncp
database` destroys the module as a whole, and `tofu/ncp/network` goes with it
once neither the VM nor the database module is left.

In `beetleenv` the namespace is kept, so another `provision.sh aws database` can
follow straight on. `./scripts/status.sh` there shows what is still up.

---

## Running it as one script

[`migrate.sh`](migrate.sh) is steps 4 to 7 end to end — everything between a
source database that holds data and an instance that exists.

```bash
SRC_HOST=... SRC_DB_PASS=... DB_PASSWORD=... ./migrate.sh
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
| `SRC_PROVIDER` | `ncp` | the source cloud |
| `SRC_HOST` | **none — required** | the MySQL public domain, from `conn-info.sh ncp database` |
| `SRC_PORT` | `3306` | the tofu module pins it, so there is no `.env` key |
| `SRC_DB_USER` | `dbadmin` | `TF_VAR_ncp_db_username` |
| `SRC_DB_PASS` | **none — required** | `NCP_DB_PASSWORD`, kept in OpenBao |
| `SRC_DB` | `testdb` | `TF_VAR_ncp_db_name` |
| `NS_ID` | `cpbt01` | from `conn-info.sh aws --ids` |
| `RDBMS_ID` | `cpbt-aws-db-mysql` | " |
| `DB_PASSWORD` | **none — required** | the RDS instance's master password |
| `DST_DB` | `testdb` | what to call the database on the target |

**Three values have no default on purpose.** Unset, the script stops on its first
lines rather than several steps later. `SRC_HOST` cannot have one — the public
domain does not exist until it is issued — and the two passwords are secrets
tofuenv and beetleenv keep out of their `.env` files.

```bash
SRC_HOST=... SRC_DB_PASS=... DB_PASSWORD=... DST_DB=testdb ./migrate.sh
```
