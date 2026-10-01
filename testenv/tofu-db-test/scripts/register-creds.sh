#!/usr/bin/env bash
# ==============================================================================
# register-creds.sh — a thin wrapper around init/openbao/register-creds.sh
# ------------------------------------------------------------------------------
#   Use it to re-register credentials after refilling .env.
#   ./scripts/register-creds.sh [--keep-env]
# ==============================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
exec bash "$SCRIPT_DIR/../init/openbao/register-creds.sh" "$@"
