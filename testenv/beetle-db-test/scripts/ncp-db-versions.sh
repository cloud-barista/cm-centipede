#!/usr/bin/env bash
#
# ncp-db-versions.sh — list the managed DB engine versions NCP offers (read-only)
#
# The body is in lib/db-versions.sh. This file only picks the CSP.
#
#   ./scripts/ncp-db-versions.sh            # print the list
#   ./scripts/ncp-db-versions.sh --write    # update NCP_<ENGINE>_DST_VERSIONS in .env
#
# ⚠ These are TARGET (managed) versions. Source versions are decided by what the
#   vendor's apt repository serves for jammy; that table is in .env.example.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=./lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
load_env_file "$ENV_FILE"

CSP="ncp"

# shellcheck source=./lib/db-versions.sh
. "$SCRIPT_DIR/lib/db-versions.sh"

db_versions_main "$@"
