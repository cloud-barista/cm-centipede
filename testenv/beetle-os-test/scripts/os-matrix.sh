#!/usr/bin/env bash
#
# os-matrix.sh — object storage migration matrix
#
# Source: a MinIO container this folder builds and seeds (src/Dockerfile.minio,
#         six buckets, providerName=onprem)
# Target: a managed bucket created with cm-beetle, one per cell
#
# Every combination of source bucket x CSP runs as one cell. Unlike the
# managed-DB matrix there is nothing expensive to amortise: a bucket is created
# in seconds, so each cell makes its own and deletes it again.
#
# One cell:
#   1) cm-beetle recommend -> create the target bucket -> wait
#   2) cm-centipede POST /plans/target
#   3) cm-centipede POST /migration -> poll for progress
#   4) cm-centipede POST /validation -> the verdict for this cell
#   5) cm-beetle DELETE the target bucket
#
# Verdicts: PASS / FAIL / SKIP
#
# ⚠ CSPs: whatever OS_CSPS names, checked at run time against cm-beetle's own
#   GET /recommendation/middleware/objectStorage/support. There is no table in
#   this folder to keep in step.
#
# ⚠ Access is direct on both sides. honeybee's ssh-tunnel object storage inspect
#   needs an agent on the source host, and transx-ex has no tunnelled transfer
#   path for storage, so there is no --mode option.
#
# ⚠ Requires:
#   - cm-beetle, cb-tumblebug and cb-spider running, with the <csp>-<region>
#     connection registered (the deployments stack does that: make up && make init)
#   - cm-honeybee and cm-centipede running (checked in step 1)
#   - a running docker daemon, jq, curl
#   - the source container runs --privileged (systemd). Local testing only.
#
# ⚠ Cost: object storage is charged by what it holds, and a cell's bucket lives
#   for one migration. They are removed when the run ends or is interrupted with
#   Ctrl-C; --keep-bucket leaves them (holding the migrated objects). Anything a
#   harder interruption left behind is reclaimed by --cleanup, which lists the
#   cb-tumblebug namespace — there is no local state file to lose.
#
# Usage:
#   ./scripts/os-matrix.sh
#   ./scripts/os-matrix.sh --csp aws
#   ./scripts/os-matrix.sh --buckets "raw-data images"
#   ./scripts/os-matrix.sh --only aws:raw-data
#   ./scripts/os-matrix.sh --cleanup
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

# shellcheck source=./lib/os-matrix.sh
. "$SCRIPT_DIR/lib/os-matrix.sh"

matrix_main "$@"
