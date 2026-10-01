#!/usr/bin/env bash
#
# aws-db-matrix.sh — AWS managed-RDBMS version matrix
#
# Source: a systemd container this folder builds (src/Dockerfile.<engine>,
#         providerName=onprem)
# Target: a database inside an AWS RDS instance created with OpenTofu
#
# Every combination of source version x target version runs as one cell. Cost
# decides the loop order: one target instance is created per target version
# (5-30 min), every source version runs against it, and then it is destroyed.
#
# One cell:
#   1) Start the source container -> wait for matrix-init.service
#      (the account, the database and the seed all finish together)
#   2) Create the target database — tofu/my-target or tofu/pg-target
#   3) Collect the source with cm-honeybee
#   4) cm-centipede POST /plans/target  -> a downgrade is refused here (BLOCK)
#   5) cm-centipede POST /migration -> poll for progress
#   6) cm-centipede POST /validation -> the verdict for this cell
#
# Verdicts: PASS / BLOCK (downgrade refused up front) / FAIL / SKIP
#
# ⚠ Engines: mysql, mariadb, postgresql. AWS has no managed MongoDB this matrix
#   can use — DocumentDB exposes no public endpoint, so the matrix host cannot
#   reach it, and EC2 self-hosting is out of scope. Run mongodb on NCP.
#
# ⚠ Transport security: RDS defaults to require_secure_transport=ON for MariaDB
#   11.8+ and rds.force_ssl=1 for PostgreSQL. The matrix leaves those alone
#   (AWS_TARGET_SECURE_TRANSPORT=csp-default) and connects with
#   TARGET_TLS_MODE=prefer. To see what happens when only plaintext is possible,
#   use --secure-transport off.
#
# ⚠ Requires:
#   - ./scripts/up.sh has started OpenBao and the tofu runner, with the AWS keys
#     registered
#   - cm-honeybee and cm-centipede running (checked in step 1)
#   - a running docker daemon, jq, curl
#   - source containers run --privileged (systemd). Local testing only.
#
# ⚠ Cost: one RDS instance per target version. They are destroyed when the run
#   ends or is interrupted with Ctrl-C, but --keep-instance leaves them running
#   (and billing). Anything an interruption left behind is reclaimed by
#   --cleanup, which reads tofu state.
#
# Usage:
#   ./scripts/aws-db-matrix.sh
#   ./scripts/aws-db-matrix.sh --engines mysql --src-versions "8.0" --dst-versions "8.4.11"
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
