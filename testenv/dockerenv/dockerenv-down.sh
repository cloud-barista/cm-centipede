#!/usr/bin/env bash
# ──────────────────────────────────────────────────────────────────────────────
# [DOCKERENV DOWN] Stop all 12 containers of the role-split test environment
#
# Usage:
#   ./dockerenv-down.sh            # stop and remove containers (images and volumes kept)
#   ./dockerenv-down.sh --volumes  # also remove anonymous volumes
#   ./dockerenv-down.sh --rmi      # also remove the images that were built
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if docker compose version >/dev/null 2>&1; then
    DC=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
    DC=(docker-compose)
else
    echo "!!! docker compose not found." >&2
    exit 1
fi

# See dockerenv-up.sh: down reads the same .env, so it resolves the same image
# names as up and satisfies docker-compose.yml's required DOCKERENV_PASSWORD. It
# does not check the value - stopping containers needs no password of its own.
if [ ! -f "${SCRIPT_DIR}/.env" ]; then
    echo "!!! .env not found (cp .env.example .env). docker compose needs it to resolve the stack." >&2
    exit 1
fi
DC+=(--env-file "${SCRIPT_DIR}/.env")

DOWN_OPTS=()
for arg in "$@"; do
    case "$arg" in
        --volumes) DOWN_OPTS+=("--volumes") ;;
        --rmi)     DOWN_OPTS+=("--rmi" "local") ;;
    esac
done

echo ">>> Stopping and removing test environment containers..."
"${DC[@]}" down "${DOWN_OPTS[@]}"
echo ">>> Done."
