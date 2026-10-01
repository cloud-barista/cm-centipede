# Managed-DB Version Matrix — cm-honeybee + cm-centipede over cm-beetle

Runs a migration for **every combination of a source engine version and a target
engine version**, where the source is a container this folder builds and the
target is a **managed cloud database provisioned with cm-beetle**. The outcome is
reported as a matrix.

```
  src \ dst   │ 8.0         8.4
  ────────────┼────────────────────────
  8.0         │ PASS 3m12s  PASS 3m40s
  8.4         │ BLOCK 4s    PASS 3m18s
```

The migration itself is **cm-centipede**, driven over its REST API, with
**cm-honeybee** collecting the source. This folder provisions, prepares and
judges; it does not migrate anything itself.

> **Four servers must already be running** — cm-honeybee and cm-centipede for the
> migration, cm-beetle and cb-tumblebug for the provisioning (with cb-spider
> behind them). This folder starts none of them, and steps 1 and 2 of every run
> check that all four answer before a single managed instance is created. `.env`
> points at them with `HB_BASE`, `CP_BASE`, `BEETLE_URL` and `TUMBLEBUG_URL`.

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
resolves — one per CSP the credentials file holds. Both stores keep them in
`deployments/docker-compose/data/`, so they survive a `make down` and every later
`make up`. Re-run it only after `make clean-all`, which deletes that data on
purpose.

**No CSP key ever enters this folder.** cb-tumblebug holds them and beetle reaches
them through the connection; step 2 of every run only checks that the connection
exists. The one secret in `.env` here is the managed database's master password.

`make up` runs in the **foreground** — it builds, starts, and then streams the
logs, so run this matrix from a second terminal and stop the stack with `Ctrl+C`
followed by `make down`. It also unseals OpenBao on the way up; `make unseal`
does that on its own if the containers were restarted some other way. `make
status` lists what is running, `make logs` follows the logs. See
[`deployments/README.md`](../../deployments/README.md) for the rest.

---

## Support matrix

The source is built from the vendor's apt repository, so all four engines work
there. The **target** has to clear two separate bars: the CSP must offer it as a
managed service — self-hosted targets are out of scope — and cm-beetle must be
able to ask for it.

| Engine | AWS target | NCP target |
|---|---|---|
| mysql | RDS | Cloud DB for MySQL |
| mariadb | RDS | **none** — NCP has no managed MariaDB |
| postgresql | **not yet** — beetle cannot ask for it | **not yet** — beetle cannot ask for it |
| mongodb | **none** — see below | **not yet** — beetle cannot ask for it |

**Runnable today: AWS mysql, AWS mariadb, NCP mysql.**

**Why postgresql and mongodb are "not yet".** cm-beetle's managed-RDBMS model
declares `dbEngine` as `enums:"mysql,mariadb"`, and for anything else
`selectEngineVersion` returns a **mysql recommendation with a warning instead of
an error** (`pkg/core/recommendation/rdbms.go`). Silently building the wrong
engine is worse than refusing, so this matrix refuses first. The lists live in
`BEETLE_ENGINES_<CSP>` in `scripts/lib/beetle.sh` — one line per CSP, which is
what changes the day beetle gains an engine, or set it in `.env` to try it
without patching.

**Why no MongoDB on AWS.** The only managed option is DocumentDB, which exposes
no public endpoint; a client outside the VPC cannot reach it without a tunnel or
a VPN. Running MongoDB on EC2 would work but is self-hosting, which this tool
does not do.

**Why no MariaDB on NCP.** NCP offers MariaDB only as something you install
yourself on a server. Use AWS for mariadb columns.

An impossible combination is refused by `assert_engine_known`
(`scripts/lib/beetle.sh`) **before anything is created**, with the reason and what
to do instead — and the two bars are reported apart, because "this CSP does not
sell it" and "beetle cannot ask for it" call for different answers.

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

The postgresql and mongodb images are kept even though neither can be a target
yet. They cost nothing to keep and are ready the day beetle opens those engines.

---

## Quick start

```bash
cd testenv/beetle-db-test

cp .env.example .env && chmod 600 .env
# fill in AWS_DB_PASSWORD (or NCP_DB_PASSWORD), then:

./scripts/aws-db-matrix.sh --engines mysql --src-versions "8.0" --dst-versions "8.0"
```

Start with a single cell. One cell exercises the whole path — image build,
provisioning, target database creation, collection, migration, validation,
teardown — for the price of one instance. Widen the version lists once it goes
green.

```bash
./scripts/aws-db-matrix.sh                    # everything in AWS_ENGINES
./scripts/ncp-db-matrix.sh --engines mysql
./scripts/aws-db-matrix.sh --only 8.4:8.0     # one cell of an existing matrix
./scripts/aws-db-matrix.sh --tls-mode require # demand TLS instead of preferring it
./scripts/aws-db-matrix.sh --target-db-create matrix
                                              # create the target database here,
                                              # instead of leaving it to centipede
./scripts/aws-db-matrix.sh --cleanup          # reclaim what an interrupted run left
```

`chmod 600` is advised here rather than enforced: with no `up.sh` and no
`register-creds.sh` there is nothing that reads the file before a run to refuse
it, so the matrix warns and carries on. It still holds a password. See
[Credentials](#credentials).

---

## How it fits together

```
 Step 0  Prerequisites      docker, jq, curl
            |               + cm-honeybee and cm-centipede running, with
            |                 cm-centipede pointed at the SAME cm-beetle as this
            |                 folder (centipede.beetle.endpoint) — the target is
            |                 a beetleDb reference, so centipede calls beetle too
            |               + cm-beetle, cb-tumblebug and cb-spider running,
            |                 with the <csp>-<region> connection registered
 Step 1  .env               cp from .example, chmod 600, fill in the DB password
            |
 Step 2  ./scripts/<csp>-db-versions.sh      (optional) what does this region offer
            |
 Step 3  ./scripts/<csp>-db-matrix.sh        the matrix itself
            |    ├ pre-flight    honeybee, centipede, beetle, tumblebug,
            |    │               connection, password, namespace, versions
            |    ├ network       vNet + two subnets + security group — both CSPs
            |    └ per column    create -> [NCP: console step] -> probe -> cells -> delete
            |
 Step 4  logs/<csp>-db-matrix.log
         logs/<csp>-db-matrix-api.log
         logs/<csp>-db-matrix-result.json
            |
 Step 5  ./scripts/<csp>-db-matrix.sh --cleanup   only if a run was killed outright
```

There is no step for taking a stack down, because this folder brings none up.
`logs/` is created on the way; there is nothing to set up.

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
> production** — a container granted these can reach the host that runs it.

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

Container names start with `beetle-db-test-`, published ports live in the
**31xxx** band and the resource prefix is `cpbdb` — all this folder's own, so it
can be up alongside any other environment in this repo.

---

## Credentials

There is no vault in this folder, and no CSP key either. **cb-tumblebug holds
the CSP keys** — put there once by `make init` — and beetle reaches them through
the connection. The only secret that stays here is the managed database's master
password, and the file mode is what protects it.

```
.env (600)
    AWS_DB_PASSWORD / NCP_DB_PASSWORD      the managed instance's master password
      │
      ▼
  db_password() in beetle.sh               the only place it is read
      │
      ├──▶ instance create                 adminUserPassword in the request body
      ├──▶ logical database create/drop    X-Admin-User-Password header
      └──▶ centipede's target connection   db.password in the plan request
```

Everything goes through `db_password()`, so putting the password back behind a
secret store later changes that one function and nothing else.

`assert_db_password` validates the NCP password against the rules NCP enforces
**before** anything is provisioned. Without that check, NCP reports the violation
as a failed creation about 30 minutes later.

---

## One column, step by step

1. **Skip if empty.** If `--only` filtered out every cell of this column, no
   instance is created. Standing up a managed database for a column nobody runs
   wastes tens of minutes and real money.
2. **`create_rdbms`** — recommendation, then the three version checks below, then
   `POST` with `Prefer: respond-async` and poll `GET /request/{id}`.
3. **NCP only: the console step.** See
   [Known limitations](#known-limitations).
4. **`wait_target_reachable`** — TCP connect to the endpoint until it answers.
   Without this, every cell in the column fails one at a time with the same
   connection error.
5. **The creation probe.** Creates a throwaway logical database with the very
   call the cells use, and drops it. Failing it marks the whole column SKIP, with
   the error the CSP actually returned.
6. **Cells**, in source-version order.
7. **`release_column`** — delete the instance, unless `--keep-instance`.

---

## One cell, step by step

| Step | What happens |
|---|---|
| 1 | Build the source image if this version has none, start the container, wait for `matrix-init.service` |
| 2 | Clear any leftover target database, and create it here if `TARGET_DB_CREATE=matrix` |
| 3 | Register the source with cm-honeybee and collect it (`import/db`) |
| 4 | `POST /plans/target` — **a downgrade is refused here, and the cell is BLOCK** |
| 5 | `POST /migration`, then poll until it settles — centipede creates the target database now, by default |
| 6 | `POST /migration/{id}/validation`, then poll — this is the verdict |
| 7 | `GET /migration/{id}/logs` — the per-item migration log |
| 8 | Drop the target database, remove the source container, record the result |

### Who creates the target database

`TARGET_DB_CREATE` decides, and it decides more than who makes the call.
centipede's `EnsureTargetDatabase` reports whether it created the database, and
that flag picks the undo path for a failed cell.

| | step 2 | `created` | a failed cell |
|---|---|---|---|
| `centipede` **(default)** | leftover dropped | `true` | the database is dropped whole |
| `matrix` | leftover dropped, then created | `false` | transx-ex's rollback empties it and keeps it |

Both are real situations — an operator who provisions the target ahead of time
gets the second — so both are runnable, and the value is recorded with the result.
Either way a leftover from a previous cell goes first: transx-ex requires the
target to exist and be empty, and under `centipede` a leftover would also make
centipede find a database it does not own.

**The target database is created through beetle, not over the wire** — by
whichever side makes the call. A managed instance's master account is granted
rights on the databases the service knows about and cannot create new ones — NCP
answers `Error 1044 (42000): Access denied for user 'dbadmin'@'%'`. So the
instance's owner is asked instead. The call takes a name and the admin password
and nothing else, so **no character set can be specified** and the database
follows the instance defaults; table character sets travel inside the dump's
`CREATE TABLE` statements, and centipede reports a database-level difference as a
warning.

**Step 7 reads the log after the verdict, not before it.** By then the entries
also carry what any rollback did, which is the part worth reading when a cell
failed. When the migration itself never finishes, step 6 is never reached, so the
log is read at that failure instead. `GET /migration/{id}/logs` is **paginated** —
the matrix walks to the end of the pages rather than reading one.

**Validation is centipede's, not this folder's.** It compares exact row counts per
table or collection, twelve kinds of schema object and character sets. A charset
difference is reported as a **warning**, which is the right call here: the target
database was created without a charset argument, so differing from the source is
expected.

---

## Known limitations

### 1. Managed instances that refuse plaintext connections

AWS turns TLS on by default for some engine versions: **MariaDB 11.8 and later**
set `require_secure_transport=ON`. It is not the engine's default but the
parameter the CSP attaches to a managed instance — the same engine version run
outside a managed service does not carry it. A client that can only speak
plaintext cannot reach those instances at all.

`TARGET_TLS_MODE` sets transport security per connection, with the five
libpq-style modes:

| Mode | Encrypt | Verify chain | Verify hostname | Server offers no TLS |
| ---- | :-----: | :----------: | :-------------: | -------------------- |
| `disable` | no | — | — | plaintext |
| `prefer` **(matrix default)** | try | no | no | plaintext |
| `require` | yes | no | no | error |
| `verify-ca` | yes | yes | no | error |
| `verify-full` | yes | yes | yes | error |

It governs the migration — the `beetleDb.tlsMode` centipede carries into
transx-ex's `DirectConfig` — and nothing else. **Setting up a cell does not use
it.** Creating the target database is cm-beetle's call, made through cb-spider's
SQL fallback, and that picks its own transport, whether centipede or this script
asks for it.

The mode rides on the reference because cm-beetle has nothing to say about it:
beetle reports where the instance is and who its admin is, while a managed
instance offers TLS whether or not it demands it, so the choice is the caller's.
Before `BeetleDBRef` carried the field, a managed target could only be reached in
plaintext — the mode was accepted here and then silently dropped on the way.

**Why `prefer` is the default.** A managed instance offers TLS whether or not it
demands it, so `prefer` encrypts in practice and still connects to an instance
that allows plaintext — including NCP Cloud DB for MySQL, which offers no TLS at
all. One value runs every column, without touching what the CSP configured.

Three limits worth knowing:

- **MongoDB has no `prefer`** — a client either negotiates TLS or it does not, and
  transx-ex rejects it. The matrix drops **mongodb cells to `disable`** and leaves
  the other engines on the configured mode, so one mongodb does not refuse a whole
  run. Unreachable on this path in any case: a `beetleDb` target accepts mysql and
  mariadb only. The effective mode appears per engine in the result JSON
  (`engines[].tlsMode`).
- **Creating the target database ignores the mode entirely** — see above. That
  connection belongs to cb-spider, which decides for itself, and there is no
  setting on either side that changes it. This is why the TLS field on the
  reference does **not** unblock the columns in the next section: the wall is in
  creation, not in the migration.
- **`verify-ca` and `verify-full` use the system trust store only.** RDS and NCP
  sign with their own CAs, so verification fails without their bundle, and this
  matrix has no path to supply one.

#### No parameter group, and no escape hatch

A parameter group that allows plaintext would settle this, and **there is no
parameter-group API anywhere in the beetle stack** — which is also why there is
no `--secure-transport` option here. What you can do instead:

1. Target a version whose managed instance is created with the value off. The
   console's default parameter group shows which; `./scripts/aws-db-versions.sh`
   lists what the region offers.
2. Create a parameter group in the console with `require_secure_transport=0`,
   attach it and reboot — manual, because nothing in the stack exposes it.
3. Change cb-spider so its SQL fallback connects over TLS to this CSP too.

`AWS_MARIADB_DST_VERSIONS` still ships with 11.8 in it. A SKIP that prints why is
a result about this stack worth having, and hiding the combination would only
make the gap harder to find later.

### 2. Automated backups turn on binary logging, and that blocks stored functions

The seed carries a stored function on purpose, and on MySQL and MariaDB that one
object decides whether a cell can finish. Restoring it into a managed instance
fails like this:

```
migration failed at stage "restore": create failed on function
matrix_db.fn_order_total (statement 16/52):
Error 1419 (HY000): You do not have the SUPER privilege and binary logging is
enabled (you *might* want to use the less safe log_bin_trust_function_creators
variable)
```

MySQL puts **two separate gates** in front of creating a stored function while
the binary log is on, and the error number says which one was hit:

| Gate | What it wants | Violation |
| ---- | ------------- | --------- |
| the function declares itself safe to log | one of `DETERMINISTIC`, `NO SQL`, `READS SQL DATA` | **1418** |
| the account may make that declaration | `SUPER`, or `log_bin_trust_function_creators=1` | **1419** |

**1419 means the declaration was fine and the privilege was not.** The seed
declares `READS SQL DATA`, so the first gate passes; the second cannot, because a
managed service never grants `SUPER` to the master account — it keeps it for its
own maintenance user. Nothing about the migration is at fault, and nothing in the
dump is missing.

**Why the matrix forces the backup retention to zero.** On RDS, automated backups
are what turn binary logging on. Ask for none and the binary log stays off, and
the function restores under the ordinary `CREATE ROUTINE` privilege. cm-beetle's
recommendation answers a requested `0` with the CSP's own default of `7`, so the
matrix sets `backupRetentionDays` again on the create call itself, where it is
read. `<CSP>_DB_BACKUP_RETENTION_DAYS` is the knob, and `0` is the default for
this reason rather than to save money — though an instance that lives for one
column has nothing worth backing up either.

Three things worth knowing:

- **It only works if the CSP honours the zero.** The value travels
  beetle -> cb-tumblebug -> cb-spider before it reaches the CSP. Read it back off
  the instance to be sure: `backupRetentionDays` in
  `GET /migration/middleware/ns/{ns}/rdbms/{name}`.
- **Raising it re-breaks every mysql and mariadb column**, not just the cell with
  the function in it — the restore stops at that statement and the whole item
  fails. If you need backups on for some other reason, expect those columns to go
  red and read this section again.
- **PostgreSQL is unaffected.** It has no equivalent of this gate, so its columns
  do not care what the retention is.

If the retention cannot be held at zero, the only other way through is a
parameter group with `log_bin_trust_function_creators=1` — the same wall as in 1,
and just as manual.

### 3. NCP managed databases need a public domain, issued by hand

NCP answers with a private domain until a public domain is requested, and the
request exists only in the console — no API action.

So `ncp-db-matrix.sh` **always stops and waits for Enter** after an instance is
ready, once per target version:

```
NCP console > Database > Cloud DB for <engine> > select the DB server
  > DB Management > Public Domain Management > Request
```

It does not try to decide whether the wait is necessary. Guessing is fine when
the guess is right and unrecoverable when it is wrong: the run would sail past
the one moment a human could act, and every cell in the column would then fail
trying to reach a private domain. The wait ignores `NO_PAUSE`; with no tty at all
(CI) it warns and moves on.

After Enter the instance is read again. **A single-resource read is also the
refresh** — cb-tumblebug asks cb-spider and overwrites `endpoint` /
`publicAccess` / `status` with the live answer before storing it again
(`core/resource/rdbms.go GetRDBMS`) — so a domain issued in the console shows up
here. `publicAccess` is an observation, not an echo of the request: cb-spider's
NCP driver sets it `true` only when the instance really has a public domain, and
falls back to the private one otherwise.

If it is still `false` the run does not stop. The record may simply not have
caught up, and whether the address is reachable is what the next check decides.

---

## Verdicts and results

| Verdict | Meaning |
|---|---|
| **PASS** | Migration `completed` and validation `passed` |
| **BLOCK** | A higher-to-lower version pair that `POST /plans/target` refused with a downgrade error — the expected, correct outcome |
| **FAIL** | Anything else |
| **SKIP** | Filtered out by `--only`/`--engines`, or the column could not be prepared (instance failed, endpoint unreachable, creation probe failed) |

There is **no `--skip-version-check`**: the centipede API has no way to skip the
version check, so a downgrade cannot be attempted for real from here. There is no
`--scope` or `--async` either — centipede's DBMS migration is always full scope
and always asynchronous.

Outputs land in `logs/`:

- `<csp>-db-matrix.log` — the full run, with colour stripped
- `<csp>-db-matrix-api.log` — every beetle, honeybee and centipede call as a
  runnable `curl` line plus the response, with credentials masked
- `<csp>-db-matrix-result.json`:

```json
{
  "csp": "aws",
  "namePrefix": "cpbdb",
  "nsId": "cpbdb01",
  "provisioner": "cm-beetle",
  "migrator": "cm-centipede",
  "collector": "cm-honeybee",
  "srcMode": "direct",
  "dstMode": "direct",
  "dstConnection": "beetleDb",
  "targetDbCreate": "centipede",
  "targetTLSMode": "prefer",
  "dbmsOnFailure": "cleanup",
  "grantedPublicSchema": null,
  "pgTargetSchema": null,
  "finishedAt": "2026-09-09 18:20:11",
  "engines": [
    { "engine": "mysql", "tlsMode": "prefer",
      "cells": [
        { "srcVersion": "8.0", "dstVersion": "8.4", "status": "PASS",
          "elapsedSec": 192, "srcServerVersion": "8.0.43",
          "migrationId": "01J...", "validationStatus": "passed",
          "detail": "validation passed (source 8.0.43)" }
      ] }
  ]
}
```

`migrationId` is the handle for reading a cell back out of centipede afterwards,
which is why `KEEP_MIGRATION=1` is the default.

The two `.log` files **accumulate**. A run appends to whatever is already there
and opens its section with a separator carrying the timestamp and the command
line, so searching for `=== run` steps between runs. A run costs money and
minutes and a failure is usually read against the run before it. Nothing rotates
them; set `NO_LOG=1` to write none. The result JSON is a single document rather
than a log, so it is **replaced** each run.

---

## Configuration reference

Everything lives in `.env`. Precedence:

```
CLI option  >  real shell variable  >  .env  >  script default
```

`.env.example` documents every key. The ones that decide behaviour:

| Key | Default | What it does |
|---|---|---|
| `BEETLE_URL` / `TUMBLEBUG_URL` | `:8056/beetle` / `:1323/tumblebug` | where the provisioner answers |
| `HB_BASE` / `CP_BASE` | `:8081` / `:8085` | where cm-honeybee and cm-centipede answer |
| `MATRIX_NS` | `cpbdb01` | the cb-tumblebug namespace — this folder's own |
| `MATRIX_NAME_PREFIX` | `cpbdb` | 2-6 chars; every resource name starts with it, and `--cleanup` matches on it |
| `MATRIX_ALLOWED_CIDR` | `0.0.0.0/0` | inbound CIDR for the engine port |
| `BEETLE_ENGINES_<CSP>` | `mysql mariadb` / `mysql` | what beetle can build there; widen to try a newly-supported engine |
| `HOST_IP` | `127.0.0.1` | how honeybee reaches a source container's published port |
| `<CSP>_ZONE` / `_ZONE2` | | two different zones — a managed RDBMS wants both |
| `<CSP>_VNET_CIDR` | | a /16, unless you set the subnet CIDRs yourself |
| `<CSP>_DB_PASSWORD` | | the managed instance's master password — the one secret here |
| `<CSP>_DB_SRC_*` | 2 / 4096 / 100 | the source profile beetle sizes the target from |
| `<CSP>_ENGINES` | | engines to run on this CSP |
| `<CSP>_<ENGINE>_SRC_VERSIONS` | | rows: apt repository versions |
| `<CSP>_<ENGINE>_DST_VERSIONS` | | columns: managed engine versions |
| `TARGET_TLS_MODE` | `prefer` | transport security for the **migration**, carried as `beetleDb.tlsMode` |
| `TARGET_DB_CREATE` | `centipede` | who creates the target database — `centipede` or `matrix` |
| `RDBMS_TIMEOUT` / `RDBMS_POLL_INTERVAL` | `2400` / `15` | waiting for a managed instance |
| `POLL_INTERVAL` | `5` | centipede's polling — a different clock entirely |
| `DBMS_ON_FAILURE` | `cleanup` | `keep` leaves a failed target database for inspection |
| `MODE` | `direct` | source access: `direct` or `ssh`. The target is always `direct` |
| `KEEP_NETWORK` | `0` | `1` keeps the vNet, security group and namespace |

### Finding valid target versions

```bash
./scripts/aws-db-versions.sh              # read-only
./scripts/ncp-db-versions.sh
./scripts/aws-db-versions.sh --write      # write the result into *_DST_VERSIONS
```

It lists every engine the CSP sells, marking `*` for the ones in `<CSP>_ENGINES`
and `-` for the ones beetle cannot build. `--write` fills in only the engines
beetle can build — writing a version list for an engine that cannot be a target
would put something in `.env` the matrix then refuses to run.
