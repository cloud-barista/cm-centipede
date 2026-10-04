# cm-centipede CM-Beetle-Based Migration Test Environment

Create and destroy **VMs, object storage buckets and managed databases** on any
CSP cb-tumblebug has a connection for, by calling the **cm-beetle REST API**.

These resources exist to give cm-centipede something real to migrate. Each
resource type is provisioned and destroyed **independently** — just a bucket,
just a VM, or just the databases.

> **cm-beetle, cb-tumblebug and cb-spider must already be running**, with the CSP
> connections registered on tumblebug. beetleenv starts nothing at all — no
> containers, no secret store — and needs only `curl` and `jq`. `.env` points at
> beetle with `BEETLE_URL` and at tumblebug with `TUMBLEBUG_URL`.

### Starting that stack — `make up`, `make init`, `make down`

All three backing servers come up in one command, from the **repo root**. The
Compose stack in [`deployments/docker-compose`](../../deployments) runs cm-beetle
on 8056, cb-tumblebug on 1323 and cb-spider on 1024 — exactly what the default
`BEETLE_URL` and `TUMBLEBUG_URL` in `.env.example` point at, so a stack started
this way needs no URL changes here.

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
logs, so run beetleenv from a second terminal and stop the stack with `Ctrl+C`
followed by `make down`. It also unseals OpenBao on the way up; `make unseal`
does that on its own if the containers were restarted some other way. `make
status` lists what is running, `make logs` follows the logs. See
[`deployments/README.md`](../../deployments/README.md) for the rest.

Then come back here and continue with the quick start below.

---

## Support matrix

| CSP | vm | bucket | mysql | mariadb | postgresql | mongodb |
|---|:--:|:--:|:--:|:--:|:--:|:--:|
| aws | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ |
| ncp | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |

Nothing in that table is hard-coded. Engine support is read from cb-tumblebug at
runtime — `./scripts/catalog.sh <csp> engines` prints it — and the two ❌ columns
are cm-beetle's limit, not the CSP's: its managed RDBMS API declares
`dbEngine` as `enums:"mysql,mariadb"`. See
[PostgreSQL and MongoDB](#postgresql-and-mongodb).

---

## Quick start

```bash
cd beetleenv
cp .env.example .env && chmod 600 .env    # change every ChangeMe, fill in region/zones

./scripts/up.sh                           # check the stack, create the namespace
./scripts/catalog.sh aws rdbms            # what aws actually offers

./scripts/provision.sh aws database       # every engine in BEETLEENV_AWS_DB_ENGINES
./scripts/provision.sh aws vm             # lands in the same vNet as the database
./scripts/provision.sh aws bucket

./scripts/conn-info.sh aws                # endpoints and accounts, secrets masked
./scripts/conn-info.sh aws --reveal       # password and SSH private key
./scripts/conn-info.sh aws --ssh          # save the key, print the ssh command

./scripts/status.sh                       # what is still running
./scripts/deprovision.sh aws database     # always clean up - these cost money
./scripts/deprovision.sh all all          # tear everything down, every CSP
```

The namespace is created once by `up.sh` and never deleted, so a teardown can be
followed straight by another `provision.sh`.

Swap `aws` for `ncp` and the same sequence runs against NCP — `.env.example`
ships both, and every resource is named per CSP, so the two never touch each
other even though they share one namespace.

---

## How it fits together

```
 Step 1  .env            cp .env.example .env  ->  every ChangeMe, region, zones
            |
 Step 2  Start           ./scripts/up.sh
            |              └ beetle /readyz, tumblebug /readyz
            |              └ per CSP: check the settings
            |              └ create the namespace (the one call to tumblebug)
            |
 Step 3  Look up         ./scripts/catalog.sh aws rdbms | engines | objectstorage
            |              └ read-only; the live catalogue, not a list in here
            |
 Step 4  Provision       ./scripts/provision.sh aws database   (or vm / bucket)
            |              └ recommend -> check -> inject -> migrate -> wait
            |              └ the shared vNet is created on the way if absent
            |
 Step 5  Connection info ./scripts/conn-info.sh aws [--reveal] [--ssh] [--json]
         NCP only        ./scripts/ncp-db-domain.sh   -> public domain check
            |
 Step 6  Check           ./scripts/status.sh   -> what is up. Deletes nothing.
            |
 Step 7  Destroy         ./scripts/deprovision.sh aws all
                           ./scripts/deprovision.sh all all  -> every CSP
                           └ the namespace is kept, always
```

Every entry point calls `preflight()` first, so a script run out of order fails
with "cm-beetle is not reachable" or "namespace does not exist" rather than with
something unrelated further in.

---

## Step 1 — Create `.env`

```bash
cd beetleenv
cp .env.example .env && chmod 600 .env
```

This file is **a store, not an intake form**: nothing in it is blanked after the
first run, because the one secret it holds — the RDBMS admin password — is
re-sent on every call that creates a database. `preflight()` refuses to run while
the file is readable by anyone but its owner.

There are **no CSP credentials here at all**. cb-tumblebug holds those, and
beetleenv never sees them.

### What a CSP needs

| | Key | Why |
|---|---|---|
| region | `BEETLEENV_<CSP>_REGION` | half of the connection name |
| zones | `BEETLEENV_<CSP>_ZONE`, `_ZONE2` | a managed RDBMS wants subnets in two |
| network | `BEETLEENV_<CSP>_VNET_CIDR` | the block the VM and databases share |
| DB password | `BEETLEENV_<CSP>_DB_PASSWORD` | re-sent on every create |
| engines | `BEETLEENV_<CSP>_DB_ENGINES` | which databases to create |

**`_ZONE2` must differ from `_ZONE`.** `up.sh` refuses two identical values,
because the CSP would refuse the database an hour later.

**The region has to match a connection cb-tumblebug already holds**, under the
name beetle looks for:

```
<csp>-<region>          aws-ap-northeast-2, ncp-kr
```

That is not a convention beetleenv chooses. `GenerateConnectionName` in cm-beetle
builds exactly that string and resolves nothing else, so a connection registered
under any other name is invisible to beetle.
`./scripts/catalog.sh <csp> connection` says whether yours resolves.

**Everything else is resolved at provision time.** Engine versions, instance
specs, storage types and sizes, VM specs and VM images all come out of
cm-beetle's recommendation APIs, which ask cb-tumblebug for the live catalogue.
That is why there is no VM image or VM spec key anywhere in `.env`, and why
changing a region needs nothing looked up by hand.

What you configure instead is the **source** side — "the database being migrated
had 2 vCPU, 4GB and 100GB of storage":

```dotenv
BEETLEENV_AWS_DB_SRC_VCPU=2
BEETLEENV_AWS_DB_SRC_MEMORY_MB=4096
BEETLEENV_AWS_DB_SRC_STORAGE_GB=100
```

beetle sizes the target from those. To get something bigger, raise them; there
is no instance type to name.

The minimum for one CSP:

```dotenv
BEETLE_URL=http://localhost:8056/beetle
TUMBLEBUG_URL=http://localhost:1323/tumblebug

BEETLEENV_NS=cpbt01
BEETLEENV_NAME_PREFIX=cpbt
BEETLEENV_CSPS="aws"

BEETLEENV_AWS_REGION=ap-northeast-2
BEETLEENV_AWS_ZONE=ap-northeast-2a
BEETLEENV_AWS_ZONE2=ap-northeast-2c
BEETLEENV_AWS_VNET_CIDR=10.0.0.0/16
BEETLEENV_AWS_DB_ENGINES="mysql mariadb"
BEETLEENV_AWS_DB_PASSWORD=ChangeMe
```

`BEETLEENV_CSPS` is quoted because `.env` is sourced by the shell — an unquoted
value with spaces in it is read as a command.

### Resource names

```dotenv
BEETLEENV_NAME_PREFIX=cpbt      # "centipede beetle". Letters and digits, no hyphen.
```

Everything is `<prefix>-<csp>-<what>`:

| Resource | Name |
|---|---|
| vNet and subnets | `cpbt-aws-vnet`, `cpbt-aws-subnet-1`, `cpbt-aws-subnet-2` |
| Security group | `cpbt-aws-sg` |
| Databases | `cpbt-aws-db-mysql`, `cpbt-aws-db-mariadb` |
| Bucket | `cpbt-aws-bucket` |
| VM infrastructure and its key | `cpbt-aws-infra`, `cpbt-aws-sshkey` |

**The CSP is in the name because the namespace is shared.** A cb-tumblebug
resource id is unique per namespace and resource type, not per connection, so a
plain `cpbt-vnet` would mean the AWS vNet and the NCP vNet at once — and
`deprovision.sh ncp` would delete the AWS one. Names are matched on
`<prefix>-<csp>-` exactly so that a per-CSP teardown is safe.

**These are cb-tumblebug ids, not CSP resource names.** tumblebug generates what
the CSP sees from a uid of its own, so no CSP id length limit applies here — and
there is no globally unique bucket name to pick either, which is why `.env` has
no bucket name key.

`preflight()` enforces `^[a-z][a-z0-9]{1,9}$` on the prefix. **No hyphen**,
because ownership is a `startswith("<prefix>-<csp>-")` test: with a hyphen
allowed, `cp` would claim what `cp-aws-x` created (`cp-aws-x-aws-vnet` starts
with `cp-aws-`). Without one, a name's first segment is its prefix, whole.

---

## Step 2 — `up.sh`

```bash
./scripts/up.sh
```

Three things: beetle and tumblebug answer, every CSP in `BEETLEENV_CSPS` has the
settings it needs, and the namespace exists. It creates no cloud resources and
costs nothing. Safe to re-run.

**The namespace is the one call that does not go through beetle.** cm-beetle has
no namespace API — the routes are in `pkg/api/rest/server.go` but commented out —
while every migration handler starts by reading the namespace and failing if it
is not there. So `up.sh` posts to cb-tumblebug's `/ns` directly. That is why
`TUMBLEBUG_URL` is in `.env` at all; nothing else uses it.

A CSP short of a key is reported and stepped over rather than aborting the run,
so one pass lists everything that needs filling in. The exit status is still
non-zero — a CSP that cannot be used is a failure, not a warning.

---

## Step 3 — `catalog.sh`

Read-only. Creates nothing.

```bash
./scripts/catalog.sh aws rdbms           # engines, versions, specs, storage
./scripts/catalog.sh ncp engines         # just the engine list, one per line
./scripts/catalog.sh aws objectstorage   # object storage feature support
./scripts/catalog.sh aws vmspec          # VM specs for the source profile in .env
./scripts/catalog.sh aws vmimage         # OS images for the same
./scripts/catalog.sh aws imageprobe      # when vmimage finds nothing: why
./scripts/catalog.sh aws connection      # does <csp>-<region> resolve?
```

`--json` gives the raw payloads.

This is where "why did my mariadb request stop" gets answered before a provision
rather than after one fails.

`vmspec` and `vmimage` are the two halves of what `provision.sh <csp> vm` needs:
beetle recommends an infrastructure only where it can pair a spec with an image,
and it reports **no candidates at all** when it can pair neither — a 200 with an
empty list, not an error. Asking for the halves separately says which one is
missing. An empty answer from both usually means cb-tumblebug has not finished
loading its spec and image catalogue for that connection; the load runs on
initialization and takes several minutes.

### The image search key is `os id` + `version id`

beetle builds it as `node.OS.ID + " " + node.OS.VersionID` and never looks at
`prettyName`, so `BEETLEENV_<CSP>_VM_SRC_OS` must be written that way:

```dotenv
BEETLEENV_AWS_VM_SRC_OS="ubuntu 22.04"     # matches
BEETLEENV_AWS_VM_SRC_OS="Ubuntu 22.04.3 LTS"   # finds nothing
BEETLEENV_AWS_VM_SRC_ARCH=x86_64           # the other half of the search
```

`catalog.sh <csp> vmimage` prints the key it derived, so a mismatch is visible
before it becomes "no candidates".

### `imageprobe`

When the image search comes back empty, the filters are ANDed inside
cb-tumblebug and the answer says only that the whole conjunction matched nothing.
`imageprobe` adds beetle's filters one at a time — **the first `0` is the cause**
— and then lists the `osType` values the catalogue does hold:

```
$ ./scripts/catalog.sh aws imageprobe

==> image filter probe — aws-ap-northeast-2
  Each row adds one of the filters beetle uses. The first 0 is the cause.

  provider + region                      3
  + osType ubuntu 22.04                  1
  + osArchitecture x86_64                1
  + isGPUImage=false                     1
  + includeBasicImageOnly=true           0     <- this filter empties it

  osType values the catalogue holds for aws ap-northeast-2:
    amazon linux 2023
    ubuntu 22.04
    ubuntu 24.04
```

A `0` on `includeBasicImageOnly` means the images exist but none is flagged
`isBasicImage` in cb-tumblebug's catalogue — beetle asks for basic images only,
and that flag comes from tumblebug's own image assets, not from anything
beetleenv sends.

This is the one place beetleenv queries cb-tumblebug for something other than a
namespace. There is no beetle endpoint for a raw image search, and the probe is
read-only.

---

## Step 4 — `provision.sh`

```bash
./scripts/provision.sh <csp> <database|bucket|vm> [--engine <engine>]
```

Three resources, and only the three worth asking for by name. The vNet, its two
subnets and the security group are not among them: `database` and `vm` create
them on the way in and share one set between them, and a bucket needs no network
at all. See [the network is shared](#the-network-is-shared).

Every resource follows one shape:

```
recommend  ->  check  ->  inject  ->  migrate  ->  wait
```

**Recommend.** A description of a source resource goes to cm-beetle, which
resolves everything that differs per CSP and answers with a concrete target
specification. The source descriptions are built from `.env` in
`scripts/lib/recommend.sh`, next to the code that reads the answer back — they
carry only the fields beetle actually reads, and a field it ignores is one that
goes stale without anything noticing.

**Check.** See [the substitution guard](#the-substitution-guard) below. This is
the step that makes the difference between a test environment and a surprise.

**Inject.** The recommendation deliberately leaves three things empty, and all
three are filled in here:

| Field | Left empty by | If it stayed empty |
|---|---|---|
| `vNetId`, `subnetIds`, `securityGroupIds` | beetle | the create fails resolving an empty vNet id — tumblebug's `autoFillDefaults` covers engine version, spec and storage, but not the network |
| `adminUserPassword` | beetle | cm-beetle substitutes its own built-in default, `BeetleRdbms1234!` |
| `rdbmsName` | — it is `mig-rdbms-01` for every engine | the mysql and the mariadb instance would collide |

**Migrate and wait.** Every migration call is sent with `Prefer: respond-async`
and followed through `GET /request/{reqId}`, so progress is visible and a dropped
connection does not abandon a resource that is still being built. `--sync` sends
it the other way if something between you and beetle rewrites headers.

### The network is shared

`database` and `vm` both call `ensure_shared_network` first, and both land in the
same vNet. cm-centipede's whole reason for these resources is migrating data
between them, and a VM that cannot reach the database is not a test environment.

**That is why the network is not a resource you name**, in either direction. On
its own it would be an empty network nobody asked for, and offering it as a step
invites the two callers to end up with one each. It is created on demand by
whichever of `database` and `vm` runs first, found by the other, and removed by
`deprovision.sh` once neither is left — see
[the last one out](#the-last-one-out-takes-the-network).

The VM adopts the vNet rather than making its own because
`POST /migration/ns/{ns}/infra` takes `useExisting=true`: the ids in the request
body are matched against what exists, and only created if the match fails.

Two subnets, in two different zones, because every managed RDBMS wants them. The
`/24`s are derived from `_VNET_CIDR` by replacing the third octet, which holds
for a `/16`; anything smaller needs `_SUBNET1_CIDR` and `_SUBNET2_CIDR` set
explicitly, and says so rather than deriving a subnet outside the block.

### Naming, and why `nameSeed` is not used

The migration APIs offer a `nameSeed` query parameter that prefixes the
recommended names at creation time. beetleenv does not use it anywhere, and sets
the names in the request body instead. Two reasons:

- The recommendation names every RDBMS instance `mig-rdbms-01` whatever the
  engine, so one seed would give two instances one name.
- beetle's `ApplyNameSeed` prefixes the node groups' `vNetId`, `subnetId` and
  `securityGroupIds` along with the names. On top of the real resource ids
  injected for the VM, that would ask for `cpbt-cpbt-aws-vnet` and find nothing.

Either the seed names everything or the caller does. Mixing them silently breaks
the references.

### How long it takes

| Resource | Typical |
|---|---|
| network | seconds |
| bucket | seconds |
| vm | 2-5 minutes, plus the SSH wait |
| database | **5-30 minutes** — AWS at the low end, NCP at the high |

The SSH readiness check is polled every 35 seconds, not faster: beetle rate
limits it to one call per infrastructure per 30 seconds and answers 429 in
between.

---

## The substitution guard

**Asked for an engine a CSP does not have, cm-beetle answers `200 OK` with a
different one.** From `pkg/core/recommendation/rdbms.go`:

```go
if targetEngine == "mariadb" && !isMariaDBSupported(desiredCsp, support, hasSupport) {
    targetEngine = "mysql"
    warning := fmt.Sprintf("MariaDB is not supported on CSP '%s'. ...")
```

An engine it does not know at all — `postgresql` today — becomes `mysql` the same
way, a few lines above.

For a recommendation engine that is right: the caller asked for somewhere to put
their data, and MySQL is closer than nothing. For beetleenv it is not.
`provision.sh aws database --engine mariadb` that quietly builds a MySQL instance
gives cm-centipede the wrong thing to migrate, and the mistake surfaces much
later, somewhere else.

So the answer is compared against the request, and a substitution stops the run
before anything is created:

```
[WARN]  beetle: mariadb is not supported on CSP 'aws'. Recommended MySQL as fallback ...
[ERROR] beetle recommended 'mysql' for a requested 'mariadb' on aws.
        That is its documented fallback, not an error on its side - but creating a
        mysql instance when a mariadb was asked for would be the wrong test
        resource, so nothing has been created.
```

There are two earlier checks as well, so the usual case never gets that far:
`assert_engine_known` rejects an engine cm-beetle cannot build at all, and
`assert_engine_supported` rejects one this CSP does not offer, both before the
first resource is created.

---

## Step 5 — `conn-info.sh`

```bash
./scripts/conn-info.sh aws            # masked
./scripts/conn-info.sh aws --reveal   # password and private key in full
./scripts/conn-info.sh aws --ssh      # save the key and print how to log in
./scripts/conn-info.sh aws --ids      # just the API identifiers
./scripts/conn-info.sh aws --json     # machine-readable, all three id layers
```

Read live from cb-tumblebug, so it shows what exists rather than what was once
created.

`--reveal` and `--ssh` are the two ways to get the VM's private key: cb-tumblebug
generates it and does not hand it over unless asked. The database password comes
out of `.env`, not out of any API — tumblebug never returns it. `--ids` and
`--json` never print either, whatever else is passed.

### Logging in — `--ssh`

`--reveal` prints the key and leaves you to save it, chmod it and assemble an
address out of two other sections. `--ssh` does all of that:

```
$ ./scripts/conn-info.sh aws --ssh

==> aws — SSH access, nsId cpbt01

  nodeId        vm-beetleenv-source-01-1   (infraId cpbt-aws-infra, Running)
    key         …/beetleenv/keys/cpbt01/cpbt/aws/cpbt-aws-sshkey.pem  (0600, written)
    ssh         ssh -i …/cpbt-aws-sshkey.pem -o StrictHostKeyChecking=accept-new cb-user@52.78.182.93
    look        ssh -i … cb-user@52.78.182.93 'ls -al /home/cb-user'
    copy up     scp -i … ./file cb-user@52.78.182.93:/home/cb-user/
    sync up     rsync -av -e 'ssh -i …' ./dir/ cb-user@52.78.182.93:/home/cb-user/dir/
```

**The key goes to a file, not to stdout.** That is the point of the mode rather
than a detail of it: `--reveal` leaves the key in the terminal scrollback, and in
anything that tees its output, in a log file on disk. `keys/<ns>/<prefix>/<csp>/<sshKeyId>.pem`
is written 0600 inside a 0700 directory, and rewritten only when the content
differs — a second run says `unchanged`.

**`keys/` is not `state/`.** `state/` is documented as holding no secrets, and a
private key sitting there would quietly end that. `keys/` is its own directory,
gitignored, and `deprovision.sh <csp> vm` removes this setup's key file along
with the key pair it came from — a leftover file would otherwise have the next provision's `ssh`
command offering a key that opens nothing.

**One call per key, matched per node.** The key is fetched from each node's own
`sshKeyId` rather than from "the first key in the namespace", so a namespace
holding two infrastructures pairs each node with the key that actually opens it.

`accept-new` rather than ssh's default prompt: a re-created VM presents a new
host key, and the prompt would block a command you meant to leave running.

A node with no public IP prints why instead of a command — reaching a
private-only node needs a bastion, and this API does not report one.

### Three layers of identifier

Every resource carries all three, and they answer different questions.

| Layer | Field | What it is for |
|---|---|---|
| beetle API | `id` | the path parameter — `/migration/ns/{nsId}/infra/{infraId}` |
| cb-tumblebug | `uid` | tumblebug's internal handle. For a bucket it is **also the real name in the CSP** |
| CSP | `cspResourceId`, `cspResourceName` | what the CSP console shows — `vpc-0abc…`, `i-0abc…` |

The labels in the output are the API parameter names on purpose: what stands
beside `infraId` is exactly what an `infraId` path segment takes.

```
  managed RDBMS
    rdbmsId       cpbt-aws-db-mysql
      engine      mysql 8.4
      status      Available
      endpoint    cpbt-aws-db-mysql.xxxx.rds.amazonaws.com:3306
      admin user  dbadmin
      csp id      db-ABCDEF1234

  object storage
    osId          cpbt-aws-bucket
      status      Available
      csp name    tb-os-4kq2n7   <- the bucket name in the CSP
      csp id      arn:aws:s3:::tb-os-4kq2n7

  VM infrastructure
    infraId       cpbt-aws-infra  (Running)
      nodeId      cpbt-aws-infra-1
        public 1.2.3.4  private 10.0.1.10  user cb-user
        csp id    i-0abc123def456
```

`--ids` strips it down to the identifiers alone, one `<name> <value>` pair per
line, so a value is one `awk` away:

```bash
$ ./scripts/conn-info.sh aws --ids
nsId        cpbt01
rdbmsId     cpbt-aws-db-mysql
            cpbt-aws-db-mariadb
osId        cpbt-aws-bucket
infraId     cpbt-aws-infra
nodeId      cpbt-aws-infra-1
vNetId      cpbt-aws-vnet
subnetId    cpbt-aws-subnet-1
            cpbt-aws-subnet-2
sgId        cpbt-aws-sg
sshKeyId    cpbt-aws-sshkey

$ ./scripts/conn-info.sh aws --ids | awk '$1=="infraId"{print $2}'
cpbt-aws-infra
```

`--json` carries all three layers under the same names, with the keys
cb-tumblebug did not fill in omitted rather than set to `null`.

`status.sh` uses the same labels, so the two outputs read alike.

### It reads through to the CSP

cb-tumblebug serves its list endpoints from its own key-value store without
asking the CSP anything. A single-resource `GET` is different: it calls
cb-spider, overwrites `endpoint`, `publicAccess` and `status` from the live
answer, and stores the record back.

So `conn-info.sh` lists once and then re-reads each managed database, bucket and
infrastructure of yours singly. Costs one call per resource this setup created,
and means what it prints is what the CSP has rather than what was last written
down.

The infrastructure is re-read for a second reason: **the list leaves
`nodeUserName` null and the single read fills it in.** That field is the account
the node accepts, and it is the same field cm-centipede reads when it resolves a
`beetleSsh` connection — so the `ssh` command printed here and the login
cm-centipede performs are the same user, rather than a guess that agrees with it
only on cb-tumblebug's default image.

---

## NCP public domains — `ncp-db-domain.sh`

A managed database on NCP answers on a **private** domain until a public one is
issued, and issuing it is a console action with no API behind it.
`BEETLEENV_NCP_DB_PUBLIC_ACCESS=true` is carried into the create call, but
whether a public domain exists is a separate fact only the console decides.

```bash
./scripts/ncp-db-domain.sh          # refresh, then report
./scripts/ncp-db-domain.sh --wait   # keep watching while you work the console
```

```
==> ncp: refreshing managed DB records from the CSP
  MISSING   mysql    still on the private domain (db-4a4pov.vpc-cdb.ntruss.com:3306)

[WARN]  1 engine(s) have no public domain.

  Issue one per engine, in the NCP console:
    Database > Cloud DB for <engine> > select the DB server
      > DB Management > Public domain > request
```

It exits 1 while any engine is still without one, so it can gate a script that
needs the endpoint to be reachable.

The verdict is not a guess at the hostname. cb-spider's NCP driver uses the
public domain when there is one and the private one otherwise, and sets
`publicAccess` to say which it used; cb-tumblebug copies that field on refresh.
So `publicAccess` is an observation of what the CSP has, not an echo of what was
asked for.

A public domain has to be re-issued whenever the instance is re-created.

---

## Step 6 — `status.sh`

```bash
./scripts/status.sh        # every CSP in BEETLEENV_CSPS
./scripts/status.sh aws    # one CSP
```

```
==> nsId cpbt01

==> aws — connection aws-ap-northeast-2
    rdbmsId    cpbt-aws-db-mysql  [mysql Available]
               cpbt-aws-db-mariadb  [mariadb Available]
    osId       cpbt-aws-bucket  [Available]
    infraId    cpbt-aws-infra  [Running, 1 node]
    vNetId     cpbt-aws-vnet  [Available]

==> ncp — connection ncp-kr
    ...

  9 resource(s) named cpbt-<csp>-* are up.
```

Read-only: it deletes nothing, creates nothing and does not touch the namespace.
Everything here costs money while it runs, so "what is still up" is worth being
able to ask cheaply at the end of a day — and separately from deciding to delete
it.

**A summary, not a detail view.** Every CSP at once, one line per resource: its
id, and whether it is running. Nothing about how to reach it — no endpoint, no
IP, no CSP resource id, no per-node rows. That is `conn-info.sh`, one CSP at a
time. Keeping the split visible in the output is what stops the two from becoming
the same command printed twice.

**Other prefixes are counted, not hidden.** Everything above is filtered on
`<prefix>-<csp>-`, so after `BEETLEENV_NAME_PREFIX` changes the old setup drops
out of every script's view while it keeps running. `status.sh` ends with what
other prefixes still hold in the namespace:

```
==> other prefixes in cpbt01 - not shown above, never touched by deprovision.sh
    cpbt       aws: 1 infraId, 1 osId, 1 vNetId
  set BEETLEENV_NAME_PREFIX to one of these - in .env, or in a copy passed as
  ENV_FILE=... - to see them in full or delete them.
```

| | `status.sh` | `conn-info.sh` |
|---|---|---|
| Scope | every CSP by default | one CSP, required |
| Answers | what exists, what is costing money | how to connect to it |
| Secrets | never | `--reveal` (printed), `--ssh` (written 0600) |
| Modes | one | default, `--reveal`, `--ssh`, `--ids`, `--json` |

The labels are the same in both, so a resource is recognisable across them.

The list calls are made once and filtered per CSP rather than once per CSP,
because beetle paces its calls to cb-tumblebug and one namespace holds them all
anyway.

---

## Step 7 — `deprovision.sh`

```bash
./scripts/deprovision.sh aws database [--engine mysql]
./scripts/deprovision.sh aws all           # one CSP: vm, then database, then bucket
./scripts/deprovision.sh all all           # every CSP in BEETLEENV_CSPS
./scripts/deprovision.sh aws all --force
```

**Everything beetleenv deletes, it deletes from here.** `status.sh` only reports
and `provision.sh` only creates.

**The namespace is never deleted**, whatever is passed. `up.sh` creates it once
and it outlives every resource in it, so a teardown can be followed straight by
another provision — and anything else sharing the namespace is unaffected either
way. If you do want it gone, that is a cb-tumblebug operation:
`curl -X DELETE ${TUMBLEBUG_URL}/ns/${BEETLEENV_NS}`.

**What exists is read from the list APIs, not from `state/`.** A state file that
was never written, or was written by a run that then failed, must not be able to
leave a database running. Anything named `<prefix>-<csp>-*` is deleted; anything
else in the namespace is left alone.

`all` deletes in dependency order — vm, then database, then bucket.

### The last one out takes the network

There is no `deprovision.sh <csp> network`, just as there is no
`provision.sh <csp> network`. Instead **every run ends by checking whether
anything of ours is still inside the vNet, and removing it once nothing is:**

```
[OK]    deleted RDBMS cpbt-aws-db-mysql
[INFO]  keeping the network - still used by: cpbt-aws-infra
```

```
[OK]    deleted infra cpbt-aws-infra
[OK]    deleted security group cpbt-aws-sg
[OK]    deleted vNet cpbt-aws-vnet
```

That is the only safe rule available. "Delete the network" cannot be a step in a
teardown, because the vNet is shared: deleting it while a database sits in it
would either be refused by the CSP or strand the database. What it can be is a
consequence — whichever of the database and the VM leaves last takes the network
with it.

**Buckets do not count as occupants.** An object storage bucket sits outside the
vNet, so a leftover bucket never keeps a network alive.

**A vNet that outlived everything else** — from an interrupted run, or from
deleting the database and the VM in two separate commands — is cleaned up by
running `deprovision.sh <csp> all` again. It finds nothing to delete and does the
network sweep, which is why that sweep runs on every path including one that
deleted nothing.

**A list call that fails keeps the network.** An unread list is not an empty one,
and mistaking the two would delete a vNet around a live database.

**`all all` asks for confirmation**, and only that combination: a named CSP or a
named resource is already an explicit statement of what to delete, while
"everything, everywhere" is the one that can be typed by mistake. `--yes` skips
the prompt for scripted teardowns.

**The RDBMS delete is synchronous.** cm-beetle declares `Prefer: respond-async` on
the infra and object storage deletes and not on that one, so the call is simply
held open for as long as the CSP takes. Minutes on AWS, longer on NCP. It is not
hung.

---

## `state/`

`state/<ns>/<prefix>/<csp>/*.json` holds the recommendation each resource was
created from — what the APIs cannot give back. It is advisory: a missing state
file never blocks a deprovision.

**No secrets.** The password is injected after the recommendation is written to
disk, exactly so that a leaked state file grants nothing.

---

## `keys/`

`keys/<ns>/<prefix>/<csp>/<sshKeyId>.pem` holds the VM private keys
`conn-info.sh --ssh` saved, 0600 inside 0700 directories. Laid out like `state/`,
so another setup's keys are never in the way. Gitignored, like `.env`.

**This is the one place besides `.env` where a secret sits at rest**, which is
why it is a directory of its own rather than a corner of `state/`. cb-tumblebug
generates the key pair and hands the private half over only when asked, so a file
here is the only copy outside tumblebug — and the only way into the node.

`deprovision.sh <csp> vm` deletes this setup's key file along with the key pair
itself, and nothing else in the directory. Nothing
else writes to the directory, and `conn-info.sh --ssh` recreates whatever is
missing.

---

## PostgreSQL and MongoDB

Neither can be created through cm-beetle today.

**PostgreSQL** is the near one. cb-tumblebug and cb-spider already carry it —
`./scripts/catalog.sh ncp engines` lists it — but cm-beetle's managed RDBMS model
declares `dbEngine` as `enums:"mysql,mariadb"` and maps anything else to mysql.
So beetleenv refuses it explicitly:

```
[ERROR] engine 'postgresql' is not supported by cm-beetle yet.
        Its managed RDBMS API declares enums:"mysql,mariadb", and a request for
        anything else is answered with a mysql recommendation instead of an error.
```

When beetle's engine list grows, `BEETLEENV_SUPPORTED_ENGINES` in
[`scripts/lib/recommend.sh`](scripts/lib/recommend.sh) is the one line that has to
follow; the port is already in `engine_port` and the firewall rule already opens
it.

**MongoDB** is further off. It is not an RDBMS, and there is no managed NoSQL
resource anywhere in the stack yet.
