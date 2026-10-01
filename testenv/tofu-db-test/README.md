# Managed-DB Version Matrix — cm-honeybee + cm-centipede over OpenTofu

Runs a migration for **every combination of a source engine version and a target
engine version**, where the source is a container this folder builds and the
target is a **managed cloud database provisioned with OpenTofu**. The outcome is
reported as a matrix.

```
  src \ dst   │ 8.0.46      8.4.11
  ────────────┼────────────────────────
  8.0         │ PASS 3m12s  PASS 3m40s
  8.4         │ BLOCK 4s    PASS 3m18s
```

The migration itself is **cm-centipede**, driven over its REST API, with
**cm-honeybee** collecting the source. This folder provisions, prepares and
judges; it does not migrate anything itself.

> **cm-honeybee and cm-centipede must already be running.** This folder starts
> neither, and step 1 of every run checks that both answer before a single
> managed instance is created. `.env` points at them with `HB_BASE` and
> `CP_BASE`.

### Starting that stack — `make up`, `make init`, `make down`

The backing servers come up in one command, from the **repo root**. The Compose
stack in [`deployments/docker-compose`](../../deployments) runs **cm-honeybee on
8081** — exactly what the default `HB_BASE` in `.env.example` points at, so a
stack started this way needs no URL change here.

```bash
cd ../..                # repo root
cp deployments/docker-compose/.env.example deployments/docker-compose/.env
                        # fill in the API credentials, once

make up                 # start every service (beetle, tumblebug, spider, ...)
make init               # register the CSP credentials — run this once
make down               # stop and remove the containers
```

**`make init` is a one-time step.** It is the "CSP connections registered on
tumblebug" half of the requirement above: it decrypts
`~/.cloud-barista/credentials.yaml.enc` — asking for its password once — and
registers those credentials into OpenBao and into cb-tumblebug, which is what
creates the `<csp>-<region>` connections beetle resolves — one per CSP the
credentials file holds. Both
stores keep them in `deployments/docker-compose/data/`, so they survive a
`make down` and every later `make up`. Re-run it only after `make clean-all`,
which deletes that data on purpose.

`make up` runs in the **foreground** — it builds, starts, and then streams the
logs, so run this matrix from a second terminal and stop the stack with `Ctrl+C`
followed by `make down`. It also unseals OpenBao on the way up; `make unseal`
does that on its own if the containers were restarted some other way. `make
status` lists what is running, `make logs` follows the logs. See
[`deployments/README.md`](../../deployments/README.md) for the rest.

---

## Support matrix

The source is built from the vendor's apt repository, so all four engines work
there. The **target** is limited to what the CSP offers as a managed service —
self-hosted targets are out of scope.

| Engine | AWS target | NCP target |
|---|---|---|
| mysql | RDS | Cloud DB for MySQL |
| mariadb | RDS | **none** — NCP has no managed MariaDB |
| postgresql | RDS | Cloud DB for PostgreSQL |
| mongodb | **none** — see below | Cloud DB for MongoDB (STAND_ALONE) |

**Why no MongoDB on AWS.** The only managed option is DocumentDB, which exposes
no public endpoint; a client outside the VPC cannot reach it without a tunnel or
a VPN. Running MongoDB on EC2 would work but is self-hosting, which this tool
does not do. Use NCP for mongodb columns.

**Why no MariaDB on NCP.** NCP offers MariaDB only as something you install
yourself on a server. Use AWS for mariadb columns.

An impossible combination is refused by `assert_engine_known`
(`scripts/lib/tofu.sh`) **before anything is created**, with the reason and what
to do instead.

### Source versions come from apt, not Docker Hub

The source image installs the server from the vendor's own apt repository for
Ubuntu 22.04, so the row axis is what that repository offers. A version outside
these lists fails during `docker build`, before a container ever starts.

| Engine | Source versions available |
|---|---|
| mysql | 8.0 · 8.4 |
| mariadb | 10.6 · 10.11 · 11.4 · 11.8 |
| postgresql | 13 · 14 · 15 · 16 · 17 |
| mongodb | 6.0 · 7.0 · 8.0 |

---

## Quick start

```bash
cd testenv/tofu-db-test

cp .env.example .env && chmod 600 .env
# fill in the CSP keys and the DB password, then:

./scripts/up.sh                 # OpenBao + credential registration + tofu runner

./scripts/aws-db-matrix.sh --engines mysql --src-versions "8.0" --dst-versions "8.0.46"
```

Start with a single cell. One cell exercises the whole path — image build,
provisioning, target database creation, collection, migration, validation,
teardown — for the price of one instance. Widen the version lists once it goes
green.

```bash
./scripts/aws-db-matrix.sh                    # everything in AWS_ENGINES
./scripts/ncp-db-matrix.sh --engines postgresql
./scripts/aws-db-matrix.sh --only 8.4:8.0     # one cell of an existing matrix
./scripts/aws-db-matrix.sh --tls-mode require # demand TLS instead of preferring it
./scripts/aws-db-matrix.sh --cleanup          # reclaim what an interrupted run left
./scripts/down.sh
```

`chmod 600` is enforced, not advised: `up.sh` and `register-creds.sh` refuse to
read a `.env` anyone else can. See [Credentials](#credentials).

---

## How it fits together

```
 Step 0  Prerequisites      docker, jq, curl + a CSP access key
            |               + cm-honeybee and cm-centipede running
 Step 1  .env               cp from .example, chmod 600, fill in keys
            |
 Step 2  ./scripts/up.sh    OpenBao up -> init or unseal -> store credentials
            |                -> blank the keys in .env -> tofu runner up
            |
 Step 3  ./scripts/<csp>-db-versions.sh      (optional) what does this region offer
            |
 Step 4  ./scripts/<csp>-db-matrix.sh        the matrix itself
            |    ├ pre-flight    honeybee, centipede, stack, credentials, versions
            |    ├ network       NCP only: VPC + PUBLIC subnet
            |    └ per column    provision -> [NCP: console step] -> probe -> cells -> destroy
            |
 Step 5  logs/<csp>-db-matrix.log
         logs/<csp>-db-matrix-api.log
         logs/<csp>-db-matrix-result.json
            |
 Step 6  ./scripts/down.sh  stop the stack (CSP resources are already gone)
```

---

## The source containers

A source is **a machine, not a process**. `src/Dockerfile.<engine>` builds
Ubuntu 22.04 with systemd as PID 1, so `systemctl` works, sshd is a unit, and the
database is a service that starts at boot — which is what an on-premises source
is, and what anything deployed onto it later expects to find.

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

**Readiness is one signal.** `matrix-init.service` is `Type=oneshot` with
`RemainAfterExit=yes`, so the unit stays `active` after its script returns. That
makes `systemctl is-active matrix-init.service` mean "the server is up, the
account exists and the seed is loaded" — a single question instead of three.

**Settings reach the container as a file, not as environment variables.** systemd
does not hand its own environment to the units it starts, so anything passed with
`docker run -e` would be invisible to the init script. `lib/source.sh` writes a
per-cell env file on the host and bind-mounts it at `/opt/matrix/matrix.env`; the
image ships `defaults.env` so it also runs on its own.

**The seed** is deliberately small and shaped to exercise structure rather than
volume: 4 tables with 3 foreign keys, a view, a function, a procedure, a trigger,
an event where the engine has one, and rows containing Korean, Japanese and emoji
so a charset regression shows up in the data.

---

## Credentials

You type keys into `.env`; `up.sh` moves them into OpenBao and blanks them in the
file. The tofu modules then read them through the `vault` provider, so no
plaintext key stays on disk.

```
.env (600)
      │  ./scripts/up.sh → init or unseal → register-creds.sh
      ▼
  OpenBao KV v2                              the keys are blanked afterwards
    secret/csp/aws   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
    secret/csp/ncp   NCP_ACCESS_KEY / NCP_SECRET_KEY
    secret/db/aws    AWS_DB_PASSWORD
    secret/db/ncp    NCP_DB_PASSWORD
      │
      ├──▶ tofu provider "vault"          provisioning
      └──▶ vault_db_password() in tofu.sh target databases, probe, centipede request
```

`register-creds.sh` validates the NCP password against the rules the ncloud
provider enforces **before** anything is provisioned. Without that check, NCP
reports the violation as a failed creation about 30 minutes later.

### What this protects, and what it does not

- **`VAULT_TOKEN` stays in `.env` permanently.** It is the OpenBao root token;
  anything that can read the file can read every stored credential. The file mode
  is the whole protection, which is why 600 is enforced.
- **`terraform.tfstate` holds the DB master password in plaintext.** This is
  inherent to Terraform/OpenTofu state. All state files are git-ignored.
- **The target password travels in the centipede request body**, and in
  `MODE=ssh` the SSH private key travels in the honeybee request body — honeybee
  stores the PEM itself, not a path. Both are masked in `logs/*-api.log`, but
  they are on the wire; use this against test credentials only.

---

## OpenTofu modules

| Module | Creates | Applied |
|---|---|---|
| `aws/rdbms` | one `aws_db_instance` + subnet group + SG + parameter group | per column |
| `ncp/network` | VPC + PUBLIC subnet | once per run |
| `ncp/rdbms` | one of `ncloud_mysql` / `ncloud_postgresql` / `ncloud_mongodb` + ACG rule | per column |
| `my-target` | `mysql_database` | per **AWS** MySQL/MariaDB cell |
| `pg-target` | `postgresql_database` + optional `postgresql_grant` | per **AWS** PostgreSQL cell |
| `ncp-target` | `ncloud_mysql_databases` / `ncloud_postgresql_databases` | per **NCP** cell |
| `aws/versions`, `ncp/versions` | nothing — data sources only | on demand |

**Why a cell's databases are made two different ways.** On AWS the master
account is a real administrator, so `my-target` and `pg-target` connect to the
instance and run `CREATE DATABASE`. On NCP it is not: its managed databases are
administered through the CSP, the master account holds rights on the databases
the service knows about, and creating one over the wire is refused —
`Error 1044 (42000): Access denied for user 'dbadmin'@'%' to database
'cptfm_probe'`. So NCP cells go through the CSP API instead.

The API also takes an **owner** at creation time, which a wire-protocol
`CREATE DATABASE` would leave as whoever connected. That is worth having, but it
does not on its own settle the schema-permission problem: on PostgreSQL 13 / 14
`public` is owned by the role `postgres`, and owning a database gives no rights
inside a schema owned by someone else.

Variables reach a module through `matrix.auto.tfvars.json`, written by
`tofu_vars()` before each apply. Passing `-var` through `docker exec` → `bash -c`
→ `tofu` gives quoting three chances to break, and a file also records what was
actually applied, which `destroy` then reuses unchanged.

### A column is a tofu workspace

```
tofu workspace select -or-create mysql-8-0
tofu apply    -auto-approve       # 5-30 min
  ... probe, then cells ...
tofu destroy  -auto-approve
tofu workspace delete mysql-8-0
```

State per column means an interrupted run leaves a record of exactly what exists.
`--cleanup` walks the workspaces and destroys each one; nothing has to be
recognised by name in a console.

---

## One column, step by step

1. **Skip if empty.** If `--only` filtered out every cell of this column, no
   instance is created. Standing up a managed database for a column nobody runs
   wastes tens of minutes and real money.
2. **`create_rdbms`** — select the workspace, write the tfvars, `tofu apply`.
3. **NCP only: the console step.** `csp_after_instance_ready` in
   `ncp-db-matrix.sh` stops and waits for Enter. See
   [Known limitations](#known-limitations).
4. **`wait_target_reachable`** — TCP connect to the endpoint until it answers.
   Without this, every cell in the column fails one at a time with the same
   connection error.
5. **PostgreSQL only: the privilege query.** Asks the instance whether the master
   user can already create in `public` and whether it could grant that right.
6. **The creation probe.** Creates a throwaway database with the very module the
   cells use, and on PostgreSQL also creates and drops a table inside it. Failing
   it marks the whole column SKIP, with the error the CSP actually returned.
7. **Cells**, in source-version order.
8. **`release_column`** — `tofu destroy`, unless `--keep-instance`.

---

## One cell, step by step

| Step | What happens |
|---|---|
| 1 | Build the source image if this version has none, start the container, wait for `matrix-init.service` |
| 2 | Create the empty target database — `my-target` / `pg-target` on AWS, `ncp-target` on NCP |
| 3 | Register the source with cm-honeybee and collect it (`import/db`) |
| 4 | `POST /plans/target` — **a downgrade is refused here, and the cell is BLOCK** |
| 5 | `POST /migration`, then poll until it settles |
| 6 | `POST /migration/{id}/validation`, then poll — this is the verdict |
| 7 | `GET /migration/{id}/logs` — the per-item migration log |
| 8 | Drop the target database, remove the source container, record the result |

**Step 2 comes before step 4 for a reason.** centipede creates a target database
only for `providerName=onprem`, and a managed target is `aws` or `ncp`; that same
check runs at plan time, so a missing database fails the plan, not the migration.

**Step 7 reads the log after the verdict, not before it.** By then the entries
also carry what any rollback did, which is the part worth reading when a cell
failed. Each entry prints its status, item path, duration and size, and for a
failure the object that broke, the error and the rollback outcome; a count per
status leads. When the migration itself never finishes, step 6 is never reached,
so the log is read at that failure instead.

`GET /migration/{id}/logs` is **paginated** — 20 entries by default, 100 at most
— and reports the real count in `total`. The matrix walks to the end of the
pages rather than reading one, stopping at `CP_LOG_MAX_PAGES` (50) and saying so
if a cell were ever wide enough to reach it.

**Validation is centipede's, not this folder's.** It compares exact row counts per
table or collection, twelve kinds of schema object (views, functions, procedures,
triggers, events, foreign keys, sequences, types, extensions, rules, materialized
views, validators) and character sets. A charset difference is reported as a
**warning**, because a managed target follows its own server defaults — the same
judgement this matrix made when it compared snapshots itself.

---

## Known limitations

These are the parts worth reading before changing anything.

### 1. Managed instances that refuse plaintext connections

AWS turns TLS on by default for some engine versions: **MariaDB 11.8 and later**
set `require_secure_transport=ON`, and RDS PostgreSQL sets `rds.force_ssl=1`. A
client that can only speak plaintext cannot reach those instances at all.

`TARGET_TLS_MODE` sets transport security per connection, with the five
libpq-style modes:

| Mode | Encrypt | Verify chain | Verify hostname | Server offers no TLS |
| ---- | :-----: | :----------: | :-------------: | -------------------- |
| `disable` | no | — | — | plaintext |
| `prefer` **(matrix default)** | try | no | no | plaintext |
| `require` | yes | no | no | error |
| `verify-ca` | yes | yes | no | error |
| `verify-full` | yes | yes | yes | error |

It governs the migration — the `db.tlsMode` centipede carries into transx-ex —
and nothing else. **Setting up a cell does not use it.** Creating the target
database (`my-target`, `pg-target`) and the PostgreSQL privilege query connect on
the best transport the server offers: TLS when it is available, plaintext when it
is not.

That separation was learned the hard way. The setup connections used to honour
`TARGET_TLS_MODE` too, on the argument that one value everywhere means a column
cannot migrate successfully and then fail its own preparation. But NCP Cloud DB
for MySQL offers **no TLS at all**, so at the default `prefer` the probe could
not connect and every NCP MySQL column SKIPped — while transx-ex, which
implements a real `prefer`, would have migrated into it over plaintext quite
happily. Setup failing on the experiment's own condition is a false negative, and
it hides the result the run exists to produce.

**Why `prefer` is the default.** A managed instance offers TLS whether or not it
demands it, so `prefer` encrypts in practice and still connects to an instance
that allows plaintext. One value runs every column, without touching what the CSP
configured.

Three limits worth knowing:

- **MongoDB has no `prefer`** — a client either negotiates TLS or it does not, and
  transx-ex rejects it. Rather than refusing a whole run because one engine in it
  is mongodb, the matrix runs **mongodb cells at `disable`** and leaves the other
  engines on the configured mode. The effective mode appears per engine in the
  result JSON (`engines[].tlsMode`).
- **Creating the target database ignores the mode entirely** — see above. The
  MySQL provider could not express `prefer` in any case: it validates `tls`
  against `false`, `skip-verify` and `true` and rejects anything else at apply
  time. `skip-verify` plus `allowFallbackToPlaintext` in `conn_params` gives the
  driver the behaviour libpq calls `prefer`, which is what `my-target` uses.
  `pg-target` just says `sslmode = "prefer"`; libpq implements it itself.
- **`verify-ca` and `verify-full` use the system trust store only.** RDS and NCP
  sign with their own CAs, so verification fails without their bundle, and this
  matrix has no path to supply one.

#### The parameter group, an escape hatch

`AWS_TARGET_SECURE_TRANSPORT=off` attaches a parameter group that allows
plaintext. It is not the default, because a PASS obtained with the CSP's own
setting switched off does not answer the question the matrix is asked. Use it to
see what happens when TLS is genuinely unavailable. The mode is recorded in the
result JSON.

### 2. NCP Cloud DB for PostgreSQL closes schema `public`

**This is a trait of one managed service, not of PostgreSQL.** A stock PostgreSQL
13 / 14 leaves `public` as `{postgres=UC/postgres,=UC/postgres}` — the second entry
grants USAGE and CREATE to PUBLIC — and **AWS RDS leaves it that way**, which is
why RDS columns need nothing here. **NCP Cloud DB for PostgreSQL** revokes it,
leaving `{postgres=UC/postgres}`, so its master account cannot create a table in a
database it otherwise administers.

That is a defensible hardening choice on NCP's part, and nothing in this matrix can
undo it: the account is not a superuser, does not own schema `public`, and is not a
member of the role that does. Two things that look like they should get around it
do not:

- **Owning the database is not owning its schema.** `ncp-target` creates the
  database through the CSP API with `owner` set to the master account, which is
  the right thing to do for other reasons — but on PostgreSQL 13 / 14 `public`
  is owned by the login role `postgres`, and a database's owner has no rights in a
  schema owned by someone else. The symptom is not a permission message but
  `ERROR: no schema has been selected to create in`: an unqualified `CREATE TABLE`
  looks for the first schema in `search_path` it may create in, and finds none.
- **Granting needs the schema's owner.** `GRANT ... ON SCHEMA public` only
  succeeds for the schema's owner or a member of it. Where that fails there is
  nothing to grant with.

**PostgreSQL 15 changed the ground, so this is a 13 / 14 problem.** From 15,
`public` is owned by `pg_database_owner` rather than by a login role, so a
database's owner *is* effectively its schema's owner and can create in it. NCP
Cloud DB for PostgreSQL 15 columns therefore migrate into `public` with no special
handling — the version, not the CSP, is what decides.

### 3. NCP managed databases need a public domain, issued by hand

NCP answers with a private domain until a public domain is requested, and the
request exists only in the console — no API action, no provider argument.

So `ncp-db-matrix.sh` **always stops and waits for Enter** after an instance is
ready, once per target version:

```
NCP console > Database > Cloud DB for <engine> > select the DB server
  > DB Management > Public Domain Management > Request
```

It does not try to decide whether the wait is necessary. Guessing is fine when
the guess is right and unrecoverable when it is wrong: the run would sail past
the one moment a human could act, and every cell in the column would then fail
trying to reach a private domain. The wait ignores `NO_PAUSE` for the same
reason; with no tty at all (CI) it warns and moves on.

After Enter, `rdbms_info` runs `tofu apply -refresh-only` so the console-issued
domain lands in state and in the outputs. If it is still absent, the column stops
immediately rather than spending three minutes waiting on an address that does
not exist.

---

## Verdicts and results

| Verdict | Meaning |
|---|---|
| **PASS** | Migration `completed` and validation `passed` |
| **BLOCK** | A higher-to-lower version pair that `POST /plans/target` refused with a downgrade error — the expected, correct outcome |
| **FAIL** | Anything else |
| **SKIP** | Filtered out by `--only`/`--engines`, or the column could not be prepared (instance failed, endpoint unreachable, creation probe failed) |

There is **no `--skip-version-check`**: the centipede API has no way to skip the
version check, so a downgrade cannot be attempted for real from here.

Outputs land in `logs/`:

- `<csp>-db-matrix.log` — the full run, with colour stripped
- `<csp>-db-matrix-api.log` — every honeybee and centipede call as a runnable
  `curl` line plus the response, with credentials masked
- `<csp>-db-matrix-result.json`:

```json
{
  "csp": "aws",
  "namePrefix": "cptfm",
  "provisioner": "opentofu",
  "migrator": "cm-centipede",
  "collector": "cm-honeybee",
  "srcMode": "direct",
  "dstMode": "direct",
  "targetTLSMode": "prefer",
  "dbmsOnFailure": "cleanup",
  "secureTransport": "csp-default",
  "grantedPublicSchema": null,
  "pgTargetSchema": null,
  "finishedAt": "2026-09-08 18:20:11",
  "engines": [
    { "engine": "mysql", "tlsMode": "prefer",
      "cells": [
        { "srcVersion": "8.0", "dstVersion": "8.4.11", "status": "PASS",
          "elapsedSec": 192, "srcServerVersion": "8.0.43",
          "migrationId": "01J...", "validationStatus": "passed",
          "detail": "validation passed (source 8.0.43)" }
      ] }
  ]
}
```

`targetTLSMode`, `secureTransport`, `grantedPublicSchema` and `pgTargetSchema`
record the conditions the run needed, so results stay comparable later — a PASS
won into a schema of the matrix's own is not the same statement as a PASS into
`public`, and `pgTargetSchema` is what tells them apart.

`migrationId` is the handle for reading a cell back out of centipede afterwards,
which is why `KEEP_MIGRATION=1` is the default.

The two `.log` files **accumulate**. A run appends to whatever is already there
and opens its section with a separator carrying the timestamp and the command
line, so searching for `=== run` steps between runs. A run costs money and
minutes and a failure is usually read against the run before it, which a log
truncated at startup would have thrown away. Nothing rotates them — a full
matrix writes a few hundred KB, so delete them by hand when they stop being
interesting, or set `NO_LOG=1` to write none.

The result JSON is a single document rather than a log, so it is **replaced**
each run; the run that produced it is recorded in the run log.

---

## Configuration reference

Everything lives in `.env`. Precedence:

```
CLI option  >  real shell variable  >  .env  >  script default
```

`.env.example` documents every key. The ones that decide behaviour:

| Key | Default | What it does |
|---|---|---|
| `HB_BASE` / `CP_BASE` | `:8081` / `:8085` | where cm-honeybee and cm-centipede answer |
| `HOST_IP` | `127.0.0.1` | how honeybee reaches a source container's published port |
| `MATRIX_NAME_PREFIX` | `cptfm` | 2-6 chars; every resource name starts with it |
| `MATRIX_ALLOWED_CIDR` | `0.0.0.0/0` | inbound CIDR for the engine port |
| `ADMIN_DB` | `matrixadm` | database created with the instance; the matrix connects here to create and drop cell databases |
| `<CSP>_ENGINES` | | engines to run on this CSP |
| `<CSP>_<ENGINE>_SRC_VERSIONS` | | rows: apt repository versions |
| `<CSP>_<ENGINE>_DST_VERSIONS` | | columns: managed engine versions |
| `TARGET_TLS_MODE` | `prefer` | transport security for every target connection |
| `AWS_TARGET_SECURE_TRANSPORT` | `csp-default` | `off` attaches a parameter group allowing plaintext |
| `PG_TARGET_TEMPLATE` | `template1` | template each cell's database is copied from (AWS only) |
| `DBMS_ON_FAILURE` | `cleanup` | `keep` leaves a failed target database for inspection |
| `MODE` | `direct` | source access: `direct` or `ssh`. The target is always `direct` |

NCP target versions must be **full strings** (`8.0.36`, not `8.0`). AWS accepts
both a prefix and a full version and resolves a prefix to the current minor
release.

### Finding valid target versions

```bash
./scripts/aws-db-versions.sh              # read-only
./scripts/ncp-db-versions.sh
./scripts/aws-db-versions.sh --write      # write the result into *_DST_VERSIONS
```

NCP's catalog is complete, so what it prints is everything. **AWS's is a
summary**: the provider has no data source that lists every version, so
`tofu/aws/versions` probes one version line at a time. Absent from the summary is
not unavailable — put the version in `<CSP>_<engine>_DST_VERSIONS` and the plan
decides. The authoritative check is the matrix's own `tofu plan` per target
version, which also catches "cannot be ordered with this instance class".
