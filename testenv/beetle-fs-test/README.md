# Filesystem Migration Matrix — cm-honeybee + cm-centipede over cm-beetle

Migrates **a whole source filesystem onto a freshly created node on every
configured CSP**, where the source is a container this folder builds and seeds
and the target is a VM provisioned with **cm-beetle**. The outcome is reported
as a table.

```
  csp      │ status  files      bytes        elapsed   destination
  ---------┼---------------------------------------------------------------
  aws      │ PASS    54         2,596,664    4m12s     /home/cb-user/testdata
  ncp      │ FAIL    -          -            1m45s
```

The migration itself is **cm-centipede**, driven over its REST API, with
**cm-honeybee** inspecting the source. This folder provisions, prepares and
judges; it does not migrate anything itself.

> **Four servers must already be running** — cm-honeybee and cm-centipede for the
> migration, cm-beetle and cb-tumblebug for the provisioning (with cb-spider
> behind them). This folder starts none of them, and steps 1 and 2 of every run
> check that all four answer before a single resource is created. `.env` points at
> them with `HB_BASE`, `CP_BASE`, `BEETLE_URL` and `TUMBLEBUG_URL`.

### Starting that stack — `make up`, `make init`, `make down`

The backing servers come up in one command, from the **repo root**. The Compose
stack in [`deployments/docker-compose`](../../deployments) runs **cm-honeybee on
8081, cm-centipede on 8085, cm-beetle on 8056 and cb-tumblebug on 1323** — what
the defaults in `.env.example` point at, so a stack started this way needs no URL
change here.

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
cd testenv/beetle-fs-test

cp .env.example .env && chmod 600 .env
# the defaults work against a deployments stack; set the regions and zones, then:

./scripts/fs-support.sh --recommend      # (optional) can beetle build a node here
./scripts/fs-matrix.sh --csp aws         # one cell
```

Start with a single cell. One cell exercises the whole path — image build, seed,
agent install, inspection, network, node creation, plan, migration, validation,
teardown — and costs one VM that lives for minutes. Widen once it goes green.

```bash
./scripts/fs-matrix.sh                   # every CSP in FS_CSPS
./scripts/fs-matrix.sh --csp "aws ncp"
./scripts/fs-matrix.sh --keep-on-fail    # leave a failed cell's node to look at
./scripts/fs-matrix.sh --cleanup         # reclaim what an interrupted run left
```

> ⚠ **`--keep-node` and `--keep-on-fail` leave a running VM.** It bills until
> `--cleanup` or the CSP console removes it. Everything else — a normal end, a
> Ctrl-C, a failure — removes the node on the way out.

---

## How it fits together

```
 Step 0  Prerequisites      docker, jq, curl, ssh, ssh-keygen
            |               + cm-honeybee and cm-centipede running
            |               + cm-beetle, cb-tumblebug and cb-spider running,
            |                 with the <csp>-<region> connection registered
 Step 1  .env               cp from .example, chmod 600, set regions and zones
            |
 Step 2  ./scripts/fs-support.sh          (optional) what can beetle do, and where
            |
 Step 3  ./scripts/fs-matrix.sh           the matrix itself
            |    ├ pre-flight  honeybee, centipede, beetle, tumblebug,
            |    │             connection, zone agreement, namespace
            |    ├ source      key pair -> build -> start -> seed -> read it back
            |    ├ collect     register the connection (installs the agent),
            |    │             one inspect, cached for the whole run
            |    └ per cell    network -> node -> probe -> plan -> migrate
            |                  -> validate -> delete node -> release network
 Step 4  logs/fs-matrix.log
         logs/fs-matrix-api.log
         logs/fs-matrix-result.json
            |
 Step 5  ./scripts/fs-matrix.sh --cleanup   only if a run was killed outright
```

---

## The source container

A source is **a machine, not a process**. `src/Dockerfile.fs` builds Ubuntu 22.04
with systemd as PID 1, so `systemctl` works, sshd is a unit, and the dataset is
built by a unit at boot — which is what an on-premises source is. It is
[`dockerenv`](../dockerenv)'s `compose/Dockerfile.filesystem` with one change:
**SSH key authentication replaces the password**.

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

`/tmp` being **executable** is not decoration. cm-honeybee's agent install SFTPs
a busybox and a shell script into `/tmp` and then runs them; a `noexec` mount
fails that with a permission error naming the script rather than the mount.

> ⚠ `--privileged` and a host cgroup mount. **Local testing only, never
> production** — the same warning `dockerenv` carries, for the same reason.

**One container serves the whole run.** The dataset is inspected once and every
CSP migrates that same inspection.

**Readiness is one signal.** `matrix-init.service` is `Type=oneshot` with
`RemainAfterExit=yes`, so the unit stays `active` after `init-fs.sh` returns.
That makes `systemctl is-active matrix-init.service` mean "the SSH key is
installed and the dataset is complete" — one question instead of two.

Container name `beetle-fs-test-fs-src`, published SSH port in the **34xxx** band
(`34922`), resource prefix `cpbfs` — so this folder, `beetle-os-test`,
`beetle-db-test`, `beetleenv`, `tofu-db-test` and `dockerenv` can all be up at
once.

### The dataset

`src/scripts/seed-fs.sh` is dockerenv's `03-setup-filesystem.sh`, file for file,
so a result here is comparable with a docker-to-docker run.

| Directory | Size | What is in it |
|---|--:|---|
| `media/` | 2.4M | binary — jpg x6, mp4 x2, mp3 x2 |
| `documents/` | 116K | reports, contracts, invoices — **plus two multibyte filenames** |
| `data/` | 84K | csv x6, json x5, xml x1 |
| `logs/` | 44K | application x6, syslog x1 |
| `scripts/` | 20K | shell scripts |
| `backups/` | 12K | tar.gz placeholders |

**54 files, about 2.6 MB, 18 directories three levels deep.** Text and binary
mixed, and three files are deliberately multibyte — `documents/한글_인코딩_검증.txt`,
`documents/日本語_エンコード検証.txt` and `data/json/metadata/utf8_encoding_check.json`
— two of them in the **filename**, not just the content. Every other file is
ASCII on purpose, so a transfer that mangles an encoding shows up in those three
and nowhere else.

The tree is rebuilt on every boot and its generated numbers use `$RANDOM`, so the
byte total moves a little between runs. Source and destination are always
compared within one run, so that changes nothing about the verdict.

---

## One cell, step by step

| Step | What happens |
|---|---|
| 0 | vNet with two subnets, security group (SSH only), SSH key |
| 1 | Recommend, inject the network, `POST /infra?useExisting=true`, wait, `ssh-ready` |
| 2 | Probe the node over SSH — `$HOME`, `rsync`, and the key beetle hands out |
| 3 | `POST /plans/target` — the source model from honeybee, the target as a `beetleSsh` ref |
| 4 | `POST /migration`, then poll until it settles |
| 5 | `POST /migration/{id}/validation`, then poll — this is the verdict |
| 6 | `GET /migration/{id}/logs`, delete the node, release the network |

**Step 1 comes first for a reason.** centipede creates nothing of its own:
`MigrateFileSystem` resolves both ends and hands them to transx-ex. The node has
to be there before the plan runs.

**The network has to exist before the node.** This is the main structural
difference from the object storage matrix, where a bucket sits outside the VPC and
is created on its own. beetle's recommendation leaves `vNetId`, `subnetId` and
`securityGroupIds` empty and expects the caller to fill them in, so
`ensure_network` creates them and `inject_infra_network` puts their ids into the
recommendation. `useExisting=true` is what makes beetle adopt them instead of
creating a second set.

**No `nameSeed`.** The migration API offers one, and it cannot be used here:
beetle's `ApplyNameSeed` prefixes `nodeGroups[].vNetId`, `subnetId` and
`securityGroupIds` along with the names, so a seed on top of the real ids would
ask for `cpbfs-cpbfs-vnet` and find nothing. Either the seed names everything or
this folder does. This folder does.

### Step 2 is why there is no destination path in `.env`

The probe opens one SSH session to the node and answers three questions with it.

**Where the data goes.** A plan with no filter resolves `dstPath` to the scan
root — `/testdata` — because `resolveObjectStorageDstPath` leaves a `beetleSsh`
destination alone. The transfer never gets that far: before rsync runs, transx-ex
opens an SSH session and runs `mkdir -p /testdata` **as the node's own user, with
no sudo** (`storagex/executor-rsync.go` `ensureRemoteDir`). That is a permission
denied on every CSP image.

So the destination is the login user's `$HOME` plus a directory, read off the node
itself. Building it from beetle's `nodeUserName` instead would assume the home
directory follows the user name, and CSP images disagree about that often enough
to matter.

**Whether rsync is there.** transx-ex shells out to `rsync -e ssh`, which needs
rsync on *both* ends. Without this check a missing rsync fails every cell
identically, minutes into a transfer, with an error about a closed connection.

**Whether the `beetleSsh` reference resolves.** The probe makes the same two
beetle calls cm-centipede makes (`GetSSHAccessInfo`: the infra read for the
address and user, the sshKey read for the private key), so a key or a user name
beetle cannot supply is found here rather than mid-migration.

### The one filter this folder sends

```json
"fileSystemFilter": { "targetMapping": { "srcName": "<scan root>", "dstName": "<dst path>" } }
```

`rules` stays empty, so the whole dataset still migrates — only where it lands
changes. `srcName` has to equal the scan root **exactly**, which is why it comes
from honeybee's answer (`hb_scan_root`) and not from `.env`;
`selectMigrationPath` matches it against the scan root or a listed folder and
answers a miss with a 400.

beetle-os-test sends no filter at all, and that is the right thing there: with
none, the destination is rewritten to the `osId` anyway. Here it would be
`/testdata`, which is the permission problem above.

**Strategy is `relay`**, centipede's default: pull to centipede's local staging,
then push to the node. The alternative, agent-forward, needs an SSH agent set up
on whatever host centipede runs on — a statement about centipede's deployment
rather than about this test.

---

## Verdicts and results

| Verdict | Meaning |
|---|---|
| **PASS** | Migration `completed` and validation `passed` |
| **FAIL** | Anything else |
| **SKIP** | The cell was never reached |

**SKIP is not "filtered out with `--csp`".** `--csp` narrows the row list itself,
so a CSP left out of it has no row in the table and no entry in the result JSON
at all. What a SKIP means is that the run ended before that cell's turn came —
today the only way that happens is `--stop-on-fail` stopping at an earlier
failure.

There is no **BLOCK**. In the managed-DB matrix that verdict means "a
higher-to-lower version pair the plan refused up front", and a filesystem has no
version to compare.

Outputs land in `logs/`:

- `fs-matrix.log` — the full run, with colour stripped
- `fs-matrix-api.log` — every beetle, honeybee and centipede call as a runnable
  `curl` line plus the response, with credentials masked
- `fs-matrix-result.json`:

```json
{
  "namePrefix": "cpbfs",
  "nsId": "cpbfs01",
  "provisioner": "cm-beetle",
  "migrator": "cm-centipede",
  "collector": "cm-honeybee",
  "srcType": "filesystem",
  "dstType": "beetleSsh",
  "srcScanRoot": "/testdata",
  "srcFileCount": 54,
  "srcSizeBytes": 2596664,
  "finishedAt": "2026-09-11 02:31:44",
  "csps": [
    { "csp": "aws", "region": "ap-northeast-2", "status": "PASS", "elapsedSec": 252,
      "infraId": "cpbfs-aws-infra", "nodeId": "...", "nodeUserName": "cb-user",
      "publicIP": "3.x.x.x", "dstPath": "/home/cb-user/testdata",
      "migrationId": "01J...", "validationStatus": "passed",
      "detail": "validation passed (54 files)" }
  ]
}
```

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
| `FS_CSPS` | `aws ncp` | the rows — one cell each |
| `BEETLE_URL` / `TUMBLEBUG_URL` | `:8056/beetle` / `:1323/tumblebug` | where the provisioner answers |
| `HB_BASE` / `CP_BASE` | `:8081` / `:8085` | where cm-honeybee and cm-centipede answer |
| `MATRIX_NS` | `cpbfs01` | the cb-tumblebug namespace — this folder's own |
| `MATRIX_NAME_PREFIX` | `cpbfs` | 2-6 chars; everything is `<prefix>-<csp>-<what>`, and `--cleanup` matches on it |
| `<CSP>_REGION` | | must match the region the connection was registered under |
| `<CSP>_ZONE` / `_ZONE2` | | both required; **`_ZONE` must be the connection's assigned zone** |
| `<CSP>_VNET_CIDR` | | a `/16` unless `_SUBNET1_CIDR` / `_SUBNET2_CIDR` are set too |
| `<CSP>_NODE_*` | 2 vCPU / 4 GB / 20 GB / ubuntu 22.04 | what beetle is asked to match |
| `FS_ALLOWED_CIDR` | `0.0.0.0/0` | who may reach the node's port 22 |
| `HOST_IP` | `127.0.0.1` | how cm-honeybee and cm-centipede reach the source's published port |
| `FS_DST_PATH` | *(read off the node)* | pins an absolute destination instead |
| `FS_SRC_SSH_PORT` | `34922` | the source's published SSH port |
| `KEEP_NODE` / `KEEP_ON_FAIL` | `0` | ⚠ `1` leaves a **running, billing VM** |
| `KEEP_NETWORK` | `0` | `1` keeps the vNet and friends — free, and the next run adopts them |
| `NODE_TIMEOUT` | `1800` | waiting for cm-beetle to build a node |

### Reclaiming what a run left

```bash
./scripts/fs-matrix.sh --cleanup
```

cb-tumblebug's namespace is the state, so this needs nothing from a previous run:
nodes and network resources are found by listing the namespace and matching
`<prefix>-<csp>-`. Order matters and is handled — node, then security group, then
SSH key, then vNet, since a vNet cannot go while something is attached to it.

A create whose request reached the CSP but whose record never reached tumblebug is
in no list. Those are recorded in `logs/.inflight-<csp>.json` while the create is
in flight, and **reported, never deleted** — nothing here can reach a VM
cb-tumblebug has no record of, so a person has to check the console.
