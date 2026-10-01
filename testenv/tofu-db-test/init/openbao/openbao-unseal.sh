#!/usr/bin/env bash
# ==============================================================================
# openbao-unseal.sh — unseal OpenBao after a container restart
# ------------------------------------------------------------------------------
#   Opens a sealed OpenBao instance with the unseal key stored in
#   secrets/openbao-init.json.
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INIT_OUTPUT="${INIT_OUTPUT:-$SCRIPT_DIR/secrets/openbao-init.json}"
VAULT_ADDR="${VAULT_ADDR:-http://localhost:38210}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

if [ ! -f "$INIT_OUTPUT" ]; then
    echo -e "${RED}Error: ${INIT_OUTPUT} not found. Run openbao-init.sh first.${NC}" >&2
    exit 1
fi

# Wait until reachable
echo -n "Waiting for OpenBao to be reachable..."
for i in $(seq 1 30); do
    if curl -sf "${VAULT_ADDR}/v1/sys/seal-status" -o /dev/null 2>/dev/null; then
        echo -e " ${GREEN}OK${NC}"; break
    fi
    echo -n "."; sleep 1
    [ "$i" -eq 30 ] && { echo -e " ${RED}FAILED${NC}"; exit 1; }
done

SEALED=$(curl -sf "${VAULT_ADDR}/v1/sys/seal-status" | jq -r '.sealed')
if [ "$SEALED" = "false" ]; then
    echo -e "${GREEN}[openbao-unseal]${NC} Already unsealed."
    exit 0
fi

UNSEAL_KEY=$(jq -r '.keys[0]' "$INIT_OUTPUT")
SEALED=$(curl -sf -X POST "${VAULT_ADDR}/v1/sys/unseal" \
    -H "Content-Type: application/json" \
    -d "{\"key\": \"${UNSEAL_KEY}\"}" | jq -r '.sealed')

if [ "$SEALED" = "false" ]; then
    echo -e "${GREEN}[openbao-unseal]${NC} OpenBao unsealed & ready."
else
    echo -e "${RED}[openbao-unseal]${NC} Unseal failed. Check: curl ${VAULT_ADDR}/v1/sys/seal-status" >&2
    exit 1
fi
