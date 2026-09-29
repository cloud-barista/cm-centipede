# cm-centipede OpenTofu-Based Migration Test Environment

Create and destroy **object storage buckets, databases and VMs** on **AWS** and
**NCP (Naver Cloud Platform)** using OpenTofu, with credentials held in OpenBao.

These resources exist to give cm-centipede something real to migrate. Each resource
type is provisioned and destroyed **independently** — you can create just a bucket,
just a VM, or just the databases.

---

## Support matrix

| CSP | vm<br>(for filesystem testing) | objectStorage | mysql | mariadb | postgresql | mongodb |
|---|:--:|:--:|:--:|:--:|:--:|:--:|
| aws | ✅<br>(+ NFS via EFS, optional) | ✅ | ✅ | ✅ | ✅ | — |
| ncp | ✅ | ✅ | ✅ | — | ✅ | ✅ |

Every database is a managed service. Each CSP is left with the engines it offers:
AWS has no managed MongoDB and NCP has no managed MariaDB, so those two cells are empty.
The AWS VM can also mount an EFS file system over NFS, as an option of the `vm`
resource rather than a resource of its own — see [Step 3](#step-3--provision-resources).

---

## Quick start (AWS)

```bash
cd tofuenv
cp .env.example .env && chmod 600 .env    # then fill in AWS keys, a DB password, a bucket name
./scripts/up.sh               # start OpenBao, store credentials, start the tofu runner
./scripts/provision.sh aws bucket
./scripts/conn-info.sh aws bucket
./scripts/gen-data.sh --provider aws --target bucket
./scripts/deprovision.sh aws bucket    # always clean up - these resources cost money
./scripts/down.sh
```

NCP needs three extra things. See [Step 4](#step-4--ncp-only-issue-the-public-domain)
and the [AWS vs NCP](#aws-vs-ncp-at-a-glance) table.

---

## How it fits together

```
 Step 0  Prerequisites     Docker + CSP access keys
            |
 Step 1  .env              cp .env.example .env && chmod 600 .env  ->  fill in keys
            |
 Step 2  Start             ./scripts/up.sh
            |                └ start OpenBao -> store credentials -> blank .env keys -> start runner
            |
 Step 3  Provision         ./scripts/provision.sh aws bucket      (or vm / database)
            |                └ prints connection info when it finishes
            |                └ for NCP, the network module is created first (automatically)
            |
 Step 4  Public domain     NCP managed databases only: request it once in the console,
            |              then ./scripts/ncp-db-domain.sh
            |
 Step 5  Test data         ./scripts/gen-data.sh --target all      (optional)
            |
 Step 6  Connect           ./scripts/conn-info.sh                  (all connection info at once)
            |
 Step 7  Destroy           ./scripts/deprovision.sh aws bucket
            |                └ for NCP, the network module is removed once nothing needs it
            |
 Step 8  Shut down         ./scripts/down.sh
```

Credentials never reach a tofu module as an environment variable: `up.sh` writes them
into OpenBao, blanks them in `.env`, and each module reads them at apply time through
the `vault` provider.

---

## AWS vs NCP at a glance

| | AWS | NCP |
|---|---|---|
| Bucket | S3 | Object Storage |
| VM login account | `ubuntu` | **`root`** |
| MySQL / PostgreSQL | RDS | Managed Cloud DB |
| MongoDB | **not provisioned** | Managed Cloud DB (STAND_ALONE) |
| MariaDB | RDS | **not provisioned** |
| Network | default VPC, nothing to do | **a VPC is required — created and destroyed automatically** |
| Reaching a DB from outside | works immediately | **a public domain must be requested once, in the console** |
| DB engine version format | major.minor (`8.0`) | **full version (`8.0.36`)** |
| Managed DB provisioning time | a few minutes | **about 30 minutes** |

---

## Step 0 — Prerequisites

**Tools**

```bash
docker --version            # Docker Desktop, or Docker on Linux
docker compose version
jq --version                # credential registration and Step 5
curl --version
go version                  # only needed for Step 5 (test data)
```

**Credentials** — you only need keys for the CSP you actually use.

| CSP | Keys | Where to get them |
|---|---|---|
| AWS | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | IAM user with S3, RDS and EC2 permissions (plus EFS for the VM's optional NFS) |
| NCP | `NCP_ACCESS_KEY`, `NCP_SECRET_KEY` | Portal > My Page > Manage Authentication Key |

NCP Object Storage uses the same key pair as the rest of NCP — there is no separate
S3 credential.

---

## Step 1 — Create `.env`

`.env` holds your keys and settings. It is covered by `.gitignore`; never commit or
share it.

```bash
cd tofuenv
cp .env.example .env && chmod 600 .env
```

**Mode `600` is required, not advised.** `up.sh` and `register-creds.sh` refuse
to read a `.env` that anyone else can. `VAULT_TOKEN` is the OpenBao **root**
token and it stays in the file permanently — anything that can read it can read
every credential you stored — and your CSP keys sit there in plaintext until
Step 2 registers them.

Then open `.env` and fill in the values. The template documents every key; the
minimum for AWS is:

```dotenv
# Credentials - blanked automatically once stored in OpenBao (Step 2)
AWS_ACCESS_KEY_ID=your_access_key_id
AWS_SECRET_ACCESS_KEY=your_secret_access_key
AWS_DB_PASSWORD=a_strong_db_password

# Settings - kept as is
TF_VAR_aws_region=ap-northeast-2
TF_VAR_aws_name_prefix=cptf                             # short for "centipede tofu"
TF_VAR_aws_bucket_name=cptf-aws-bucket-yourname-20260804   # must be globally unique
TF_VAR_aws_db_name=testdb
TF_VAR_aws_db_username=dbadmin
```

For NCP, add:

```dotenv
# Credentials - blanked automatically once stored in OpenBao (Step 2)
NCP_ACCESS_KEY=your_access_key
NCP_SECRET_KEY=your_secret_key
NCP_DB_PASSWORD=Cent1pede!2024        # strict rules, see below

# Settings - kept as is
TF_VAR_ncp_region=KR
TF_VAR_ncp_zone=KR-2
TF_VAR_ncp_name_prefix=cptf           # short for "centipede tofu"
TF_VAR_ncp_bucket_name=cptf-ncp-bucket-yourname-20260804
TF_VAR_ncp_db_name=testdb
TF_VAR_ncp_db_username=dbadmin
TF_VAR_ncp_mysql_version=8.0.36       # full version string, not 8.0
TF_VAR_ncp_postgres_version=14.22
TF_VAR_ncp_mongodb_version=7.0.28
```

**The name prefix** is short for **centipede tofu** and names every resource this
environment creates (`cptf-vm`, `cptf-mysql`, `cptf-rds-sg`, `cptf-vpc`, ...), so they
are easy to tell apart from anything else in the account. Each CSP has its own key, and
they may hold different values:

| Key | Length limit | Why |
|---|---|---|
| `TF_VAR_aws_name_prefix` | 2-20 | An RDS identifier is capped at 63 and gets a `-postgres` suffix here |
| `TF_VAR_ncp_name_prefix` | 2-10 | The managed MongoDB `service_name` is capped at 15, and `cptf-mongodb` is already 12 |

Lowercase letters, digits and hyphens, starting with a letter; a violation is rejected at
plan time. **Buckets are not covered by the prefix** — an S3 name has to be globally
unique, so `TF_VAR_aws_bucket_name` and `TF_VAR_ncp_bucket_name` are set in full.

Keep some room below those limits: NCP login keys append a role and a random suffix on
top of the prefix (`cptf-vm-a1b2c3`), and the longest generated name is the prefix plus
11 characters (`-postgresql`, `-server-acg`).

Set the prefix **before** the first provisioning. Changing it later renames resources,
which for RDS and EC2 means **replacement, and the data in them is lost**. On NCP it also
breaks the `vm` and `database` modules, which look the VPC, subnet and ACG up **by name**.
To change it on an already provisioned environment, see
[Changing the NCP name prefix afterwards](#changing-the-ncp-name-prefix-afterwards).

Five things reliably go wrong here:

**Bucket names must be unique.** An S3 bucket name is globally unique across all AWS
accounts, so common names are already taken and the apply fails with
`BucketAlreadyExists`. Append a date or your own identifier. Lowercase letters, digits
and hyphens only, 3-63 characters. NCP bucket names must be unique within the region.

**Use `dbadmin`, not `admin`, as the DB username.** PostgreSQL reserves `admin` and
`rdsadmin`, so an apply using them fails. `dbadmin` is safe on all engines and is the
default.

**The NCP DB password rules are enforced.** A violation is rejected by `up.sh` before
anything is provisioned:

- 8 to 20 characters
- at least one letter, one digit and one special character
- allowed special characters: `~ ! @ # $ % ^ * ( ) - _ = [ ] { } ; : , . < > ?`
- forbidden: `` ` `` `&` `+` `\` `"` `'` `/` and whitespace
- example: `Cent1pede!2024`

**An AWS engine version the region does not offer fails the apply.** RDS is looser than
NCP about the format — it accepts both a prefix (`8.0`) and a full version (`8.0.42`),
and resolves a prefix to the current minor release, which the provider tracks separately
in `engine_version_actual`, so a prefix causes no replacement. The value still has to
exist in the region; list what it offers with `./scripts/aws-db-versions.sh` once Step 2
is done.

**TLS is enforced by some engine versions, not by others.** RDS MariaDB sets
`require_secure_transport=ON` from 11.8, and RDS PostgreSQL sets `rds.force_ssl=1`
from 15; MySQL never does by default. With the default versions (`8.0` / `10.6` /
`14`) a plaintext client connects to all three. Raise one of those versions and it
stops: set `TF_VAR_aws_secure_transport=off` to attach a parameter group that allows
plaintext, or connect with TLS instead.

The value is **fixed at creation time** — the parameter group is attached as the
instance boots, so no reboot is ever needed. Changing it afterwards means
`deprovision.sh aws database` followed by `provision.sh aws database`; a plain
`--force` re-apply would attach the group to a running instance and leave PostgreSQL
waiting for a reboot that never comes.

**NCP engine versions must be full version strings** — `8.0.36`, not `8.0`. The
provider normalises state to the full version it gets back from the API, and the field
forces replacement, so a partial version makes **every** subsequent plan want to
re-create the database (about 30 minutes each time). A wrong value is caught at plan
time, not after the apply. List the supported versions with
`./scripts/ncp-db-versions.sh` once Step 2 is done.

Leave `VAULT_TOKEN` empty — Step 2 fills it in.

---

## Step 2 — Start the stack

```bash
./scripts/up.sh
```

This does four things:

1. Starts the **OpenBao** container (the credential vault).
2. **Initialises** it on first run, **unseals** it on later runs.
3. **Stores** the `.env` credentials in OpenBao, then **blanks those keys in `.env`**
   so no plaintext key stays on disk.
4. Starts the **tofu-runner** container, which every script drives via `docker exec`.

On success:

```
=== Ready ===
  Provision  :  ./scripts/provision.sh <csp> <resource>
```

| Flag | Effect |
|---|---|
| *(none)* | Register credentials, then blank them in `.env` |
| `--keep-env` | Register credentials but leave the `.env` values in place |
| `--no-register` | Skip credential registration entirely |

Credentials already in OpenBao are **not** overwritten by blank `.env` values, so
re-running `up.sh` is safe.

---

## Step 3 — Provision resources

```
./scripts/provision.sh <csp> <resource> [--force]
```

| CSP | Valid resources |
|---|---|
| `aws` | `bucket`, `vm`, `database` |
| `ncp` | `bucket`, `vm`, `database` |

**Running the same command twice is safe.** A resource that already holds tofu state is
reported and skipped, so nothing is created and nothing is charged:

```
=== aws/bucket is already provisioned - nothing to do ===
  bucket_name = "cptf-aws-bucket-test"
  ...
  Connection info  :  ./scripts/conn-info.sh aws bucket
  Re-apply anyway  :  ./scripts/provision.sh aws bucket --force
  Destroy          :  ./scripts/deprovision.sh aws bucket
```

Use `--force` to apply anyway. That is what you want when a previous apply stopped
halfway (state exists but some resources are missing) or when a `.env` value changed —
on NCP, remember that changing a managed DB variable re-creates the database.
`TF_VAR_aws_secure_transport` is the one AWS value `--force` cannot apply properly —
see [Step 1](#step-1--create-env).

**AWS**

```bash
./scripts/aws-db-versions.sh           # optional: list engine versions, instance classes, AMI
./scripts/provision.sh aws bucket
./scripts/provision.sh aws vm          # Ubuntu 22.04, for filesystem migration tests
./scripts/provision.sh aws database    # RDS: MySQL + MariaDB + PostgreSQL
```

**AWS vm with NFS (EFS)** — set `TF_VAR_aws_nfs_enabled=true` before `provision.sh aws vm`
and the VM comes with an EFS file system, mounted over NFS (v4.1) on first boot:

```dotenv
TF_VAR_aws_nfs_enabled=true
#TF_VAR_aws_nfs_mount_path=/home/ubuntu/testdata    # where EFS is mounted
#TF_VAR_aws_data_path=/home/ubuntu/testdata         # where gendata writes (Step 5)
```

The two paths are **independent**; they just share a default. Left alone, the test
data lands on the NFS mount, so a filesystem migration reads from NFS. Point either one
elsewhere and the data stays on the local disk while EFS is mounted on its own.

- The mount path must not be `/`, `/home`, `/home/ubuntu` or a system directory —
  mounting over the home directory hides `.ssh/authorized_keys` and locks SSH out.
  A violation is rejected at plan time.
- EFS is reachable from inside the VPC only. A mount target goes into every default
  subnet, and its security group accepts NFS (2049) from the VM alone.
- The mount happens in cloud-init, **after** `provision.sh` returns — give it a couple of
  minutes before Step 5. Check it with `ssh ... cloud-init status --wait` and
  `ssh ... mountpoint <mount path>`.
- **Fixed at creation time.** Changing either `aws_nfs_*` value on a provisioned VM
  takes `provision.sh aws vm --force`, which **re-creates the VM** — anything on its local
  disk is lost. EFS and everything on it are destroyed with `deprovision.sh aws vm`.

**NCP** — the same three resources, in any order:

```bash
./scripts/ncp-db-versions.sh           # optional: list engine versions, images and specs
./scripts/provision.sh ncp bucket
./scripts/provision.sh ncp vm
./scripts/provision.sh ncp database    # managed: MySQL + PostgreSQL + MongoDB
```

NCP has no default VPC, so `vm` and `database` resolve a VPC, PUBLIC subnet and ACG
**by name** from the `network` module. You never provision that module yourself:
whichever of the two you run first creates it, and Step 7 removes it once nothing
needs it any more.

Connection info is printed automatically when an apply finishes.

**How long to expect:**

| Resource | Time |
|---|---|
| bucket (either CSP) | seconds |
| the automatic `ncp network` step | about a minute, added to the first `ncp vm` or `ncp database` |
| vm (either CSP) | a few minutes |
| `aws database` | several minutes |
| `ncp database` | **about 30 minutes** — do not interrupt it |

The NCP managed database resources have no update path: every attribute forces
replacement. Changing one variable re-creates the database and costs another 30
minutes, so get `.env` right before applying.

---

## Step 4 — NCP only: issue the public domain

NCP managed databases (MySQL, PostgreSQL, MongoDB) get **two** addresses: a private
domain that only resolves inside the VPC, and a public domain for everything else.
The public domain can only be requested **from the console** — there is no API for it,
so it cannot be automated. Until you request it, the `*_host` outputs are empty and
nothing outside the VPC can connect.

```
NCP console > Database > Cloud DB for <engine> > select the DB server
  > DB Management > Public Domain Management > Request      (once per engine, so 3 times)
```

Then pull it into the tofu state and verify:

```bash
./scripts/ncp-db-domain.sh      # refresh state + report per-engine status
```

- The PUBLIC subnet created by the `network` module is a **prerequisite** for
  requesting a public domain.
- If you destroy and re-create a database, you must **request the domain again**.
- All three NCP engines are managed, so **none of them is exempt** — each needs its own
  public domain request.

---

## Step 5 — Load test data (optional)

Fills the provisioned resources with dummy and seed data by building and running
[`gendata`](gendata/).

| Target | What it does |
|---|---|
| `bucket` | Uploads dummy files (CSV, TXT, JSON, XML, SQL, PNG, GIF, ZIP) |
| `filesystem` | Transfers dummy files to the VM over SFTP |
| `database` | Loads the `shop_db` schema and seed data into whichever engines the provider has (AWS: MySQL, MariaDB, PostgreSQL / NCP: MySQL, PostgreSQL, MongoDB) |

**Prerequisites:** the runner container is up (Step 2), the target resources exist
(Step 3), `go` is installed, and for NCP managed databases the public domain has been
issued (Step 4) — engines without one are skipped with a warning.

**The filesystem target writes to `data_path`**, the vm module's output —
`/home/ubuntu/testdata` on AWS, `/root/testdata` on NCP, following each image's
login account. `conn-info.sh <csp> vm` prints it, and that path is the source a
filesystem migration reads from. Override it with `TF_VAR_aws_data_path` /
`TF_VAR_ncp_data_path`, to somewhere that account can write — SFTP does not sudo.

**Where the values come from.** `gen-data.sh` assembles the connection info and
pipes it into gendata; gendata itself looks nothing up. Addresses and the database
password come from `tofu output -json` inside the runner, the object-storage key
pair from OpenBao (`secret/csp/<csp>` — `.env` no longer holds it after Step 2),
and `VAULT_ADDR`/`VAULT_TOKEN` from `.env`. Only the modules your `--target` needs
are read, so `--target bucket` does not require a VM.

It goes over **stdin, never a file**: that JSON carries the database password and
the object-storage secret key, and a file would leave both on disk after the run.
Every environment that drives gendata hands its inputs over the same way, which is
what lets gendata have a single input path.

```bash
./scripts/gen-data.sh --target all                    # bucket + VM + DB on AWS
./scripts/gen-data.sh --provider ncp --target all     # same, on NCP
./scripts/gen-data.sh --target bucket
./scripts/gen-data.sh --target database --engine mysql
./scripts/gen-data.sh --target all --dry-run          # generate locally, upload nothing
./scripts/gen-data.sh --target bucket --cleanup       # the reverse: empty the bucket
./scripts/gen-data.sh -h                              # full option list
```

| Option | Default | Values |
|---|---|---|
| `--target` | `all` | `bucket`, `filesystem`, `database`, `all` |
| `--engine` | `all` | `mysql`, `mariadb`, `postgresql`, `mongodb`, `all`. An engine the provider does not serve has no host output and is skipped |
| `--provider` | `aws` | `aws`, `ncp` |
| `--dry-run` | off | Generate dummy files only; skip upload, transfer and load |
| `--force` | off | Skip the existing-data check (allows overwrite and `DROP`) |
| `--cleanup` | off | Delete instead of generate. `--target bucket` only, and it empties the **whole** bucket |

**How much data** — [`gendata/config/config.json`](gendata/config/config.json), the
`dummy` block. Sizes are **in MB** and ship as `1` (roughly one 1 MiB file per format).
`N` means `N` files of about 1 MiB each, so `"sizeCSV": 10` is about 10 MB of CSV.
Set a format to `0` to skip it. `layout` controls folder depth and breadth;
`objectStorage` and `filesystem` control paths and concurrency.

**Safety** — gendata refuses to run if the target already holds data, because the
database fixtures are destructive (they include `DROP`). Use `--force` to override.
Paths are fixed rather than per-run, so re-running overwrites the same location instead
of accumulating copies. A summary of the last run is written to
`gendata/runs/last-run.json`.

---

## Step 6 — Get connection info

### `conn-info.sh` — everything at once (recommended)

```
./scripts/conn-info.sh [aws|ncp] [network|bucket|vm|database|all] [--reveal]
```

```bash
./scripts/conn-info.sh                        # all AWS resources, secrets masked
./scripts/conn-info.sh ncp all
./scripts/conn-info.sh aws database
./scripts/conn-info.sh ncp database --reveal  # print passwords and URIs in clear text
```

| Argument | Default | Values |
|---|---|---|
| csp | `aws` | `aws`, `ncp` |
| resource | `all` | `bucket`, `vm`, `database`, `all`, plus `network` for NCP |
| `--reveal` | off | Print sensitive values in clear text instead of `<sensitive>` |

Resources that do not exist yet are skipped. For NCP `database`, a warning is printed
for any engine whose public domain has not been issued.

`--reveal` prints passwords and connection URIs to your terminal — be careful with
scrollback, logs and screen sharing.

### Connecting to a VM

Copy the `ssh_command` output and run it from the `tofuenv` directory:

```bash
ssh -i ssh_keys/aws-vm.pem ubuntu@3.35.xxx.xxx     # AWS: user is ubuntu
ssh -i ssh_keys/ncp-vm.pem root@223.130.xxx.xxx    # NCP: user is root
```

The private key is generated by tofu and written to `ssh_keys/` with mode 600. It is
gitignored; never commit it.

### Reading one output directly

`conn-info.sh` masks sensitive values unless you pass `--reveal`. To read a single
value yourself:

```bash
docker exec tofuenv-runner bash -c \
  'cd /work/tofu/aws/database && tofu output -raw mysql_connection_uri'
```

Replace `aws` with `ncp`, and `database` with `bucket`, `vm` or `network`. Drop `-raw`
for non-sensitive outputs. Output names are the **same on both CSPs**, so anything
reading them stays CSP-agnostic:

| Module | Outputs |
|---|---|
| bucket | `bucket_name`, `bucket_region`; AWS also `bucket_arn`, `bucket_domain_name`; NCP also `s3_endpoint`, `s3_signing_region` |
| vm | `public_ip`, `ssh_user`, `key_file`, `ssh_command`, `data_path`; AWS also `nfs_enabled`, `nfs_file_system_id`, `nfs_dns_name`, `nfs_mount_path`; NCP also `private_ip`, `login_key_name` |
| database | `db_name`, `db_username`, `db_password`; per engine `<engine>_host`, `<engine>_port`, `<engine>_connection_uri` where `<engine>` is `mysql`, `mariadb` or `postgres` on AWS, and `mysql`, `postgres` or `mongodb` on NCP |
| database, NCP managed | `<engine>_public_domain`, `<engine>_private_domain`, `<engine>_acg_no` |

Sensitive outputs (`db_password`, every `*_connection_uri`) need `-raw`. On NCP,
`<engine>_host` **is** the public domain, so it and the URI are empty until Step 4 is
done; `<engine>_private_domain` only resolves inside the VPC.

---

## Step 7 — Destroy resources

**Destroy what you provisioned. RDS instances, servers and public IPs are billed.**

```
./scripts/deprovision.sh <csp> <resource>
```

```bash
./scripts/deprovision.sh aws database
./scripts/deprovision.sh aws vm
./scripts/deprovision.sh aws bucket
```

On NCP, the same three resources — the `network` module is not one of them:

```bash
./scripts/deprovision.sh ncp database
./scripts/deprovision.sh ncp vm
./scripts/deprovision.sh ncp bucket
```

**`ncp bucket` is emptied first.** NCP's bucket resource has no `force_destroy` — the
one `tofu/aws/bucket` sets on `aws_s3_bucket` — and the S3 API refuses to delete a
bucket that still holds objects:

```
Error: DELETING ERROR
operation error S3: DeleteBucket, https response error StatusCode: 409,
api error BucketNotEmpty: The bucket you tried to delete is not empty.
```

So the script runs `./scripts/gen-data.sh --provider ncp --target bucket --cleanup`
before the destroy, which needs **go on the host**. Without it — or if the cleanup
fails — you are told so and the destroy still runs: an empty bucket goes either way.
If it comes back with `BucketNotEmpty`, empty the bucket in the console (incomplete
multipart uploads included) and run the command again.

A destroy that fails is **retried once, 60 seconds later** (`destroy failed — retry 1/1
in 60s`). NCP answers some delete calls with a 500 while the resource is still being
released, and the same call then goes through. If the retry fails too, the error is
printed again at the end and the command exits non-zero.

A module that tracks no resource is reported and skipped rather than destroyed —
`ncp/database holds no resources - nothing to destroy.` The plan alone would still read
the by-name lookups, and those are exactly what fails after a prefix change.

Because only `vm` and `database` reference the VPC, the script destroys the `network`
module for you as soon as **neither of them is left**. Destroy them in either order;
whichever goes last takes the VPC, subnet and ACG with it. While one of them is still
provisioned, you will see `Keeping ncp/network: another module still uses it.`

If NCP has not finished releasing the servers, that final network destroy can fail.
It is reported as a **warning**, not an error — the resource you asked for is already
gone — and re-running the same command a few minutes later finishes the job.

If an NCP managed database destroy ends in `WAITING FOR DELETE ERROR`, the delete
request already went through — **run the same command again** and it will finish. The
provider's delete wait is hard-coded (5 minutes for MySQL, 10 for PostgreSQL and
MongoDB) and sometimes expires first.

### Changing the NCP name prefix afterwards

`TF_VAR_ncp_name_prefix` is baked into the names `vm` and `database` search for, so
changing it once `ncp/network` exists makes every lookup come back empty. Both scripts
detect this and say so up front instead of letting tofu fail on an `Invalid index`:

```
=== TF_VAR_ncp_name_prefix does not match the existing ncp/network ===
  ncp/network was created as : cmcp-vpc
  ncp/vm will look for       : cptf-vpc
```

What to do depends on whether anything is still provisioned:

**Nothing left but the network** — destroy it and let the next provisioning rebuild it
under the new name. The VPC, subnet and ACG hold no data, so this costs about a minute:

```bash
./scripts/deprovision.sh ncp vm       # vm holds nothing, so this only clears the network
./scripts/provision.sh ncp vm         # re-creates the network under the new prefix
```

**A VM or database is still provisioned** — the destroy is planned against the very same
by-name lookups, so it cannot run while the names disagree. Put the **old** prefix back in
`.env` first, destroy everything, then set the new one:

```bash
# .env: TF_VAR_ncp_name_prefix=<the old value the script printed>
./scripts/deprovision.sh ncp database
./scripts/deprovision.sh ncp vm
# .env: TF_VAR_ncp_name_prefix=<the new value>
./scripts/provision.sh ncp vm
```

On AWS there is no such lookup — the modules only *name* resources — so changing
`TF_VAR_aws_name_prefix` never blocks a destroy. It does replace the RDS instances and
EC2 machines on the next apply, and their data goes with them.

### NCP login keys that will not delete

An NCP destroy can get everything except the login key:

```
Error: Error Deleting LoginKey
Status: 500 Internal Server Error, Body: {
  "responseError": { "returnCode": "1300",
    "returnMessage": "Please try your call again later.\nTemporarily out of service." } }
```

`1300` is NCP's catch-all failure. It is usually described as transient — the server the
key belonged to is still being released — but it has been observed **unchanged over 12
minutes of retries**, and calling `deleteLoginKeys` directly returns the same thing, so
it is not something the scripts can wait out.

So they do not try. This is the one failure `deprovision.sh` does **not** retry: once the
login key is all that is left, the 60-second retry would only spend a minute to fail
identically. Recovery is immediate, in two reported steps:

```
[1/2] dropping the key from state
        tofu state rm ncloud_login_key.vm   (cptf-vm-a1b2c3)
[2/2] re-running destroy to clear the module's outputs
```

**Forgetting the key is safe because the names are unique.** The login key is named
`<prefix>-vm-<random>` (a `random_id` resource in `tofu/ncp/vm`), so a key left behind at
NCP cannot collide with the one the next provisioning asks for. That is the whole reason for the suffix: `ncloud_login_key`
refuses to create a name that already exists, and without the suffix one undeletable key
would block the module forever.

The cost is that leftovers accumulate in the account. They are harmless and free, but
worth clearing out now and then:

```
NCP console > Services > Compute > Server > Login Key   (Classic platform, not VPC)
```

To tell which key belongs to a live resource, read the output rather than guessing the
name:

```bash
docker exec tofuenv-runner bash -c \
  'cd /work/tofu/ncp/vm && tofu output -raw login_key_name'
```

The matching `ssh_keys/ncp-vm.pem` is a `local_sensitive_file` resource, so a destroy
already removed it — if the file is still there, delete it, because a re-created key
issues a new private key.

---

## Step 8 — Shut down

```bash
./scripts/down.sh          # stop containers, keep vault data and stored credentials
./scripts/down.sh --wipe   # also delete vault data: credentials must be re-entered
```

### Coming back later

```bash
./scripts/up.sh
```

The vault is simply unsealed; your credentials are already stored, so there is nothing
to re-enter and the blank keys in `.env` are fine as they are. To **change** a
credential, put the new value in `.env` and run `./scripts/register-creds.sh`.
