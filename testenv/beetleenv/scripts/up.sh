#!/usr/bin/env bash
# ==============================================================================
# up.sh — check the stack is there, and create the namespace
# ------------------------------------------------------------------------------
#   beetleenv starts nothing. cm-beetle, cb-tumblebug and cb-spider all run
#   elsewhere, and the CSP credentials belong to cb-tumblebug - so there is no
#   secret store to bring up here and no connection to register. What is left is
#   a namespace, which cm-beetle cannot create itself.
#
#   The namespace it creates is never deleted again - not by deprovision.sh, not
#   by anything here. It outlives every resource in it, so a teardown can be
#   followed straight by another provision.
#
#   Safe to re-run: everything here is a check or a create-if-absent.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/namespace.sh
. "${SCRIPT_DIR}/lib/namespace.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/up.sh [--help]

Verifies cm-beetle and cb-tumblebug are reachable, checks the .env settings for
every CSP in BEETLEENV_CSPS, and creates the namespace if it is not there.

Creates no cloud resources. ./scripts/provision.sh does that.
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
    "") ;;
    *) usage >&2; die "unknown argument: $1" ;;
esac

preflight

log_step "cm-beetle at ${BEETLE_URL}"
log_ok "beetle is ready"
if [ -n "${TUMBLEBUG_URL:-}" ]; then
    log_ok "tumblebug is ready at ${TUMBLEBUG_URL}"
fi

# ------------------------------------------------------------------------------
# Per-CSP settings
# ------------------------------------------------------------------------------
# A CSP short of a key is reported and stepped over rather than aborting the run:
# the point of one pass is to list everything that needs filling in.

log_step "checking .env settings"

READY_CSPS=""
FAILED_CSPS=""

for CSP in ${BEETLEENV_CSPS:-}; do
    validate_csp "$CSP"
    if require_csp_env "$CSP"; then
        READY_CSPS="${READY_CSPS}${READY_CSPS:+ }${CSP}"
        log_ok "${CSP}: $(connection_name "$CSP")"
    else
        FAILED_CSPS="${FAILED_CSPS}${FAILED_CSPS:+ }${CSP}"
    fi
done

if [ -z "${BEETLEENV_CSPS:-}" ]; then
    log_warn "BEETLEENV_CSPS is empty - nothing to check"
fi

# ------------------------------------------------------------------------------
# Namespace
# ------------------------------------------------------------------------------

log_step "namespace ${BEETLEENV_NS}"

if [ -z "${TUMBLEBUG_URL:-}" ]; then
    die "TUMBLEBUG_URL is not set.
       cm-beetle has no namespace API - its routes are commented out - so the
       namespace has to be created on cb-tumblebug directly. This is the only
       call beetleenv makes outside beetle."
fi
ensure_ns

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------

printf '\n'
log_step "ready"
printf '  namespace      %s\n' "$BEETLEENV_NS"
printf '  name prefix    %s\n' "$BEETLEENV_NAME_PREFIX"
printf '  CSPs ready     %s\n' "${READY_CSPS:-<none>}"
if [ -n "$FAILED_CSPS" ]; then
    printf '  CSPs missing settings  %s\n' "$FAILED_CSPS"
fi
printf '\n'
printf '  Next:\n'
printf '    ./scripts/catalog.sh <csp> rdbms       what the CSP offers\n'
printf '    ./scripts/provision.sh <csp> database  create the DB instances\n'
printf '    ./scripts/provision.sh <csp> vm\n'
printf '    ./scripts/provision.sh <csp> bucket\n'
printf '    ./scripts/status.sh                    what is up\n'
printf '    ./scripts/deprovision.sh <csp> all     delete it again\n'
printf '\n'

# A CSP that cannot be used is a failure of this run, not a warning to scroll
# past: nothing downstream would work for it.
if [ -n "$FAILED_CSPS" ]; then
    exit 1
fi
