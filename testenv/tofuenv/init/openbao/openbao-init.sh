#!/usr/bin/env bash
# ==============================================================================
# openbao-init.sh — one-time initialization of OpenBao in persistent mode
# ------------------------------------------------------------------------------
#   Initializes a fresh OpenBao instance, stores the unseal key + root token,
#   unseals it automatically and then writes VAULT_TOKEN into .env.
#
#   Prerequisite: the OpenBao container must be running (sealed) in persistent mode.
#   Outputs: secrets/openbao-init.json (unseal key + root token — keep it safe!)
#            updated VAULT_TOKEN in ../../.env
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/../../.env}"
INIT_OUTPUT="${INIT_OUTPUT:-$SCRIPT_DIR/secrets/openbao-init.json}"
VAULT_ADDR="${VAULT_ADDR:-http://localhost:38200}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

echo -e "${YELLOW}[openbao-init]${NC} VAULT_ADDR=${VAULT_ADDR}"

# Wait until OpenBao is reachable
echo -n "Waiting for OpenBao to be reachable..."
for i in $(seq 1 30); do
    if curl -sf "${VAULT_ADDR}/v1/sys/seal-status" -o /dev/null 2>/dev/null; then
        echo -e " ${GREEN}OK${NC}"; break
    fi
    echo -n "."; sleep 1
    if [ "$i" -eq 30 ]; then
        echo -e " ${RED}FAILED${NC}"
        echo "Error: OpenBao is not reachable at ${VAULT_ADDR}" >&2
        exit 1
    fi
done

# Already initialized?
INIT_STATUS=$(curl -sf "${VAULT_ADDR}/v1/sys/seal-status" | jq -r '.initialized')
if [ "$INIT_STATUS" = "true" ]; then
    if [ -f "$INIT_OUTPUT" ]; then
        echo -e "${YELLOW}[openbao-init]${NC} Already initialized. Run openbao-unseal.sh if it needs unsealing."
        exit 0
    fi
    # Inconsistent state: the storage is initialized but the unseal key / root
    # token file is gone, so the instance can no longer be opened.
    echo -e "${RED}[openbao-init] Inconsistent state:${NC} the OpenBao storage is already initialized," >&2
    echo -e "  but ${INIT_OUTPUT} (unseal key + root token) is missing, so it cannot be unsealed." >&2
    echo "" >&2
    echo "  Discard the stored data and initialize again (safe while no real secrets exist yet):" >&2
    echo "    ./scripts/down.sh" >&2
    echo "    sudo rm -rf container-volume/openbao-data init/openbao/secrets/openbao-init.json" >&2
    echo "    ./scripts/up.sh" >&2
    exit 1
fi

# Initialize (1 key share, threshold 1 — local development convenience; raise it in production)
echo -e "${YELLOW}[openbao-init]${NC} Initializing OpenBao (1 key share, threshold 1)..."
INIT_RESPONSE=$(curl -sf -X POST "${VAULT_ADDR}/v1/sys/init" \
    -H "Content-Type: application/json" \
    -d '{"secret_shares": 1, "secret_threshold": 1}')

if [ -z "$INIT_RESPONSE" ]; then
    echo -e "${RED}Error: Initialization failed — empty response${NC}" >&2
    exit 1
fi

mkdir -p "$(dirname "$INIT_OUTPUT")"
echo "$INIT_RESPONSE" | jq . > "$INIT_OUTPUT"
chmod 600 "$INIT_OUTPUT"

UNSEAL_KEY=$(echo "$INIT_RESPONSE" | jq -r '.keys[0]')
ROOT_TOKEN=$(echo "$INIT_RESPONSE" | jq -r '.root_token')

echo -e "${GREEN}[openbao-init]${NC} Initialized successfully (secrets: ${INIT_OUTPUT})"
echo -e "  ${YELLOW}WARNING: ${INIT_OUTPUT} holds the unseal key and root token — store it safely.${NC}"

# Unseal
echo -e "${YELLOW}[openbao-init]${NC} Unsealing..."
SEALED=$(curl -sf -X POST "${VAULT_ADDR}/v1/sys/unseal" \
    -H "Content-Type: application/json" \
    -d "{\"key\": \"${UNSEAL_KEY}\"}" | jq -r '.sealed')

if [ "$SEALED" = "false" ]; then
    echo -e "${GREEN}[openbao-init]${NC} OpenBao unsealed & ready."
else
    echo -e "${RED}[openbao-init]${NC} Unseal may have failed. Check: curl ${VAULT_ADDR}/v1/sys/seal-status" >&2
fi

# Write VAULT_TOKEN into .env
if [ -f "${ENV_FILE}" ]; then
    if grep -q "^VAULT_TOKEN=" "${ENV_FILE}"; then
        sed -i "s|^VAULT_TOKEN=.*|VAULT_TOKEN=${ROOT_TOKEN}|" "${ENV_FILE}"
    else
        echo "VAULT_TOKEN=${ROOT_TOKEN}" >> "${ENV_FILE}"
    fi
    echo -e "${GREEN}[openbao-init]${NC} Wrote VAULT_TOKEN into ${ENV_FILE}."
else
    echo -e "${RED}Error: .env not found at ${ENV_FILE}${NC}" >&2
    echo "  Copy .env.example to .env first." >&2
    exit 1
fi

# Make sure the KV v2 secret engine is mounted at secret/
MOUNTS=$(curl -sf -H "X-Vault-Token: ${ROOT_TOKEN}" "${VAULT_ADDR}/v1/sys/mounts" 2>/dev/null || echo "{}")
if echo "$MOUNTS" | jq -e '."secret/"' >/dev/null 2>&1; then
    echo -e "${GREEN}[openbao-init]${NC} KV v2 secret engine is already mounted at secret/."
else
    echo -e "${YELLOW}[openbao-init]${NC} Enabling the KV v2 secret engine at secret/..."
    curl -sf -X POST "${VAULT_ADDR}/v1/sys/mounts/secret" \
        -H "X-Vault-Token: ${ROOT_TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"type": "kv", "options": {"version": "2"}}' > /dev/null
    echo -e "${GREEN}[openbao-init]${NC} KV v2 enabled at secret/."
fi

echo ""
echo "============================================================"
echo "  OpenBao initialization complete."
echo "    Init file : ${INIT_OUTPUT}"
echo "    After a restart, run openbao-unseal.sh."
echo "============================================================"
