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
#
#   It also holds assert_no_placeholder, which every entry point runs - the
#   matrix scripts through lib/common.sh. A ChangeMe left in .env would
#   otherwise be registered into OpenBao as if it were a real key.
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

# The keys .env.example ships as ChangeMe. Each must be set by the operator, to a
# value of their choosing or to empty; none falls back to a script default. The
# [CREDENTIAL] keys are empty in normal use, once register-creds.sh has moved
# them into OpenBao.
PLACEHOLDER_VARS="CP_USER CP_PASS SRC_DB_PASS DB_ROOT_PASS
AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DB_PASSWORD
NCP_ACCESS_KEY NCP_SECRET_KEY NCP_DB_PASSWORD"

# assert_no_placeholder — refuse to run while any of PLACEHOLDER_VARS is unset or
#   still holds ChangeMe (case-insensitive, surrounding whitespace ignored).
#   Every offender is listed at once. The caller must have loaded .env.
assert_no_placeholder() {
    local name v bad=""
    for name in $PLACEHOLDER_VARS; do
        if [ -z "${!name+x}" ]; then
            bad="$bad\n    $name  (not set)"
            continue
        fi
        v="$(printf '%s' "${!name}" | tr '[:upper:]' '[:lower:]')"
        v="${v#"${v%%[![:space:]]*}"}"; v="${v%"${v##*[![:space:]]}"}"
        [ "$v" = "changeme" ] && bad="$bad\n    $name  (still ChangeMe)"
    done
    [ -z "$bad" ] && return 0
    echo -e "\033[0;31mThese settings must be changed from ChangeMe (an empty value is allowed):\033[0m" >&2
    printf '%b\n' "$bad" >&2
    echo "  Edit ${ENV_FILE:-.env}, or set them as shell variables." >&2
    echo "  Leave the [CREDENTIAL] keys of a CSP you do not use empty." >&2
    return 1
}
