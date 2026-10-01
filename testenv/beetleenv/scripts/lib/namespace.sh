#!/usr/bin/env bash
# ==============================================================================
# lib/namespace.sh — the cb-tumblebug namespace every migration call needs
# ------------------------------------------------------------------------------
#   cm-beetle has no namespace API. The routes exist in pkg/api/rest/server.go
#   but are commented out, and every migration handler starts by reading the
#   namespace and failing if it is not there. So this is the one place beetleenv
#   talks to cb-tumblebug directly; everything else goes through beetle.
#
#   The namespace is created by up.sh and never deleted. It outlives every
#   resource in it, so a teardown can be followed straight by another provision,
#   and anything else sharing the namespace is unaffected by either. Every other
#   script only requires that it exists.
#
#   Deleting it is a cb-tumblebug operation, not a beetleenv one:
#     curl -X DELETE ${TUMBLEBUG_URL}/ns/${BEETLEENV_NS}
# ==============================================================================

if [ -n "${BEETLEENV_NAMESPACE_SH:-}" ]; then return 0; fi
BEETLEENV_NAMESPACE_SH=1

# shellcheck source=./beetle.sh
. "$(dirname "${BASH_SOURCE[0]}")/beetle.sh"

# ns_exists — cb-tumblebug answers 404 for a namespace it does not hold.
ns_exists() {
    tb_get "/ns/$(urlq "$BEETLEENV_NS")" >/dev/null 2>&1
}

ensure_ns() {
    local body
    if ns_exists; then
        log_info "namespace ${BEETLEENV_NS} already exists"
        return 0
    fi

    body="$(jq -n --arg name "$BEETLEENV_NS" \
        '{name: $name, description: "Test resources for cm-centipede, created by beetleenv"}')"

    if ! tb_post "/ns" "$body"; then
        die "failed to create namespace ${BEETLEENV_NS} - $(bt_message)"
    fi
    log_ok "created namespace ${BEETLEENV_NS}"
}

# require_ns — every entry point that provisions or reads resources calls this,
#   so "run up.sh first" is the message instead of a namespace error from three
#   calls further in.
require_ns() {
    if [ -z "${TUMBLEBUG_URL:-}" ]; then
        # Without a tumblebug URL the existence check is impossible. Let the
        # migration call report it rather than refusing to try.
        return 0
    fi
    if ! ns_exists; then
        die "namespace ${BEETLEENV_NS} does not exist on cb-tumblebug.
       ./scripts/up.sh creates it."
    fi
}
