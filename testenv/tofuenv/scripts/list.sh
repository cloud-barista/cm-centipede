#!/usr/bin/env bash
# ==============================================================================
# list.sh — every provisioned environment, across all prefixes
# ------------------------------------------------------------------------------
#   ./scripts/list.sh
#
#   Why:
#     Each TF_VAR_<csp>_name_prefix is an environment of its own (a tofu
#     workspace, see scripts/lib/workspace.sh), and every other script acts on
#     the prefix .env sets and nothing else. Changing the prefix therefore leaves
#     the previous environment running - and billed - out of their sight. This
#     is the one place that looks at all of them.
#
#   How it works:
#     Read-only. The state files are read directly rather than through tofu,
#     which can only look at one workspace at a time. A module counts as
#     provisioned when its state tracks a managed resource; data sources and
#     leftover outputs do not count.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

case "${1:-}" in
    "") ;;
    -h|--help) echo "Usage: $0" >&2; exit 0 ;;
    *) echo -e "${RED}unknown argument: $1${NC}" >&2; echo "Usage: $0" >&2; exit 1 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"

ROWS="$(ws_provisioned)"

echo -e "${CYAN}=== provisioned environments ===${NC}"
if [ -z "$ROWS" ]; then
    echo "  Nothing is provisioned, under any prefix."
    exit 0
fi

printf '  %-4s  %-12s  %-6s  %-3s  %-8s  %s\n' CSP PREFIX bucket vm database network
for csp in aws ncp; do
    current="$(ws_current "$csp")"
    for ws in $(printf '%s\n' "$ROWS" | awk -v c="$csp" '$1 == c { print $3 }' | sort -u); do
        cells=()
        for mod in bucket vm database network; do
            if [ "$csp" = "aws" ] && [ "$mod" = "network" ]; then
                cells+=("")
            elif printf '%s\n' "$ROWS" | grep -qxF "$csp $mod $ws"; then
                cells+=("yes")
            else
                cells+=("-")
            fi
        done
        label="$ws"
        [ "$ws" = "$current" ] && label="$ws *"
        printf '  %-4s  %-12s  %-6s  %-3s  %-8s  %s\n' "$csp" "$label" "${cells[@]}"
    done
done

echo
echo -e "  ${GREEN}*${NC} the prefix .env selects - the environment every other script acts on."
echo "  To act on another one, put its prefix in TF_VAR_<csp>_name_prefix in .env."
