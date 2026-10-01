#!/usr/bin/env bash
# ==============================================================================
# provision.sh — create resources (tofu init + validate + apply)
# ------------------------------------------------------------------------------
#   ./scripts/provision.sh <csp> <resource>
#     aws : bucket | vm | database
#     ncp : bucket | vm | database
#
#   Examples:
#     ./scripts/provision.sh aws bucket
#     ./scripts/provision.sh aws database
#     ./scripts/provision.sh ncp database
#
#   How it works:
#     Runs init -> validate -> apply for /work/tofu/<csp>/<resource> inside the
#     tofuenv-runner container and prints the outputs (connection info) afterwards.
#     Credentials and settings come from /work/.env inside the container, with
#     VAULT_ADDR overridden to the compose network address (http://openbao:8200).
#
#   One environment per prefix:
#     Every module keeps one state per TF_VAR_<csp>_name_prefix (a tofu workspace,
#     see scripts/lib/workspace.sh), and this script only ever applies the prefix
#     .env sets. Changing the prefix starts a new environment next to the old one;
#     the old one is not touched and keeps costing money - ./scripts/list.sh shows
#     what is still provisioned. The bucket name is not derived from the prefix,
#     so it is checked against every other prefix's bucket before the apply.
#
#   Repeated runs:
#     A resource that already holds state is reported and skipped, so running the same
#     command twice costs nothing and creates nothing. Pass --force to apply anyway -
#     needed to converge a module whose previous apply stopped halfway, and to pick up
#     changed .env values.
#
#   NCP notes:
#     - NCP has no default VPC, so tofu/ncp/vm and tofu/ncp/database resolve the
#       VPC/subnet/ACG by name from tofu/ncp/network. That module is handled
#       automatically here and is not a resource you pass on the command line:
#       it is applied first whenever vm or database needs it and the prefix has
#       none yet. deprovision.sh destroys it once no module of the prefix needs it.
#     - Managed databases take roughly 30 minutes to create.
#     - The tofu/ncp/versions module only holds data sources for looking up engine
#       versions, images and specs. Use ./scripts/ncp-db-versions.sh for that.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'

usage() {
    cat >&2 <<'EOF'
Usage: provision.sh <csp> <resource> [--force]
  aws : bucket | vm | database
  ncp : bucket | vm | database

  A resource that already holds state is reported and left alone; --force re-applies
  it, which is what converges a module whose previous apply stopped halfway.

  ncp/network is created automatically when vm or database needs it.
  For AWS engine versions, instance classes and the AMI: ./scripts/aws-db-versions.sh
  For NCP engine versions, images and specs: ./scripts/ncp-db-versions.sh
EOF
    exit 1
}

CSP=""; RESOURCE=""; FORCE=0
for arg in "$@"; do
    case "$arg" in
        -f|--force) FORCE=1 ;;
        -h|--help)  usage ;;
        *)
            if [ -z "$CSP" ]; then
                CSP="$arg"
            elif [ -z "$RESOURCE" ]; then
                RESOURCE="$arg"
            else
                echo "unexpected argument: $arg" >&2; usage
            fi
            ;;
    esac
done
[ -z "$CSP" ] || [ -z "$RESOURCE" ] && usage

case "$CSP" in
    aws|ncp) ;;
    *) echo "invalid csp: $CSP (supported: aws, ncp)" >&2; usage ;;
esac

case "$RESOURCE" in
    bucket|vm|database) ;;
    *) echo "invalid resource for ${CSP}: $RESOURCE" >&2; usage ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE="tofu/${CSP}/${RESOURCE}"

if [ ! -d "$ROOT_DIR/$MODULE" ]; then
    echo -e "${RED}module not found: $MODULE${NC}" >&2; exit 1
fi
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"
ws_load "$CSP"

# has_managed_state <module> — true when the prefix's workspace of the module tracks a
#   real resource. Data sources are filtered out, and outputs are ignored: a module whose
#   resources were removed from state can keep stale outputs, which would otherwise read
#   as "already provisioned".
has_managed_state() {
    local mod="$1" out
    out="$(ws_exec bash -c '
        cd "/work/'"$mod"'" 2>/dev/null || exit 0
        tofu state list 2>/dev/null || true
    ' | grep -v '^data\.' || true)"
    [ -n "$(printf %s "$out" | tr -d '[:space:]')" ]
}

# apply_module <module> [show_outputs] — init + validate + apply inside the runner
apply_module() {
    local mod="$1" show="${2:-yes}"
    ws_exec bash -c '
        set -euo pipefail
        set -a; . /work/.env; set +a
        export VAULT_ADDR=http://openbao:8200
        mkdir -p /work/.tofu-plugin-cache /work/ssh_keys
        cd "/work/'"$mod"'"
        '"$WS_INIT"'
        tofu validate
        tofu apply -auto-approve
        if [ "'"$show"'" = "yes" ]; then
            echo
            echo "=================== OUTPUTS (connection info) ==================="
            tofu output
        fi
    '
}

# bucket_owners — "<prefix> <bucket_name>" for every OTHER prefix whose bucket module
#   holds a bucket. Read straight from the state files: tofu can only read one
#   workspace at a time, and this runs before any apply.
bucket_owners() {
    docker exec "$RUNNER" bash -c '
        cd "/work/tofu/'"$CSP"'/bucket" 2>/dev/null || exit 0
        for f in terraform.tfstate.d/*/terraform.tfstate; do
            [ -f "$f" ] || continue
            ws="${f#terraform.tfstate.d/}"; ws="${ws%/terraform.tfstate}"
            [ "$ws" = "'"$WS_PREFIX"'" ] && continue
            name="$(jq -r ".outputs.bucket_name.value // empty" "$f" 2>/dev/null || true)"
            [ -n "$name" ] && printf "%s %s\n" "$ws" "$name"
        done
    ' || true
}

# check_bucket_name — refuse a bucket name another prefix already uses.
#   The bucket is the one resource whose name is not derived from the prefix, so two
#   environments share it unless .env is changed as well. Left to the apply, AWS
#   answers BucketAlreadyOwnedByYou and NCP a conflict, neither of which says which
#   environment owns it.
check_bucket_name() {
    local var="TF_VAR_${CSP}_bucket_name" name owner other
    name="$( set -a; . "$ROOT_DIR/.env" 2>/dev/null; set +a; printf %s "${!var:-}" )"
    [ -n "$name" ] || return 0
    while read -r owner other; do
        [ -n "$owner" ] || continue
        if [ "$other" = "$name" ]; then
            echo -e "${RED}=== bucket '${name}' already belongs to prefix '${owner}' ===${NC}" >&2
            echo "  The bucket name is not derived from the prefix, so each environment needs" >&2
            echo "  its own. Set another ${var} in .env for prefix '${WS_PREFIX}'." >&2
            exit 1
        fi
    done <<< "$(bucket_owners)"
}

# Already provisioned? Report it and stop, so a repeated command is a no-op instead of
# a fresh apply. Tracking a resource is the signal; it does not prove the module applied
# cleanly to the end, which is why --force exists to re-apply and converge one that
# stopped halfway.
if [ "$FORCE" -eq 0 ] && has_managed_state "$MODULE"; then
    echo -e "${YELLOW}=== ${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) is already provisioned - nothing to do ===${NC}"
    ws_exec bash -c '
        cd "/work/'"$MODULE"'"
        tofu output 2>/dev/null || true
    ' | sed 's/^/  /'
    echo
    echo "  Connection info  :  ./scripts/conn-info.sh ${CSP} ${RESOURCE}"
    echo "  Re-apply anyway  :  ./scripts/provision.sh ${CSP} ${RESOURCE} --force"
    echo "  Destroy          :  ./scripts/deprovision.sh ${CSP} ${RESOURCE}"
    exit 0
fi

if [ "$RESOURCE" = "bucket" ]; then
    check_bucket_name
fi

# NCP vm/database resolve the VPC, subnet and ACG by name from tofu/ncp/network,
# so it has to exist first. It is applied here rather than exposed as a command.
# The network lives in the same prefix's workspace, which is what keeps the by-name
# lookups matching: the names embed the prefix the workspace is named after.
if [ "$CSP" = "ncp" ] && { [ "$RESOURCE" = "vm" ] || [ "$RESOURCE" = "database" ]; }; then
    if ! has_managed_state "tofu/ncp/network"; then
        echo -e "${YELLOW}=== prerequisite: creating ncp/network (VPC + PUBLIC subnet + ACG) ===${NC}"
        apply_module "tofu/ncp/network" no
        echo -e "${GREEN}ncp/network ready.${NC}"
        echo
    fi
fi

echo -e "${CYAN}=== provision: ${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) ===${NC}"
if [ "$CSP" = "ncp" ] && [ "$RESOURCE" = "database" ]; then
    echo -e "${YELLOW}Managed DB creation takes ~30 minutes. Do not interrupt this command.${NC}"
fi

apply_module "$MODULE"
echo -e "${GREEN}=== done: ${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) ===${NC}"
echo "  Reveal a sensitive output (password, connection_uri, ...):"
echo "    docker exec -e TF_WORKSPACE=${WS_PREFIX} ${RUNNER} bash -c 'cd /work/${MODULE} && tofu output -raw <output_name>'"
echo "  Every provisioned environment, all prefixes:  ./scripts/list.sh"

if [ "$CSP" = "ncp" ] && [ "$RESOURCE" = "database" ]; then
    echo
    echo -e "${YELLOW}Next step — managed DBs are not reachable from outside yet.${NC}"
    echo "  A public domain must be issued once per DB server in the NCP console:"
    echo "    Database > Cloud DB for <engine> > select the DB server > DB Management > Public domain"
    echo "  Then reflect and verify it with:  ./scripts/ncp-db-domain.sh"
fi
