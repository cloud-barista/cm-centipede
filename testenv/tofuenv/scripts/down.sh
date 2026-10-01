#!/usr/bin/env bash
# ==============================================================================
# down.sh — stop the containers
# ------------------------------------------------------------------------------
#   ./scripts/down.sh          # stop the containers (keep the OpenBao data volume)
#   ./scripts/down.sh --wipe   # also delete the volume (OpenBao data + credentials)
#                              #   -> the next start needs init + credential re-registration
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'

if [ "${1:-}" = "--wipe" ]; then
    echo -e "${RED}Deleting the volumes as well (OpenBao data and tokens will be lost).${NC}"
    docker compose down -v
    # The OpenBao data is a bind mount rather than a named volume, so remove it explicitly.
    rm -rf "$ROOT_DIR/container-volume/openbao-data"
    # The unseal key / root token file is worthless once the storage is gone.
    rm -f "$ROOT_DIR/init/openbao/secrets/openbao-init.json"
    echo -e "${YELLOW}Removed the OpenBao storage and init secrets. The next up.sh will initialize from scratch.${NC}"
else
    docker compose down
fi
