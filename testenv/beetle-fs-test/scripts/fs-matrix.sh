#!/usr/bin/env bash
#
# fs-matrix.sh — filesystem migration matrix
#
# Source: a container this folder builds and seeds (src/Dockerfile.fs,
#         /testdata, 54 files, reached over SSH as root by key)
# Target: a node created with cm-beetle, one per CSP
#
# The whole dataset migrates as one transfer, so a cell is a CSP. Unlike the
# object storage matrix there is nothing cheap to make per row: a node takes
# minutes and costs money, which is why the row axis is the thing that varies
# least and why the node is created and destroyed inside its cell.
#
# One cell:
#   0) cm-beetle: vNet, subnets, security group, SSH key
#   1) cm-beetle recommend -> create the node -> wait -> ssh-ready
#   2) probe the node: $HOME (the destination), rsync, and beetle's SSH key
#   3) cm-centipede POST /plans/target
#   4) cm-centipede POST /migration -> poll for progress
#   5) cm-centipede POST /validation -> the verdict for this cell
#   6) cm-beetle DELETE the node, then release the network
#
# Verdicts: PASS / FAIL / SKIP
#
# ⚠ cm-honeybee installs its agent on the source when the connection is
#   registered, by downloading it from the internet. The source container needs
#   outbound access to raw.githubusercontent.com and media.githubusercontent.com.
#   The URL is hard-coded in cm-honeybee; nothing here can point it elsewhere.
#
# ⚠ Access is SSH on both sides, by key. There is no password anywhere: cm-honeybee
#   refuses a password on an fs connection (HTTP 400) and cm-centipede
#   authenticates SSH by key only, so a password-only source or target cannot be
#   migrated at all.
#
# ⚠ Requires:
#   - cm-beetle, cb-tumblebug and cb-spider running, with the <csp>-<region>
#     connection registered (the deployments stack does that: make up && make init)
#   - cm-honeybee and cm-centipede running (checked in step 1)
#   - a running docker daemon, jq, curl, ssh, ssh-keygen
#   - the source container runs --privileged (systemd). Local testing only.
#
# ⚠ Cost: a cell creates a real VM and deletes it again. They are removed when
#   the run ends or is interrupted with Ctrl-C; --keep-node leaves them running.
#   Anything a harder interruption left behind is reclaimed by --cleanup, which
#   lists the cb-tumblebug namespace — there is no local state file to lose.
#
# Usage:
#   ./scripts/fs-matrix.sh
#   ./scripts/fs-matrix.sh --csp aws
#   ./scripts/fs-matrix.sh --csp aws --keep-on-fail
#   ./scripts/fs-matrix.sh --cleanup
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

# shellcheck source=./lib/fs-matrix.sh
. "$SCRIPT_DIR/lib/fs-matrix.sh"

matrix_main "$@"
