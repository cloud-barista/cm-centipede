#!/usr/bin/env bash
# ==============================================================================
# ncp-db-domain.sh — reflect and verify the NCP managed-DB public domains
# ------------------------------------------------------------------------------
#   ./scripts/ncp-db-domain.sh
#
#   Why:
#     A public domain is what goes into the host part of a connection string for
#     an NCP managed DB. It can only be issued from the console — there is no API
#     action and no provider argument for it:
#
#       Database > Cloud DB for <engine> > select the DB server
#         > DB Management > Public domain > request
#
#     Once issued, this script refreshes the tofu state so that the
#     *_public_domain / *_host outputs pick the new value up, then verifies that
#     every managed engine has one.
#
#   Notes:
#     - Acts on the workspace of the prefix .env sets (TF_VAR_ncp_name_prefix).
#     - Only a refresh is performed (tofu apply -refresh-only), so no
#       infrastructure is changed.
#     - A public domain must be re-issued whenever the DB is re-created.
#     - All three NCP engines are managed, so each one needs its own request.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
MODULE="tofu/ncp/database"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ ! -d "$ROOT_DIR/$MODULE" ]; then
    echo -e "${RED}module not found: $MODULE${NC}" >&2; exit 1
fi
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"
ws_load ncp

echo -e "${CYAN}=== refreshing ncp/database state, prefix ${WS_PREFIX} (refresh-only) ===${NC}"
ws_exec bash -c '
    set -euo pipefail
    set -a; . /work/.env; set +a
    export VAULT_ADDR=http://openbao:8200
    mkdir -p /work/.tofu-plugin-cache /work/ssh_keys
    cd "/work/'"$MODULE"'"
    '"$WS_INIT"' >/dev/null
    tofu apply -refresh-only -auto-approve
'

JSON="$(ws_exec bash -c '
    cd "/work/'"$MODULE"'" 2>/dev/null || exit 0
    tofu output -json 2>/dev/null || true
')"

if [ -z "$JSON" ]; then
    echo -e "${RED}No outputs found. Provision it first: ./scripts/provision.sh ncp database${NC}" >&2
    exit 1
fi

echo
echo -e "${CYAN}=== public domain status ===${NC}"
MISSING=0
for engine in mysql postgres mongodb; do
    domain="$(echo "$JSON" | jq -r --arg k "${engine}_public_domain" '.[$k].value // ""')"
    if [ -z "$domain" ] || [ "$domain" = "null" ]; then
        echo -e "  ${RED}MISSING${NC}  ${engine}  — issue a public domain in the NCP console"
        MISSING=$((MISSING + 1))
    else
        port="$(echo "$JSON" | jq -r --arg k "${engine}_port" '.[$k].value // ""')"
        echo -e "  ${GREEN}OK${NC}       ${engine}  ${domain}:${port}"
    fi
done

echo
if [ "$MISSING" -gt 0 ]; then
    echo -e "${YELLOW}${MISSING} engine(s) still have no public domain.${NC}"
    echo "  NCP console > Database > Cloud DB for <engine> > select the DB server"
    echo "    > DB Management > Public domain > request"
    echo "  Then run this script again."
    exit 1
fi

echo -e "${GREEN}All managed DBs have a public domain.${NC}"
echo "  Connection info :  ./scripts/conn-info.sh ncp database --reveal"
echo "  Test data       :  ./scripts/gen-data.sh --provider ncp --target database"
