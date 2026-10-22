#!/usr/bin/env bash
# ==============================================================================
# ncp-db-domain.sh — reflect and verify the NCP managed-DB public domains and ACGs
# ------------------------------------------------------------------------------
#   ./scripts/ncp-db-domain.sh [csp] [--wait]
#
# WHY THIS EXISTS
#   A managed DB on NCP answers on a private domain until a public one is issued,
#   and issuing it is a console action with no API behind it:
#
#     NCP console > Database > Cloud DB for <engine> > select the DB server
#       > DB Management > Public domain > request
#
#   Nothing beetleenv sends can do it - cb-spider even refuses a create with
#   publicAccess=true on NCP, which is why BEETLEENV_NCP_DB_PUBLIC_ACCESS must be
#   false. So the workflow is: create here, issue there, then come back and check.
#
#   A public domain has to be re-issued whenever the instance is re-created.
#
#   The domain alone is not enough. NCP puts the instance behind an ACG of its
#   own with no inbound rule; BEETLEENV_NCP_DB_ACG_ALLOW_ALL_INBOUND=true has
#   cb-spider add TCP 0.0.0.0/0 -> DB port to it. cb-spider treats a failure there
#   as a warning and the create as a success, so this script checks the rule too.
#
# WHAT "SYNC" MEANS HERE
#   Nothing local is stored, so there is no state file to refresh. What goes
#   stale is cb-tumblebug's own record: it serves list endpoints from its
#   key-value store without asking the CSP, while a single-resource GET calls
#   cb-spider, overwrites endpoint / publicAccess / status from the live answer,
#   and writes the record back (core/resource/rdbms.go GetRDBMS).
#
#   So one GET per instance is the sync. That is all this script does, plus
#   reading the result back in a form that says which engines still need the
#   console step.
#
# HOW A PUBLIC DOMAIN IS DETECTED
#   Not by parsing the hostname. cb-spider's NCP driver picks the public domain
#   when there is one and falls back to the private one otherwise, and sets
#   PublicAccess to say which it used (ncp/resources/RDBMSHandler.go):
#
#     if serverInst.PublicDomain != "" { domain = PublicDomain;  PublicAccess = true }
#     else                             { domain = PrivateDomain; PublicAccess = false }
#
#   cb-tumblebug copies that field verbatim on refresh, so publicAccess is an
#   observation of what the CSP has, not an echo of what was asked for.
#
#   ncpDBACGToAllowAllInbound is the same kind of observation: cb-spider
#   recomputes NCPAutoOpenACG from the tag it left on the rule it added, so false
#   means the rule is not on the ACG.
#
# NCP ONLY, ON PURPOSE
#   The csp argument exists so the body has nothing NCP-specific in it, but the
#   name does not generalise: no other CSP beetleenv supports puts a managed DB's
#   reachability behind a console-only step. Should one appear, this becomes its
#   script too - by being renamed, not by growing a flag.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/beetle.sh
. "${SCRIPT_DIR}/lib/beetle.sh"
# shellcheck source=./lib/namespace.sh
. "${SCRIPT_DIR}/lib/namespace.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/ncp-db-domain.sh [csp] [--wait]

Refresh cb-tumblebug's record of each managed DB from the CSP, then report which
engines are reachable from outside - a public domain and an ACG inbound rule -
and which still need a console step.

Arguments:
  csp        defaults to ncp; the only CSP this applies to today

Options:
  --wait     keep checking until every engine is reachable, instead of
             reporting once. For leaving running while you work through the
             console.

Exit status is 1 while any engine still lacks a public domain or the ACG rule,
so this can gate a script that needs the endpoint to be reachable.

Examples:
  ./scripts/ncp-db-domain.sh
  ./scripts/ncp-db-domain.sh --wait
EOF
}

CSP=""
WAIT=0

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --wait)    WAIT=1 ;;
        -*)        usage >&2; die "unknown option: $1" ;;
        *)
            if [ -z "$CSP" ]; then CSP="$1"
            else usage >&2; die "unexpected argument: $1"
            fi
            ;;
    esac
    shift
done

CSP="${CSP:-ncp}"

preflight
validate_csp "$CSP"
CSP="$(csp_lower "$CSP")"
require_csp_env "$CSP" || exit 1
require_ns

NS_PATH="$(urlq "$BEETLEENV_NS")"

# How often --wait looks again. A console request takes a minute or two to become
# visible, and each pass is one call per engine through beetle's pacer.
DOMAIN_POLL_INTERVAL="${BEETLEENV_DOMAIN_POLL_INTERVAL:-20}"
DOMAIN_TIMEOUT="${BEETLEENV_DOMAIN_TIMEOUT:-1800}"

engines_of() {
    local engines
    engines="$(csp_env "$CSP" DB_ENGINES)"
    if [ -z "$engines" ]; then
        die "BEETLEENV_$(csp_upper "$CSP")_DB_ENGINES is empty, so there is nothing to check."
    fi
    printf '%s' "$engines"
}

# check_all — one refresh pass. Prints a line per engine and returns the number
#   still without a public domain or the ACG rule, so the caller decides what to
#   do about it.
check_all() {
    local engine name missing=0 endpoint public acg status

    for engine in $(engines_of); do
        engine="$(csp_lower "$engine")"
        name="$(resource_name "$CSP" "db-${engine}")"

        # The GET is the refresh: it makes cb-tumblebug re-read from the CSP and
        # store what it finds.
        if ! bt_get "/migration/middleware/ns/${NS_PATH}/rdbms/$(urlq "$name")"; then
            printf '  %-9s %-8s %s\n' "ABSENT" "$engine" \
                "no such instance - ./scripts/provision.sh ${CSP} database --engine ${engine}"
            missing=$((missing + 1))
            continue
        fi

        endpoint="$(bt_data '.endpoint')"
        public="$(bt_data '.publicAccess')"
        # The field is NCP's alone; elsewhere there is no such rule to look for.
        acg=true
        [ "$CSP" = "ncp" ] && acg="$(bt_data '.ncpDBACGToAllowAllInbound // false')"
        status="$(bt_data '.status')"

        if [ "$public" = "true" ] && [ "$acg" = "true" ]; then
            printf '  %-9s %-8s %s\n' "OK" "$engine" "${endpoint:-(none)}"
        elif [ "$public" = "true" ]; then
            # A public domain behind a closed ACG times out rather than refusing,
            # which is the hardest failure to read from cm-centipede's side.
            printf '  %-9s %-8s %s\n' "NO-ACG" "$engine" \
                "public domain ${endpoint:-(none)}, but no ACG inbound rule from cb-spider"
            missing=$((missing + 1))
        else
            printf '  %-9s %-8s %s\n' "MISSING" "$engine" \
                "still on the private domain${endpoint:+ (${endpoint})}"
            # Worth saying only when it explains the missing domain: a public one
            # cannot be issued for an instance that is not up yet.
            if [ -n "$status" ] && [ "$status" != "Available" ]; then
                printf '  %-9s %-8s %s\n' "" "" "status is ${status}; it has to be Available first"
            fi
            missing=$((missing + 1))
        fi
    done

    return "$missing"
}

console_hint() {
    printf '\n'
    if [ "$CSP" != "ncp" ]; then
        # The refresh above is CSP-neutral and its reading of publicAccess is
        # correct anywhere. The remedy is not: this console path is NCP's, and on
        # another CSP a false publicAccess means something else entirely -
        # usually that the create asked for a private instance.
        printf '  publicAccess is false, but the console step below is NCP'\''s.\n'
        printf '  On %s, check how that CSP exposes a managed DB before following it.\n' "$CSP"
        printf '\n'
    fi
    printf '  MISSING - issue a public domain per engine, in the NCP console:\n'
    printf '    Database > Cloud DB for <engine> > select the DB server\n'
    printf '      > DB Management > Public domain > request\n'
    printf '\n'
    printf '  NO-ACG - add the inbound rule, in the NCP console:\n'
    printf '    Server > ACG > the ACG of the DB server > Inbound > TCP <db port>\n'
    if [ "$(db_acg_open "$CSP")" = "true" ]; then
        printf '    (BEETLEENV_NCP_DB_ACG_ALLOW_ALL_INBOUND=true was asked for, so cb-spider\n'
        printf '     failed to add it, or the instance was created before the setting existed.)\n'
    else
        printf '    (BEETLEENV_NCP_DB_ACG_ALLOW_ALL_INBOUND is false, so nothing adds it for you.)\n'
    fi
}

# ------------------------------------------------------------------------------

printf '\n'
log_step "${CSP}: refreshing managed DB records from the CSP"

if [ "$WAIT" -eq 0 ]; then
    missing=0
    check_all || missing=$?

    printf '\n'
    if [ "$missing" -eq 0 ]; then
        log_ok "every managed DB on ${CSP} has a public domain and an ACG inbound rule."
        printf '  Connection info:  ./scripts/conn-info.sh %s --reveal\n\n' "$CSP"
        exit 0
    fi

    log_warn "${missing} engine(s) are not reachable yet (no public domain or no ACG rule)."
    console_hint
    printf '    Then run this again, or ./scripts/ncp-db-domain.sh %s --wait\n\n' "$CSP"
    exit 1
fi

# --wait. The hint comes first here: the point of waiting is to work through the
# console while it watches, so the instructions have to be on screen before the
# polling starts rather than after it ends.
missing=0
check_all || missing=$?
if [ "$missing" -eq 0 ]; then
    printf '\n'
    log_ok "every managed DB on ${CSP} has a public domain and an ACG inbound rule."
    printf '\n'
    exit 0
fi
console_hint
printf '\n'
log_info "watching every ${DOMAIN_POLL_INTERVAL}s; Ctrl-C to stop"

waited=0
while [ "$waited" -lt "$DOMAIN_TIMEOUT" ]; do
    sleep "$DOMAIN_POLL_INTERVAL"
    waited=$((waited + DOMAIN_POLL_INTERVAL))

    missing=0
    check_output="$(check_all)" || missing=$?
    if [ "$missing" -eq 0 ]; then
        printf '\n%s\n\n' "$check_output"
        log_ok "every managed DB on ${CSP} has a public domain and an ACG inbound rule."
        printf '  Connection info:  ./scripts/conn-info.sh %s --reveal\n\n' "$CSP"
        exit 0
    fi
    printf '  %s elapsed, %d still missing\n' "${waited}s" "$missing"
done

printf '\n'
log_error "gave up after ${DOMAIN_TIMEOUT}s with ${missing} engine(s) still missing a public domain or the ACG rule."
printf '  Nothing was changed. Run again once the console request is through.\n\n'
exit 1
