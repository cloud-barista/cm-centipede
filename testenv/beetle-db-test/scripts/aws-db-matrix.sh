#!/usr/bin/env bash
#
# aws-db-matrix.sh — AWS managed-RDBMS version matrix
#
# Source: a systemd container this folder builds (src/Dockerfile.<engine>,
#         providerName=onprem)
# Target: a logical database inside an AWS RDS instance created with cm-beetle
#
# Every combination of source version x target version runs as one cell. Cost
# decides the loop order: one target instance is created per target version
# (5-30 min), every source version runs against it, and then it is destroyed.
#
# One cell:
#   1) Start the source container -> wait for matrix-init.service
#      (the account, the database and the seed all finish together)
#   2) Create the target database — cm-beetle's logical database API
#   3) Collect the source with cm-honeybee
#   4) cm-centipede POST /plans/target  -> a downgrade is refused here (BLOCK)
#   5) cm-centipede POST /migration -> poll for progress
#   6) cm-centipede POST /validation -> the verdict for this cell
#
# Verdicts: PASS / BLOCK (downgrade refused up front) / FAIL / SKIP
#
# ⚠ Engines: mysql and mariadb. AWS also sells managed PostgreSQL, but cm-beetle
#   declares dbEngine as enums:"mysql,mariadb" and cannot ask for it yet —
#   BEETLE_ENGINES_AWS in lib/beetle.sh is the line that changes when it can.
#   AWS has no managed MongoDB this matrix can use either: DocumentDB exposes no
#   public endpoint, and EC2 self-hosting is out of scope.
#
# ⚠ Transport security: RDS sets require_secure_transport=ON for MariaDB 11.8+.
#   Creating a logical database goes through cb-spider's SQL fallback, which
#   connects in plaintext to everything but IBM hosts, so such a column cannot be
#   prepared at all and is SKIPped with the reason. There is no parameter-group
#   API in this stack to turn it off, and TARGET_TLS_MODE does not apply — it
#   governs the migration, not that connection.
#
# ⚠ Requires:
#   - cm-beetle, cb-tumblebug and cb-spider running, with the aws-<region>
#     connection registered (the deployments stack does that: make up && make init)
#   - cm-honeybee and cm-centipede running (checked in step 1)
#   - a running docker daemon, jq, curl
#   - source containers run --privileged (systemd). Local testing only.
#
# ⚠ Cost: one RDS instance per target version. They are destroyed when the run
#   ends or is interrupted with Ctrl-C, but --keep-instance leaves them running
#   (and billing). Anything a harder interruption left behind is reclaimed by
#   --cleanup, which lists the cb-tumblebug namespace — there is no local state
#   file to lose.
#
# Usage:
#   ./scripts/aws-db-matrix.sh
#   ./scripts/aws-db-matrix.sh --engines mysql --src-versions "8.0" --dst-versions "8.4"
#   ./scripts/aws-db-matrix.sh --only 8.4:8.0
#   ./scripts/aws-db-matrix.sh --mode ssh
#   ./scripts/aws-db-matrix.sh --cleanup
#
# Settings live in .env at the root of this folder (never committed).
#   Precedence: CLI option > real shell variable > .env > script default

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=./lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
load_env_file "$ENV_FILE"

CSP="aws"
CSP_TITLE="AWS"

# shellcheck source=./lib/db-matrix.sh
. "$SCRIPT_DIR/lib/db-matrix.sh"

matrix_main "$@"
