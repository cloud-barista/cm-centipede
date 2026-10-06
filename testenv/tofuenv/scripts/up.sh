#!/usr/bin/env bash
# ==============================================================================
# up.sh — bring up the whole stack
# ------------------------------------------------------------------------------
#   1) Start OpenBao
#   2) init (first run) or unseal (restart)
#   3) Register credentials (.env -> OpenBao, then blank the .env keys)
#   4) Start tofu-runner (picking up the refreshed VAULT_TOKEN)
#
#   Usage:
#     ./scripts/up.sh                 # start everything + register credentials
#     ./scripts/up.sh --keep-env      # keep the .env values after registration
#     ./scripts/up.sh --no-register   # skip credential registration
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RED='\033[0;31m'; NC='\033[0m'

KEEP_ENV_FLAG=""
DO_REGISTER=true
for arg in "$@"; do
    case "$arg" in
        --keep-env)    KEEP_ENV_FLAG="--keep-env" ;;
        --no-register) DO_REGISTER=false ;;
        *) echo "Unknown option: $arg" >&2; exit 1 ;;
    esac
done

if [ ! -f "$ROOT_DIR/.env" ]; then
    echo -e "${RED}.env is missing. Run 'cp .env.example .env && chmod 600 .env' first and fill in the values.${NC}" >&2
    exit 1
fi

# Before anything starts, so a bad mode does not leave containers running.
# shellcheck source=./lib/env-perm.sh
. "$SCRIPT_DIR/lib/env-perm.sh"
check_env_perm "$ROOT_DIR/.env" || exit 1
# In a subshell, so .env is read for the check only and nothing it sets leaks
# into the docker compose calls below.
( set -a; . "$ROOT_DIR/.env"; set +a; ENV_FILE="$ROOT_DIR/.env"; assert_no_placeholder ) || exit 1

echo -e "${CYAN}[1/4] Starting OpenBao...${NC}"
docker compose up -d openbao

echo -e "${CYAN}[2/4] OpenBao init/unseal...${NC}"
if [ -f "$ROOT_DIR/init/openbao/secrets/openbao-init.json" ]; then
    bash "$ROOT_DIR/init/openbao/openbao-unseal.sh"
else
    bash "$ROOT_DIR/init/openbao/openbao-init.sh"
fi

if $DO_REGISTER; then
    echo -e "${CYAN}[3/4] Registering credentials...${NC}"
    bash "$ROOT_DIR/init/openbao/register-creds.sh" $KEEP_ENV_FLAG
else
    echo -e "${YELLOW}[3/4] Skipping credential registration (--no-register)${NC}"
fi

echo -e "${CYAN}[4/4] Starting tofu-runner...${NC}"
# Recreate the container so it picks up the latest VAULT_TOKEN from .env
docker compose up -d --force-recreate tofu-runner

echo -e "${GREEN}=== Ready ===${NC}"
echo "  Provision  :  ./scripts/provision.sh <csp> <resource>   # csp: aws|ncp, resource: bucket|vm|database"
echo "  Examples   :  ./scripts/provision.sh aws bucket"
echo "                ./scripts/provision.sh ncp database"
echo "  Deprovision:  ./scripts/deprovision.sh <csp> <resource>"
echo "  List       :  ./scripts/list.sh            # every environment, all prefixes"
