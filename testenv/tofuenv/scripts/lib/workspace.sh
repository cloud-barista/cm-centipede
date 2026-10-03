#!/usr/bin/env bash
# ==============================================================================
# workspace.sh — one tofu workspace per name prefix
# ------------------------------------------------------------------------------
#   Sourced by every script that runs tofu against a resource module (bucket,
#   vm, database and ncp/network). Each module keeps one state per prefix, in the
#   local backend's workspace layout:
#
#     tofu/<csp>/<module>/terraform.tfstate.d/<prefix>/terraform.tfstate
#
#   so changing TF_VAR_<csp>_name_prefix in .env starts a new environment next
#   to the old one instead of replacing it. The prefix is always the one .env
#   sets: every script acts on that environment and no other, because the rest
#   of .env (bucket name, engine versions, NFS, ...) describes that one only.
#
#   WHY TF_WORKSPACE AND NOT `tofu workspace select`
#   select writes .terraform/environment, which every run against the module
#   shares. Two prefixes running at once - a 30-minute NCP database next to a
#   destroy - would switch each other's workspace mid-run. TF_WORKSPACE is per
#   process, and applying to a workspace that does not exist yet creates it.
#   The flip side: even a read creates the workspace directory, so "provisioned"
#   is judged by the state file and its resources, never by the directory.
#
#   The caller sets ROOT_DIR and RUNNER, then calls ws_load <csp>.
# ==============================================================================

WS_CSP=""
WS_PREFIX=""

# ws_load <csp> — set WS_PREFIX to TF_VAR_<csp>_name_prefix from .env, or to the
#   module default when .env does not set it. A value that cannot name a workspace
#   stops the caller here, before any tofu command runs.
ws_load() {
    local csp="$1" var="TF_VAR_${1}_name_prefix" p
    WS_CSP="$csp"
    p="$( set -a; . "$ROOT_DIR/.env" 2>/dev/null; set +a; printf %s "${!var:-}" )"
    if [ -z "$p" ]; then
        p="$(sed -n '/variable "'"${csp}"'_name_prefix"/,/^}/s/.*default *= *"\([^"]*\)".*/\1/p' \
             "$ROOT_DIR/tofu/${csp}/vm/variables.tf" | head -1)"
    fi
    # The length limits differ per CSP and are enforced by each module's own
    # validation; only what would break the workspace layout is checked here.
    if ! [[ "$p" =~ ^[a-z][a-z0-9-]+$ ]]; then
        echo -e "\033[0;31m${var}='${p}' is not a valid prefix.\033[0m" >&2
        echo "  Lowercase letters, digits and hyphens, starting with a letter." >&2
        exit 1
    fi
    # "default" is the workspace that held every state before prefixes had one
    # each, and tofu cannot delete it, so it is kept empty rather than reused.
    if [ "$p" = "default" ]; then
        echo -e "\033[0;31m${var}=default is reserved (tofu's default workspace).\033[0m" >&2
        exit 1
    fi
    WS_PREFIX="$p"
}

# ws_exec <docker exec args...> — docker exec into the runner with the prefix's
#   workspace selected.
ws_exec() {
    docker exec -e "TF_WORKSPACE=${WS_PREFIX}" "$RUNNER" "$@"
}

# WS_INIT — `tofu init` for use inside a ws_exec'd bash -c. Serialized across
#   every module and prefix: runs on different prefixes may now overlap, and
#   concurrent inits race on .terraform/ and on the shared plugin cache.
WS_INIT='flock /work/.tofu-plugin-cache/.init.lock tofu init -input=false'

# ws_drop <module> — delete the prefix's workspace once a destroy has emptied it,
#   so list.sh and the directory stay free of environments that no longer exist.
#   Run without TF_WORKSPACE: tofu refuses to delete the workspace in use. A
#   workspace that still tracks resources is refused by tofu as well, so a failed
#   delete is never forced.
ws_drop() {
    local mod="$1"
    docker exec "$RUNNER" bash -c '
        cd "/work/'"$mod"'" 2>/dev/null || exit 0
        tofu workspace delete "'"$WS_PREFIX"'" >/dev/null 2>&1 || true
    '
}

# ws_current <csp> — the prefix .env selects for the CSP, empty when it is not a
#   valid one. ws_load exits on a bad value, which must not end a read-only listing,
#   so it runs in a subshell here.
ws_current() {
    ( ws_load "$1" >/dev/null 2>&1 && printf %s "$WS_PREFIX" ) || true
}

# ws_provisioned — "<csp> <module> <prefix>" for every state that tracks a managed
#   resource, across every prefix. The state files are read directly: tofu can only
#   look at one workspace at a time, and asking it about a workspace that does not
#   exist would create one. Data sources and leftover outputs do not count.
ws_provisioned() {
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
