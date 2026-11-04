# cm-centipede UI

Next.js dashboard for cm-centipede's data migration: file system, object
storage and DBMS.

## Running it

```bash
cp .env.example .env.local     # point at your backends
npm install
npm run dev                    # http://localhost:3000
```

```bash
npm run build && npm start     # production
npm run typecheck              # types only
npm run lint                   # oxlint + design-system rules
```

From the repository root, `make dev-ui` runs the dev server.

## Environment

The browser never learns a backend address: every call goes to this app's own
origin and a Next.js route handler forwards it. None of these variables has a
`NEXT_PUBLIC_` prefix, so none of them reaches the client bundle.

| Variable | Default | Used by |
|---|---|---|
| `CENTIPEDE_ENDPOINT` | `http://localhost:8085` | `/centipede/*` |
| `CENTIPEDE_API_USERNAME` | `ChangeMe` | Basic Auth injected by the proxy |
| `CENTIPEDE_API_PASSWORD` | `ChangeMe` | Basic Auth injected by the proxy |
| `HONEYBEE_ENDPOINT` | `http://localhost:8081` | `/honeybee/*` |
| `BEETLE_ENDPOINT` | `http://localhost:8056` | `/beetle/*` |
| `BEETLE_API_USERNAME` | `ChangeMe` | Basic Auth injected by the proxy |
| `BEETLE_API_PASSWORD` | `ChangeMe` | Basic Auth injected by the proxy |

cm-beetle rejects an unauthenticated call, so its proxy injects Basic Auth the
way the cm-centipede one does — cm-centipede's own beetle client does the same
(`pkg/client/beetle/client.go`). cm-honeybee takes no credentials at all.

cm-centipede listens on **8085** (`conf/cm-centipede.yaml`), not the 8057
placeholder some files under `deployments/` still carry.

## Screens

| Tab | What it does |
|---|---|
| Overview | Last 7 days: counts, active runs (all time), category split, recent runs |
| Connections | cm-honeybee sources (CRUD), cm-beetle targets (read-only), inline drafts (session-only) |
| File System / Object Storage / Database | The six-step migration wizard, one domain adapter each |
| Runs | History with server-side pagination, logs, retry and validation |

## Architecture

```
src/
  api/        pure HTTP functions — no knowledge of TanStack Query
  queries/    useQuery / useMutation hooks; keys.ts is the single key table
  types/      hand-written mirrors of dmdl/ and the three REST APIs
  lib/        category mapping, form schema, plan envelope, name uniqueness,
              secret masking, inline vault, runner + target-model seams
  store/      Zustand: uiStore (persisted), wizardStore and secretStore (not)
  components/ layout · overview · connections · wizard · filter · runs · domains
```

Server state belongs to TanStack Query; Zustand holds only what this browser
owns. Swapping the server-state library means replacing `src/queries/` alone —
`src/api/` and the components stay as they are.

### Polling

Idle queries slow down rather than stop. A stop condition that depends on data
only polling can fetch cannot wake itself up, and migrations started from
`testenv/examples/*/migrate.sh` have to show up in an already-open window.

| Query | Idle | Active |
|---|---|---|
| migration list | 30 s | 3 s |
| shortlisted target still in cm-beetle | 5 min | 15 min, or Retry-After, after a 503 |
| connections (expanded group) | 15 s | 3 s while a check is unsettled |
| one migration / its logs / its validation | stopped | 2–5 s while running |

Hidden tabs do not poll; returning to the tab refetches at once.

### The scan target belongs to the connection

cm-honeybee's three data source-group types are `fs`, `db` and `minio`, and this
UI handles exactly those. Its other types — `csp`, and `onprem` under its
earlier spelling `ssh`, which is also what an omitted type stores — collect
infrastructure rather than data; honeybee answers 400 when asked to inspect a
filesystem, a bucket or a database through one, so they are filtered out of
every list here.

What an inspect reads is a property of the connection, not of the inspect:

| Category | Group type | Scan target |
|---|---|---|
| File System | `fs` | `fs_scan_path` — absolute, empty registers `/` |
| Object Storage | `minio` | `os_scan_bucket` + `os_scan_prefix` |
| Database | `db` | `db_name` (+ `db_pg_schema`), empty = every user database |

So it is set in **Connections**, where it is created, and the wizard's Step 1
shows it read-only alongside the two things the import request does still carry
— `max_depth` (filesystem) and the metric selection. Changing it means changing
the connection, which is what marks the result already collected under the old
target *stale*.

A stale result is refused rather than flagged, because the refined models carry
no status field for a consumer to notice. honeybee answers 400 to that and to
"nothing collected yet" alike, so the two are told apart by message
(`isStaleScanError` in `src/api/honeybee.ts`) — otherwise a scan that ran would
be reported as a scan that never did.

### An agent is not always part of a connection

cm-honeybee installs one only where it reaches the source over SSH: always for
a filesystem connection, and for a db or object storage connection only when its
access type is `ssh-tunnel`. A direct one is inspected by the server itself.

It reports the case where no agent applies as `agent_status: "failed"`, with a
message saying the check does not apply — a considered decision on its side,
since a third status value would meet every existing reader unprepared. Read
literally it makes a perfectly healthy direct connection wear an `agent FAILED`
badge, so this UI asks a different question first: *does* an agent apply here
(`agentApplies` in `src/lib/connectionCategory.ts`). Where it does not, the
badge and the failure message are left out rather than softened — there is no
agent whose health to report.

The same value is aggregated into each group's `count_agent_failed`, and
`GET /source_group` carries no access type to correct it with — a db group of
three healthy direct connections arrives as `"count_agent_failed": 3`. So the
group and page headers do not count the agent at all, in any group
(`StatusRollup`). Counting it only for filesystem groups, where the number is
unambiguous, was the first attempt and the worse one: it put two meanings behind
one line with nothing on screen to separate them.

The cost is that an agent which failed to install on an SSH host no longer
announces itself in a header, so finding it means opening the group. The
connection's own block reports it, which is where the roll-up has always said
the detail lives. Should that matter enough to change, the cheap fix is on
honeybee's side: one `count_agent_applicable` on `SourceGroupRes` would make the
ratio readable again.

### A shortlisted target may stop existing

The target shortlist is persisted, so it outlives the cm-beetle resources it
points at. It is checked rather than trusted, wherever it is on screen — the
Targets section and the wizard's Step 2.

cm-beetle has a single-item GET for each of the three kinds, and this does not
use them. Its three disagree on what "not found" is: `GetInfra` and
`GetObjectStorage` answer 404, while `GetRDBMS` has no 404 branch and folds
every failure into a 500 — so a missing rdbms is indistinguishable from a sick
cm-beetle. Absence from a list that loaded is the same unambiguous answer for
all three. It is also fewer calls (one per namespace and kind, not one per
entry, and each reaches the cloud provider through cb-tumblebug) and it reuses
the picker's own query keys, so entries from the namespace on screen are
checked for free.

Three outcomes, not two. `unknown` — cm-beetle unreachable, the proxy at 502,
the provider rate-limiting it into a 503, the namespace itself gone — is never
rendered as `gone`, because reporting a read failure as a missing target turns
every hiccup into "your saved targets have disappeared". It is said once above
the list instead of once per row.

Nothing is ever removed automatically, and a `gone` entry can still be used:
one read does not get to delete a reader's list or overrule their choice. The
same rule keeps the wizard's destination in place when its check comes back
`gone` — Step 2 says so and leaves the draft alone.

cm-beetle's 503 carries a `Retry-After`, and `ApiError.retryAfterSec` exists so
the poller can obey it rather than keep its cadence through the backpressure
that header is there to stop.

### Secrets

`src/store/secretStore.ts` is a separate file with no `persist` middleware.
Inline drafts are written to `localStorage` (`centipede-inline-vault`) by
`src/lib/vault/`, with their credentials (`privateKey`, `accessKeyId`,
`secretAccessKey`, `password`, `tlsCAPem`, `sshTunnel.privateKey`) sealed under
a secret key the user types in Connections → Inline:

- PBKDF2-SHA256 (600,000 iterations) derives the key; AES-256-GCM seals each
  record with a fresh IV. The clear part of a record (label, type, host,
  username, …) is bound to it as AAD, so editing it in storage makes the record
  fail to open.
- The secret key is kept nowhere — not in `sessionStorage` either, which
  Chrome's session restore brings back. Every reload asks for it again. Lost
  key: **forget all** deletes the vault.
- Web Crypto is used in a secure context (HTTPS or localhost). Over plain
  `http://<IP>` it is unavailable, and `@noble/ciphers` / `@noble/hashes`,
  loaded on demand, run the same algorithm instead; a vault written by one opens
  with the other.
- Locking drops the keys and the decrypted drafts, not the wizard: a destination
  already chosen in Step 2 was copied into it and stays usable.
- The beetleDb admin password from Step 2 is held apart, in memory only, and
  cleared by **reset wizard**.

This protects a copy of the browser profile, not a compromised page: script
running in the page can read unlocked drafts or the key as it is typed.

The plan viewer masks secret keys immediately before rendering, because the plan
response carries credentials back. Request bodies are never logged.
