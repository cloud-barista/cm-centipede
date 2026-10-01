#!/usr/bin/env bash
# ==============================================================================
# env-perm.sh — refuse to read a world-readable .env
# ------------------------------------------------------------------------------
#   Sourced by the two host-side entry points that read .env
#   directly: up.sh and init/openbao/register-creds.sh.
#
#   WHY THIS IS CHECKED AT ALL
#   register-creds.sh blanks the credential keys once they are in OpenBao, so
#   the CSP keys are exposed only between being typed and being registered.
#   VAULT_TOKEN is not blanked - it is the OpenBao *root* token and it stays in
#   .env for good. Anything that can read the file can read every
#   stored credential through it, so the file mode is the whole protection.
#
#   It is refused rather than warned about: a warning scrolls past, and the
#   window it opens is not one you can close after the fact.
# ==============================================================================

# check_env_perm <path> — 600 or 400, or the caller stops.
check_env_perm() {
    local file="$1" mode=""
    if mode="$(stat -c '%a' "$file" 2>/dev/null)"; then :
    elif mode="$(stat -f '%Lp' "$file" 2>/dev/null)"; then :
    else
        return 0    # no stat available; nothing to check against
    fi
    case "$mode" in
        600|400) return 0 ;;
    esac
    echo -e "\033[0;31m.env permissions are ${mode}, expected 600.\033[0m" >&2
    echo "  It holds the OpenBao root token permanently, and your CSP keys" >&2
    echo "  until up.sh registers them:" >&2
    echo "    chmod 600 ${file}" >&2
    return 1
}
