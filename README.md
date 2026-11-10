# Data Migration

This repository provides data migration features.
This is a sub-system on [Cloud-Barista platform](https://github.com/cloud-barista/docs)
and utilizes [CM-Honeybee](https://github.com/cloud-barista/cm-honeybee) to inspect the source data
and [CM-Beetle](https://github.com/cloud-barista/cm-beetle) to reach the target computing infrastructure.

## Overview

Data Migration framework (codename: cm-centipede) is going to support:

- target migration plan generation from the source data model collected by CM-Honeybee,
- migration execution and control (run, cancel, retry, logs, progress) for file systems, object storages and databases, and
- post-migration validation that the target data matches the source.

<details>
    <summary>Terminology</summary>

- Source model: The description of the source data (file systems, buckets, databases) produced by CM-Honeybee
- Plan (target model): The migration plan derived from the source model, each source entry paired with its destination and filter
- Migration: One run of a plan. Created from a plan and executed asynchronously
- Validation: The comparison of the source and the target after a completed migration

</details>

## Architecture

```
  CM-Honeybee              CM-Centipede                     CM-Beetle / CB-Tumblebug
  (source model)  ──────▶  plan → migration → validation ──▶ (target nodes, buckets, databases)
                                    │
                                    └── data-mig (storagex: rsync / S3, dbmsx: DB dump & restore)
```

- The source side always comes from CM-Honeybee (`srcConnection.source: honeybee`).
- The destination is either a CM-Beetle reference (`beetleSsh`, `beetleObjectStorage`, `beetleDb`)
  or an inline connection (`ssh`, `minio`, `db`).
- Connection credentials are stored encrypted (AES-256, key stretched from `encryption.secretKey` with scrypt and a per-database salt).

| Directory                    | Description                                                     |
| ---------------------------- | --------------------------------------------------------------- |
| [cmd/](cmd/)                 | Server entry point                                              |
| [pkg/](pkg/)                 | REST API, plan/migration/validation core, DB access             |
| [dmdl/](dmdl/)               | Data migration models (source / target / common), own Go module |
| [data-mig/](data-mig/)       | Common data migration module (storagex, dbmsx), own Go module   |
| [ui/](ui/)                   | Web UI (Next.js dashboard)                                      |
| [deployments/](deployments/) | Docker Compose stack of cm-centipede and its dependent sub-systems |
| [testenv/](testenv/)         | Test environments and end-to-end examples                       |

## Execution and development environment

- Operating system (OS):
  - Ubuntu 22.04, Ubuntu 24.04
- Language:
  - Go: 1.26.3
- Container runtime:
  - Docker Engine with Compose v2
- Tools required on the host running cm-centipede (included in the container image):
  - `rsync`, `openssh-client` (file system migrations)
- UI (optional):
  - Node.js with npm

## How to run

### 1. Write the configuration file

Start from the shipped [conf/cm-centipede.yaml](conf/cm-centipede.yaml) and replace every `ChangeMe`:
the server refuses to start while any of them is left.
The file is read from `$CENTIPEDE_ROOT/conf` (default `~/.cm-centipede/conf`) or `./conf`.

> [!WARNING]
> Change `encryption.secretKey` before running anything you care about, and then keep it.
> Losing or changing the value makes every stored credential unrecoverable.

### 2. Run the dependent sub-systems

cm-centipede needs CM-Honeybee, CM-Beetle and CB-Tumblebug to be running.
The Docker Compose stack in [deployments/](deployments/README.md) runs all of them,
together with cm-centipede itself (built from the repository root `Dockerfile`).

```shell
cp deployments/docker-compose/.env.example deployments/docker-compose/.env
# Fill in CENTIPEDE_API_*, CENTIPEDE_ENCRYPTION_SECRET_KEY, BEETLE_API_*, TB_API_*,
# SP_API_*, SP_POSTGRES_*, POSTGRES_*, TERRARIUM_API_*

make up       # build cm-centipede and start every service (foreground)
make init     # register the CSP credentials, once
make unseal   # after restarting an already-initialized stack (OpenBao comes back sealed)
```

### 3. Build and run cm-centipede

`make up` already runs cm-centipede on port 8085. To run it locally instead
(e.g. during development), stop the `cm-centipede` container first, then:

```shell
make run
```

Or, you can build and run it within Docker by this command.

```shell
docker build -t cloudbaristaorg/cm-centipede:latest .
docker run -p 8085:8085 \
  -e CENTIPEDE_SELF_ENDPOINT=0.0.0.0:8085 \
  -e CENTIPEDE_API_USERNAME=<api-username> \
  -e CENTIPEDE_API_PASSWORD=<api-password> \
  -e CENTIPEDE_BEETLE_USERNAME=<beetle-username> \
  -e CENTIPEDE_BEETLE_PASSWORD=<beetle-password> \
  -e CENTIPEDE_TUMBLEBUG_USERNAME=<tumblebug-username> \
  -e CENTIPEDE_TUMBLEBUG_PASSWORD=<tumblebug-password> \
  -e CENTIPEDE_ENCRYPTION_SECRET_KEY=<your-secret-key> \
  cloudbaristaorg/cm-centipede:latest
```

### 4. Migrate data

A migration always follows the same four steps.
The full, runnable flow for each scenario is in [testenv/examples](#test-environments).

4.1. Get the source model from CM-Honeybee

Register the source with CM-Honeybee, import it, and get the refined source model
(e.g. `GET /honeybee/source_group/{sgId}/connection_info/{connId}/fs/refined`).

4.2. Create the target plan

Pair each source entry with its destination and filter.

```shell
curl -X POST -u default:default http://localhost:8085/centipede/plans/target \
  -H 'Content-Type: application/json' \
  -d '{
        "source": <the refined source model from 4.1>,
        "plans": [{
          "srcConnection": {
            "source": "honeybee",
            "honeybee": { "connectionId": "<honeybee connection id>" }
          },
          "dstConnection": {
            "source": "beetleSsh",
            "beetleSsh": { "nsId": "<nsId>", "infraId": "<infraId>", "nodeId": "<nodeId>" }
          },
          "fileSystemFilter": {
            "targetMapping": { "srcName": "/testdata", "dstName": "/home/cb-user/testdata" },
            "rules": [
              { "action": "exclude", "type": "glob", "pattern": "**/*.log" }
            ]
          }
        }]
      }'
```

The `data` field of the response is the plan.

4.3. Run the migration

Creating a migration starts it asynchronously. Poll it until `status` is
`completed`, `failed` or `cancelled`.

```shell
curl -X POST -u default:default http://localhost:8085/centipede/migration \
  -H 'Content-Type: application/json' \
  -d '{ "name": "fs-migration", "plan": <the plan from 4.2> }'

curl -u default:default http://localhost:8085/centipede/migration/{migrationId}
curl -u default:default "http://localhost:8085/centipede/migration/{migrationId}/logs?page=1&pageSize=100"
```

Optional request fields:

- `stagingDir` : Absolute staging directory for this migration (overrides `migration.stagingDir`)
- `concurrency` : Transfers each item runs at once, 1 to 32 (overrides `migration.concurrency`)
- `dbmsOnFailure` : `cleanup` (default) undoes a failed DBMS item, `keep` leaves it for inspection

A running migration can be stopped with `POST /centipede/migration/{migrationId}/cancel`,
and a failed or cancelled one re-run with `POST /centipede/migration/{migrationId}/retry`
(`retryMode`: `partial` (default) or `full`).
Migrations are listed with `GET /centipede/migration` (paginated) or `GET /centipede/migration/all`,
and removed with `DELETE /centipede/migration/{migrationId}` (not while running).

4.4. Validate the result

```shell
curl -X POST -u default:default http://localhost:8085/centipede/migration/{migrationId}/validation
curl -u default:default http://localhost:8085/centipede/migration/{migrationId}/validation
```

`validationStatus` goes from `not_run` through `running` to `passed` or `failed`, with a per-item detail list
(an item can also be `warning`, which is reported but does not fail the validation).

## Web UI

[ui/](ui/README.md) is a Next.js dashboard for the same flow.

```shell
make dev-ui   # dev server with hot-reload at http://localhost:3000 (run `make up` first)
```

To run it in the Compose stack, enable the UI overlay in `deployments/docker-compose/.env`
(`COMPOSE_FILE=docker-compose.yaml:docker-compose.ui.yaml`, `COMPOSE_PROFILES=ui`).

## Health-check

Check if CM-Centipede is running

```bash
curl -u default:default http://localhost:8085/centipede/readyz

# Output if it's running successfully
# {"success":true,"data":{"status":"ok"}}
```

## Check out all APIs

- Swagger UI of the running server: http://localhost:8085/centipede/api/index.html
- [Centipede APIs (Swagger Document)](https://cloud-barista.github.io/cb-tumblebug-api-web/?url=https://raw.githubusercontent.com/cloud-barista/cm-centipede/main/pkg/api/rest/docs/swagger.yaml)

After changing the handler annotations, regenerate the document with `make swagger`.

## Test environments

[testenv/](testenv/) provides the environments to create sources and targets, and end-to-end examples
that drive the whole flow over the REST APIs.

| Example                                                           | Scenario                                 |
| ----------------------------------------------------------------- | ---------------------------------------- |
| [onprem-to-aws-fs](testenv/examples/onprem-to-aws-fs)             | On-premises filesystem → AWS VM          |
| [onprem-to-aws-os](testenv/examples/onprem-to-aws-os)             | On-premises MinIO → AWS object storage   |
| [onprem-to-aws-mysql](testenv/examples/onprem-to-aws-mysql)       | On-premises MySQL → AWS RDS              |
| [ncp-to-aws-os](testenv/examples/ncp-to-aws-os)                   | NCP Object Storage → AWS object storage  |
| [ncp-to-aws-mysql](testenv/examples/ncp-to-aws-mysql)             | NCP Cloud DB for MySQL → AWS RDS         |

Each example directory has a `README.md` explaining every step and a `migrate.sh` running the same API calls.

## License

[Apache License 2.0](LICENSE)
