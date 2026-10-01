#!/usr/bin/env bash
# ==============================================================================
# register-creds.sh — register credentials from .env into OpenBao KV v2
# ------------------------------------------------------------------------------
#   Registration paths:
#     secret/csp/aws  <- AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
#     secret/db/aws   <- AWS_DB_PASSWORD
#     secret/csp/ncp  <- NCP_ACCESS_KEY, NCP_SECRET_KEY
#     secret/db/ncp   <- NCP_DB_PASSWORD
#
#   Idempotency:
#     - A group is skipped when its "trigger" key is empty, so re-running after
#       the keys have been blanked never overwrites the stored OpenBao secrets.
#
#   Defense-in-depth:
#     - On successful registration the matching .env keys are blanked
#       (disable with --keep-env).
#
#   Usage:
#     ./register-creds.sh              # register + blank .env keys
#     ./register-creds.sh --keep-env   # register only, keep .env values
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/../../.env}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

KEEP_ENV=false
[ "${1:-}" = "--keep-env" ] && KEEP_ENV=true

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${RED}Error: .env not found at ${ENV_FILE}${NC}" >&2
    exit 1
fi

# Checked here as well as in up.sh: ./scripts/register-creds.sh execs straight
# into this file, so up.sh is not the only way in.
# shellcheck source=../../scripts/lib/env-perm.sh
. "$SCRIPT_DIR/../../scripts/lib/env-perm.sh"
check_env_perm "$ENV_FILE" || exit 1

# Load .env
set -a; . "$ENV_FILE"; set +a
VAULT_ADDR="${VAULT_ADDR:-http://localhost:38210}"

if [ -z "${VAULT_TOKEN:-}" ]; then
    echo -e "${RED}Error: VAULT_TOKEN is empty. Run openbao-init.sh first.${NC}" >&2
    exit 1
fi

# Check OpenBao status.
# Note: jq's // operator substitutes on false as well as null, so booleans
#       (.sealed etc.) are compared directly instead of using //.
STATUS=$(curl -sf "${VAULT_ADDR}/v1/sys/seal-status" 2>/dev/null || echo '{}')
INITIALIZED=$(echo "$STATUS" | jq -r '.initialized')
SEALED=$(echo "$STATUS" | jq -r '.sealed')
if [ "$INITIALIZED" != "true" ]; then
    echo -e "${RED}Error: OpenBao is uninitialized or unreachable (${VAULT_ADDR}). Run openbao-init.sh.${NC}" >&2; exit 1
fi
if [ "$SEALED" != "false" ]; then
    echo -e "${RED}Error: OpenBao is sealed. Run openbao-unseal.sh.${NC}" >&2; exit 1
fi

echo -e "${CYAN}=== Registering credentials into OpenBao (${VAULT_ADDR}) ===${NC}"

# Keys to blank after a successful registration
KEYS_TO_BLANK=()

# Build {data:{k:v,...}} JSON (jq escapes values safely)
build_payload() {
    local json='{}'
    local k v
    for k in "$@"; do
        v="${!k:-}"
        json=$(jq -n --argjson base "$json" --arg key "$k" --arg val "$v" '$base + {($key): $val}')
    done
    jq -n --argjson d "$json" '{data: $d}'
}

# register_group <path> <trigger_key> <key...>
register_group() {
    local path="$1" trigger="$2"; shift 2
    local keys=("$@")
    if [ -z "${!trigger:-}" ]; then
        echo -e "  ${YELLOW}SKIP${NC} secret/${path}  (${trigger} is empty -> keeping stored value)"
        return 0
    fi
    local payload; payload=$(build_payload "${keys[@]}")
    if curl -sf -X POST "${VAULT_ADDR}/v1/secret/data/${path}" \
            -H "X-Vault-Token: ${VAULT_TOKEN}" \
            -H "Content-Type: application/json" \
            -d "$payload" >/dev/null; then
        echo -e "  ${GREEN}OK${NC}   secret/${path}  registered (${#keys[@]} keys)"
        KEYS_TO_BLANK+=("${keys[@]}")
    else
        echo -e "  ${RED}FAIL${NC} secret/${path}  registration failed"
        return 1
    fi
}

# Validate the NCP managed-DB password against the same rules the ncloud
# provider enforces: 8-20 chars, at least one letter / digit / special char,
# and none of ` & + \ " ' / or whitespace.
# Without this pre-check the failure only surfaces after a ~30 min apply.
validate_ncp_db_password() {
    local pw="$1"
    # Assign len on its own line: in `local a="$1" b=${#a}` bash expands ${#a}
    # before a is assigned, which would always yield 0.
    local len=${#pw}
    if [ "$len" -lt 8 ] || [ "$len" -gt 20 ]; then
        echo "must be 8-20 characters (currently ${len})"; return 1
    fi
    printf '%s' "$pw" | LC_ALL=C grep -q '[A-Za-z]' || { echo "must contain at least one letter"; return 1; }
    printf '%s' "$pw" | LC_ALL=C grep -q '[0-9]'    || { echo "must contain at least one digit"; return 1; }
    printf '%s' "$pw" | LC_ALL=C grep -q '[]~!@#$%^*()_={};:,.<>?[-]' \
        || { echo "must contain at least one special character from ~!@#\$%^*()-_=[]{};:,.<>?"; return 1; }
    if printf '%s' "$pw" | LC_ALL=C grep -q '[`&+\\"'"'"'/[:space:]]'; then
        echo "contains a forbidden character (\` & + \\ \" ' / or whitespace)"; return 1
    fi
    return 0
}

if [ -n "${NCP_DB_PASSWORD:-}" ]; then
    if ! reason=$(validate_ncp_db_password "$NCP_DB_PASSWORD"); then
        echo -e "  ${RED}FAIL${NC} NCP_DB_PASSWORD rejected: ${reason}" >&2
        echo -e "  ${YELLOW}See the NCP password rules in .env.example.${NC}" >&2
        exit 1
    fi
    echo -e "  ${GREEN}OK${NC}   NCP_DB_PASSWORD passed the provider rule check"
fi

register_group "csp/aws" "AWS_SECRET_ACCESS_KEY" \
    AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
register_group "db/aws" "AWS_DB_PASSWORD" \
    AWS_DB_PASSWORD
register_group "csp/ncp" "NCP_SECRET_KEY" \
    NCP_ACCESS_KEY NCP_SECRET_KEY
register_group "db/ncp" "NCP_DB_PASSWORD" \
    NCP_DB_PASSWORD

# Blank the keys that were registered successfully
if [ "${#KEYS_TO_BLANK[@]}" -gt 0 ]; then
    if $KEEP_ENV; then
        echo -e "${YELLOW}[--keep-env]${NC} Skipping .env blanking."
    else
        for k in "${KEYS_TO_BLANK[@]}"; do
            sed -i "s|^${k}=.*|${k}=|" "$ENV_FILE"
        done
        echo -e "${GREEN}[register-creds]${NC} Blanked the successfully registered credential keys in .env."
    fi
else
    echo -e "${YELLOW}[register-creds]${NC} No new credentials registered (all empty or already stored)."
fi

echo -e "${CYAN}=== Done ===${NC}"
echo "  Verify:  curl -s -H \"X-Vault-Token: \$VAULT_TOKEN\" ${VAULT_ADDR}/v1/secret/data/csp/aws | jq .data.data"
echo "           curl -s -H \"X-Vault-Token: \$VAULT_TOKEN\" ${VAULT_ADDR}/v1/secret/data/csp/ncp | jq .data.data"
