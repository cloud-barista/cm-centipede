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

# current_prefix <csp> — the prefix .env selects, empty when it is not a valid one.
#   ws_load exits on a bad value, which must not end a read-only listing.
current_prefix() {
    ( ws_load "$1" >/dev/null 2>&1 && printf %s "$WS_PREFIX" ) || true
}

# provisioned — "<csp> <module> <workspace>" for every state holding a managed
#   resource.
provisioned() {
    docker exec "$RUNNER" bash -c '
        managed() { jq -e "[.resources[]? | select(.mode == \"managed\")] | length > 0" "$1" >/dev/null 2>&1; }
        for dir in /work/tofu/aws/bucket /work/tofu/aws/vm /work/tofu/aws/database \
                   /work/tofu/ncp/bucket /work/tofu/ncp/vm /work/tofu/ncp/database /work/tofu/ncp/network; do
            [ -d "$dir" ] || continue
            rel="${dir#/work/tofu/}"; csp="${rel%%/*}"; mod="${rel#*/}"
            for f in "$dir"/terraform.tfstate.d/*/terraform.tfstate; do
                [ -f "$f" ] || continue
                ws="${f%/terraform.tfstate}"; ws="${ws##*/}"
                managed "$f" && printf "%s %s %s\n" "$csp" "$mod" "$ws"
            done
        done
    '
}

ROWS="$(provisioned)"

echo -e "${CYAN}=== provisioned environments ===${NC}"
if [ -z "$ROWS" ]; then
    echo "  Nothing is provisioned, under any prefix."
    exit 0
fi

printf '  %-4s  %-12s  %-6s  %-3s  %-8s  %s\n' CSP PREFIX bucket vm database network
for csp in aws ncp; do
    current="$(current_prefix "$csp")"
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
