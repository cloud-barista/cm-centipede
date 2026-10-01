#!/usr/bin/env bash
# ==============================================================================
# ncp-db-versions.sh — list the NCP DB engine versions, server images and specs
# ------------------------------------------------------------------------------
#   ./scripts/ncp-db-versions.sh [mysql|postgresql|mongodb|server|all]
#
#   Why:
#     TF_VAR_ncp_{mysql,postgres,mongodb}_version must hold the FULL version
#     string (e.g. 8.0.36). A partial value such as "8.0" makes the provider
#     normalize state to "8.0.36" on refresh, and because engine_version_code is
#     RequiresReplace every plan would then schedule a ~30 min DB re-creation.
#     Use this script to copy the exact strings into .env.
#
#   How it works:
#     tofu/ncp/versions only holds data sources (it creates nothing), so applying
#     it is safe and free. Its outputs are printed as "version -> image product code".
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
MODULE="tofu/ncp/versions"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

usage() { echo "Usage: $0 [mysql|postgresql|mongodb|server|all]" >&2; exit 1; }

WHAT="${1:-all}"
case "$WHAT" in
    mysql|postgresql|mongodb|server|all) ;;
    -h|--help) usage ;;
    *) echo -e "${RED}unknown argument: $WHAT${NC}" >&2; usage ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ ! -d "$ROOT_DIR/$MODULE" ]; then
    echo -e "${RED}module not found: $MODULE${NC}" >&2; exit 1
fi
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

echo -e "${CYAN}=== querying NCP catalog (data sources only, nothing is created) ===${NC}"
JSON="$(docker exec "$RUNNER" bash -c '
    set -euo pipefail
    set -a; . /work/.env; set +a
    export VAULT_ADDR=http://openbao:8200
    mkdir -p /work/.tofu-plugin-cache
    cd "/work/'"$MODULE"'"
    tofu init -input=false >/dev/null
    tofu apply -auto-approve >/dev/null
    tofu output -json
')"

print_map() {
    local title="$1" key="$2" arrow="$3"
    local body
    body="$(echo "$JSON" | jq -r --arg k "$key" '
        (.[$k].value // {})
        | to_entries
        | sort_by(.key)
        | map("  \(.key)  '"$arrow"'  \(.value)")
        | join("\n")
    ')"
    echo -e "${GREEN}${title}${NC}"
    if [ -z "$body" ]; then
        echo "  (none)"
    else
        echo "$body"
    fi
    echo
}

print_list() {
    local title="$1" key="$2"
    local body
    body="$(echo "$JSON" | jq -r --arg k "$key" '(.[$k].value // []) | sort | map("  \(.)") | join("\n")')"
    echo -e "${GREEN}${title}${NC}"
    if [ -z "$body" ]; then
        echo "  (none)"
    else
        echo "$body"
    fi
    echo
}

echo
case "$WHAT" in
    mysql)      print_map "MySQL engine versions (TF_VAR_ncp_mysql_version)"           mysql_versions      "->" ;;
    postgresql) print_map "PostgreSQL engine versions (TF_VAR_ncp_postgres_version)"   postgresql_versions "->" ;;
    mongodb)    print_map "MongoDB engine versions (TF_VAR_ncp_mongodb_version)"       mongodb_versions    "->" ;;
    server)
        print_map  "Server images (TF_VAR_ncp_server_image_name)" server_images "->"
        print_list "Server specs (TF_VAR_ncp_server_spec_code)"   server_specs
        ;;
    all)
        print_map  "MySQL engine versions (TF_VAR_ncp_mysql_version)"         mysql_versions      "->"
        print_map  "PostgreSQL engine versions (TF_VAR_ncp_postgres_version)" postgresql_versions "->"
        print_map  "MongoDB engine versions (TF_VAR_ncp_mongodb_version)"     mongodb_versions    "->"
        print_map  "Server images (TF_VAR_ncp_server_image_name)"             server_images       "->"
        print_list "Server specs (TF_VAR_ncp_server_spec_code)"               server_specs
        ;;
esac

echo -e "${YELLOW}Copy the exact version strings into .env (TF_VAR_ncp_*_version).${NC}"
echo -e "${YELLOW}Partial versions like 8.0 are rejected by the module precondition.${NC}"
