# CM-Centipede Deployments

Local development stack for CM-Centipede.

This stack runs CM-Centipede — built locally from this repository — together
with its dependencies: CM-Beetle, CM-Honeybee, and CB-Tumblebug/CB-Spider.

## Layout

```
deployments/
├── README.md                        This file
└── docker-compose/
    ├── docker-compose.yaml           Main stack
    ├── docker-compose.traefik.yaml   Optional: HTTPS reverse proxy overlay
    ├── docker-compose.ui.yaml        Optional: UI dashboard overlay (--profile ui)
    ├── .env.example                  Copy to .env and fill in credentials
    ├── cb-tumblebug/                 Config/assets/init scripts for CB-Tumblebug
    ├── cm-honeybee/                  Config for CM-Honeybee + its OpenBao
    ├── etcd/                         CB-Tumblebug's etcd auth helper scripts
    ├── openbao/                      Shared OpenBao (CB-Tumblebug / MC-Terrarium secrets)
    ├── scripts/                      make init / make clean-db entry points
    ├── traefik/                      Traefik static config
    └── data/                         Persistent data (created by `make up`, gitignored)
```

## Prerequisites

- Docker Engine with Compose v2, BuildKit enabled

## Quick start

```bash
cd deployments/docker-compose
cp .env.example .env
# Fill in CENTIPEDE_API_*, CENTIPEDE_ENCRYPTION_SECRET_KEY, BEETLE_API_*, TB_API_*,
# SP_API_*, SP_POSTGRES_*, POSTGRES_*, TERRARIUM_API_*.

cd ../..
make up   # prepares volumes, starts+inits OpenBao, builds cm-centipede, starts everything
```

`make up` runs in the foreground; `Ctrl+C` then `make down` to stop.

Register cloud credentials (reads `~/.cloud-barista/credentials.yaml.enc`,
see `docker-compose/cb-tumblebug/init/README.md`):

```bash
make init
```

After restarting an already-initialized stack, OpenBao comes back sealed:

```bash
make unseal
```

## Services

| Service                 | Purpose                                              | Port(s)        |
| ----------------------- | ---------------------------------------------------- | -------------- |
| `cm-centipede`          | Data migration — **built locally**                   | 8085           |
| `cm-beetle`             | Infrastructure migration — published image           | 8056           |
| `cm-honeybee`           | Source computing info collector — published image    | 8081, 8082     |
| `openbao-honeybee`      | Dedicated OpenBao for cm-honeybee's own secrets      | 8201 → 8200    |
| `cb-tumblebug`          | Target cloud infrastructure management               | 1323           |
| `cb-tumblebug-etcd`     | CB-Tumblebug metadata store                          | 2379, 2380     |
| `cb-tumblebug-postgres` | CB-Tumblebug spec/image metadata                     | (internal)     |
| `cb-spider`             | Multi-cloud driver behind CB-Tumblebug               | 1024           |
| `cb-spider-postgres`    | CB-Spider metadata                                   | (internal)     |
| `cb-mapui`              | Map-based UI client for CB-Tumblebug                 | 1324           |
| `mc-terrarium`          | Cloud network extensions (e.g. site-to-site VPN)     | 8055           |
| `openbao`               | Shared secrets store for CB-Tumblebug / MC-Terrarium | 8200           |

**CM-Centipede** is built from the repository root `Dockerfile` (`make up`
passes `--build`) and starts once CM-Honeybee is healthy. Needs
`CENTIPEDE_API_USERNAME`/`PASSWORD` and `CENTIPEDE_ENCRYPTION_SECRET_KEY` in
`.env`; `CENTIPEDE_MIGRATION_CONCURRENCY` is optional. It mounts CM-Honeybee's
data directory read-only to read CM-Honeybee's RSA private key, which it needs
to decrypt CM-Honeybee's API responses.

**CM-Beetle** runs the published `cloudbaristaorg/cm-beetle` image (not built
locally) — CM-Centipede depends on it having already migrated the target
infrastructure, not on iterating its code. Needs `BEETLE_API_USERNAME`/
`PASSWORD` in `.env`.

**CM-Honeybee** runs the published `cloudbaristaorg/cm-honeybee` image. It
has its own OpenBao (`openbao-honeybee`, host port 8201 to avoid clashing with
the shared `openbao`), which CM-Honeybee initializes and unseals itself via
`HONEYBEE_VAULT_ADDR`, so no `make init`/`unseal` is needed for it.

By default CM-Honeybee uses the `ssh` source-group type. The `csp` type takes
the cloud credential in the source group itself (stored in `openbao-honeybee`),
so it needs no CB-Spider settings here. See
[cm-honeybee's README](https://github.com/cloud-barista/cm-honeybee/blob/main/README.md#2-register-source-group)
for registering a source group.

## Common tasks

Run `make help` for the full list. Highlights:

| Command               | Description                                                     |
| --------------------- | --------------------------------------------------------------- |
| `make up`             | Prepare volumes, start+init OpenBao, build & start all services |
| `make dev-ui`         | Run the UI dev server with hot-reload (see below)               |
| `make down`           | Stop and remove all containers                                  |
| `make init`           | Register CSP credentials into OpenBao and CB-Tumblebug          |
| `make unseal`         | Unseal OpenBao after a restart                                  |
| `make status` / `ps`  | Show container status                                           |
| `make logs`           | Follow logs for all services                                    |
| `make clean-db`       | Remove CB-Tumblebug/CB-Spider metadata (keeps OpenBao)          |
| `make clean-all`      | Full reset, including both OpenBao instances (requires re-init) |

## Traefik (optional HTTPS reverse proxy)

Fronts `cb-mapui`/`cb-spider` AdminWeb with HTTPS via local hostnames. Enable
in `.env`:

```bash
COMPOSE_FILE=docker-compose.yaml:docker-compose.traefik.yaml
```

Place a local dev cert/key at `~/.cloud-barista/certs/{cert.crt,cert.key}`
(see `traefik/traefik.yaml`), then `make up`.

## UI dashboard

`docker-compose.ui.yaml` adds `cm-centipede-ui`, the Next.js dashboard built
from `ui/`, gated behind the `ui` Compose profile so it's never built by a
plain `up`:

```bash
docker compose -f docker-compose.yaml -f docker-compose.ui.yaml --profile ui up
```

For local hot-reload instead of the containerized UI:

```bash
make up        # start backends
make dev-ui    # npm install + Next.js dev server at http://localhost:3000
```

## Data & secrets

- `data/` — persistent container data (DBs, logs, OpenBao storage), gitignored.
- `data/cm-centipede-container/db/` pairs with `CENTIPEDE_ENCRYPTION_SECRET_KEY`:
  losing or changing either one makes every stored credential unrecoverable.
- `openbao/secrets/openbao-init.json` — OpenBao's unseal key + root token
  (from `make init-openbao`), gitignored. Not recoverable if lost — you'd
  need `make clean-all` and to re-register credentials.
- `.env` is gitignored; only `.env.example` is committed.

## Syncing from upstream

`cb-tumblebug/` and `openbao/` are copied from cm-beetle's
`deployments/docker-compose/`, which tracks specific
[cb-tumblebug](https://github.com/cloud-barista/cb-tumblebug) releases. When
bumping the `cb-tumblebug`/`cb-spider`/`cb-mapui` image tags, re-sync these
directories and check for breaking API/model changes.
