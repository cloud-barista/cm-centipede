#!/usr/bin/env bash
# ==============================================================================
# conn-info.sh — print the connection info of the provisioned resources
# ------------------------------------------------------------------------------
#   ./scripts/conn-info.sh [aws|ncp] [network|bucket|vm|database|all] [--prefix <name>] [--reveal]
#
#   Arguments:
#     csp       : aws | ncp (default: both)
#     resource  : bucket | vm | database | all (default: all)
#                 ncp also accepts: network
#     --prefix  : only the environment of that prefix (default: every prefix)
#     --reveal  : print sensitive values (passwords, connection_uri) in clear text
#                 (otherwise they stay masked as <sensitive>)
#
#   Examples:
#     ./scripts/conn-info.sh                              # every environment, both CSPs (masked)
#     ./scripts/conn-info.sh aws database                 # every AWS environment's DBs
#     ./scripts/conn-info.sh aws --prefix cptf            # one environment
#     ./scripts/conn-info.sh ncp database --prefix cptf --reveal
#
#   How it works:
#     Every prefix is an environment of its own (a tofu workspace, see
#     scripts/lib/workspace.sh). The environments are found by reading the state
#     files, as ./scripts/list.sh does, and for each one `tofu output` is read
#     inside the tofuenv-runner container with that workspace selected. Only state
#     is read - no cloud or OpenBao call - so .env does not need to point at an
#     environment to show it. The one .env selects is listed first, marked *.
#     Modules without resources are left out. Sensitive outputs are only revealed
#     with --reveal, using 'tofu output -raw'.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

usage() { echo "Usage: $0 [aws|ncp] [network|bucket|vm|database|all] [--prefix <name>] [--reveal]" >&2; exit 1; }

CSP_ARG=""
RESOURCE="all"
PREFIX_ARG=""
REVEAL=0

while [ $# -gt 0 ]; do
    case "$1" in
        --reveal)                       REVEAL=1 ;;
        -h|--help)                      usage ;;
        --prefix)
            [ $# -ge 2 ] || { echo -e "${RED}--prefix needs a value${NC}" >&2; usage; }
            PREFIX_ARG="$2"; shift ;;
        --prefix=*)                     PREFIX_ARG="${1#--prefix=}" ;;
        aws|ncp)                        CSP_ARG="$1" ;;
        network|bucket|vm|database|all) RESOURCE="$1" ;;
        *) echo -e "${RED}unknown argument: $1${NC}" >&2; usage ;;
    esac
    shift
done

if [ "$CSP_ARG" = "aws" ] && [ "$RESOURCE" = "network" ]; then
    echo -e "${RED}aws has no network module (it uses the default VPC).${NC}" >&2; exit 1
fi

# The CSPs to look at: the one named, or both. network exists on NCP only.
if [ -n "$CSP_ARG" ]; then
    CSPS="$CSP_ARG"
elif [ "$RESOURCE" = "network" ]; then
    CSPS="ncp"
else
    CSPS="aws ncp"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2
    exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"

# CSP and WS_PREFIX name the environment being shown; the loop at the bottom sets
# them for each one, and ws_exec reads WS_PREFIX.
CSP=""
WS_PREFIX=""

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

ROWS="$(ws_provisioned)"

# prefixes_of <csp> — the CSP's environments, the one .env selects first, then
#   the rest by name; only --prefix when it is given.
prefixes_of() {
    local csp="$1" current all
    all="$(printf '%s\n' "$ROWS" | awk -v c="$csp" '$1 == c { print $3 }' | sort -u)"
    if [ -n "$PREFIX_ARG" ]; then
        printf '%s\n' "$all" | grep -xF -- "$PREFIX_ARG" || true
        return
    fi
    current="$(ws_current "$csp")"
    if [ -n "$current" ] && printf '%s\n' "$all" | grep -qxF -- "$current"; then
        echo "$current"
    fi
    printf '%s\n' "$all" | grep -vxF -- "${current:-}" || true
}

# modules_of <csp> — the modules to show, in display order.
modules_of() {
    if [ "$RESOURCE" != "all" ]; then
        echo "$RESOURCE"
    elif [ "$1" = "ncp" ]; then
        echo "network bucket vm database"
    else
        echo "bucket vm database"
    fi
}

if [ "$REVEAL" -eq 1 ] && [ -z "$PREFIX_ARG" ]; then
    echo -e "${YELLOW}--reveal without --prefix prints the passwords and connection URIs of EVERY environment.${NC}"
    echo -e "${YELLOW}Add --prefix <name> to reveal one environment only.${NC}"
    echo
fi

SHOWN=0
for CSP in $CSPS; do
    current="$(ws_current "$CSP")"
    for WS_PREFIX in $(prefixes_of "$CSP"); do
        mods=""
        for res in $(modules_of "$CSP"); do
            if printf '%s\n' "$ROWS" | grep -qxF "$CSP $res $WS_PREFIX"; then
                mods="$mods $res"
            fi
        done
        [ -n "$mods" ] || continue
        mark=""
        [ "$WS_PREFIX" = "$current" ] && mark=" *"
        echo -e "${GREEN}############ ${CSP} prefix: ${WS_PREFIX}${mark} ############${NC}"
        for res in $mods; do
            show_resource "$res"
        done
        SHOWN=$((SHOWN + 1))
    done
done

if [ "$SHOWN" -eq 0 ]; then
    what="${CSP_ARG:-aws/ncp} ${RESOURCE}"
    if [ -n "$PREFIX_ARG" ]; then
        echo -e "${YELLOW}Nothing provisioned for ${what} under prefix '${PREFIX_ARG}'.${NC}" >&2
    else
        echo -e "${YELLOW}Nothing provisioned for ${what}, under any prefix.${NC}" >&2
    fi
    echo "  Environments that exist: ./scripts/list.sh" >&2
    exit 1
fi

if [ -z "$PREFIX_ARG" ]; then
    echo -e "  ${GREEN}*${NC} the prefix .env selects. One environment only: add --prefix <name>."
fi
if [ "$REVEAL" -eq 0 ]; then
    echo -e "${GREEN}To also print sensitive values (password, connection_uri): $0 ${CSP_ARG:+$CSP_ARG }${RESOURCE}${PREFIX_ARG:+ --prefix $PREFIX_ARG} --reveal${NC}"
fi
