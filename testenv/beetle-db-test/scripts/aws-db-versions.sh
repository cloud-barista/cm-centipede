#!/usr/bin/env bash
#
# aws-db-versions.sh — list the managed DB engine versions AWS offers (read-only)
#
# The body is in lib/db-versions.sh. This file only picks the CSP.
#
#   ./scripts/aws-db-versions.sh            # print the list
#   ./scripts/aws-db-versions.sh --write    # update AWS_<ENGINE>_DST_VERSIONS in .env
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

CSP="aws"

# shellcheck source=./lib/db-versions.sh
. "$SCRIPT_DIR/lib/db-versions.sh"

db_versions_main "$@"
