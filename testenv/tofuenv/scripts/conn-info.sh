#!/usr/bin/env bash
# ==============================================================================
# conn-info.sh — print the connection info of the provisioned resources
# ------------------------------------------------------------------------------
#   ./scripts/conn-info.sh [aws|ncp] [network|bucket|vm|database|all] [--reveal]
#
#   Arguments:
#     csp       : aws (default) | ncp
#     resource  : bucket | vm | database | all (default: all)
#                 ncp also accepts: network
#     --reveal  : print sensitive values (passwords, connection_uri) in clear text
#                 (otherwise they stay masked as <sensitive>)
#
#   Examples:
#     ./scripts/conn-info.sh                        # all aws resources (masked)
#     ./scripts/conn-info.sh aws database           # DB only
#     ./scripts/conn-info.sh aws database --reveal  # DB URIs and password in clear text
#     ./scripts/conn-info.sh ncp all --reveal       # all NCP resources + clear text
#
#   How it works:
#     Reads `tofu output` for each module inside the tofuenv-runner container, from
#     the workspace of the prefix .env sets (TF_VAR_<csp>_name_prefix). To see an
#     environment made under another prefix, put that prefix in .env; every
#     environment is listed by ./scripts/list.sh.
#     Resources that have no state yet are skipped. Sensitive outputs are only
#     revealed with --reveal, using 'tofu output -raw'.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

usage() { echo "Usage: $0 [aws|ncp] [network|bucket|vm|database|all] [--reveal]" >&2; exit 1; }

CSP="aws"
RESOURCE="all"
REVEAL=0

for arg in "$@"; do
    case "$arg" in
        --reveal)                       REVEAL=1 ;;
        -h|--help)                      usage ;;
        aws|ncp)                        CSP="$arg" ;;
        network|bucket|vm|database|all) RESOURCE="$arg" ;;
        *) echo -e "${RED}unknown argument: $arg${NC}" >&2; usage ;;
    esac
done

if [ "$CSP" = "aws" ] && [ "$RESOURCE" = "network" ]; then
    echo -e "${RED}aws has no network module (it uses the default VPC).${NC}" >&2; exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2
    exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"
ws_load "$CSP"

# Sensitive output names per resource (revealed with -raw when --reveal is given).
# The engine list differs per CSP: AWS serves mysql/mariadb/postgres through RDS, NCP
# serves mysql/postgresql/mongodb as managed Cloud DB.
sensitive_outputs() {
    case "$1" in
        database)
            if [ "$CSP" = "ncp" ]; then
                echo "db_password mysql_connection_uri postgres_connection_uri mongodb_connection_uri"
            else
                echo "db_password mysql_connection_uri mariadb_connection_uri postgres_connection_uri"
            fi
            ;;
        *) echo "" ;;
    esac
}

# NCP managed DBs need a public domain issued in the console; warn when it is missing.
warn_missing_public_domain() {
    local module="$1" missing
    missing="$(ws_exec bash -c '
        cd "/work/'"$module"'" 2>/dev/null || exit 0
        tofu output -json 2>/dev/null || true
    ' | jq -r '. as $o | to_entries
                | map(select(.key | endswith("_public_domain")))
                | map(select(.value.value == null or .value.value == ""))
                # An engine whose port is null is not provisioned at all
                # (provision.sh --engine), so it has no domain to wait for.
                | map(select($o[(.key | sub("_public_domain$"; "_port"))].value != null))
                | map(.key) | join(" ")' 2>/dev/null || true)"

    if [ -n "$missing" ]; then
        echo -e "  ${YELLOW}Public domain not issued yet: ${missing}${NC}"
        echo -e "  ${YELLOW}These managed DBs are not reachable from outside. Issue a public domain in the${NC}"
        echo -e "  ${YELLOW}NCP console (DB Management > Public domain), then run ./scripts/ncp-db-domain.sh${NC}"
    fi
}

show_resource() {
    local res="$1"
    local module="tofu/${CSP}/${res}"

    if [ ! -d "$ROOT_DIR/$module" ]; then
        echo -e "${RED}module not found: $module${NC}" >&2
        return
    fi

    echo -e "${CYAN}=== ${CSP}/${res} (prefix ${WS_PREFIX}) ===${NC}"

    # No outputs means the module has not been provisioned yet.
    #   Detection uses -json: plain `tofu output` prints a "No outputs found"
    #   warning on stdout, so an empty state would not look empty here.
    local probe
    probe="$(ws_exec bash -c '
        cd "/work/'"$module"'" 2>/dev/null || exit 0
        tofu output -json 2>/dev/null || true
    ' | tr -d '[:space:]')"

    if [ -z "$probe" ] || [ "$probe" = "{}" ]; then
        echo -e "  ${YELLOW}(not provisioned — run ./scripts/provision.sh ${CSP} ${res} first)${NC}"
        echo
        return
    fi

    local out
    out="$(ws_exec bash -c '
        set -euo pipefail
        cd "/work/'"$module"'" 2>/dev/null || exit 0
        tofu output 2>/dev/null || true
    ')"
    echo "$out" | sed 's/^/  /'

    if [ "$CSP" = "ncp" ] && [ "$res" = "database" ]; then
        warn_missing_public_domain "$module"
    fi

    if [ "$REVEAL" -eq 1 ]; then
        local names; names="$(sensitive_outputs "$res")"
        if [ -n "$names" ]; then
            echo -e "  ${YELLOW}--- sensitive values (reveal) ---${NC}"
            for name in $names; do
                local val
                val="$(ws_exec bash -c '
                    cd "/work/'"$module"'" 2>/dev/null || exit 0
                    tofu output -raw '"$name"' 2>/dev/null || true
                ')"
                [ -n "$val" ] && echo "  ${name} = ${val}"
            done
        fi
    fi
    echo
}

if [ "$RESOURCE" = "all" ]; then
    if [ "$CSP" = "ncp" ]; then
        for r in network bucket vm database; do show_resource "$r"; done
    else
        for r in bucket vm database; do show_resource "$r"; done
    fi
else
    show_resource "$RESOURCE"
fi

if [ "$REVEAL" -eq 0 ]; then
    echo -e "${GREEN}To also print sensitive values (password, connection_uri): $0 ${CSP} ${RESOURCE} --reveal${NC}"
fi
