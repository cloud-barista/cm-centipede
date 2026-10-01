#!/usr/bin/env bash
# ==============================================================================
# lib/common.sh — shared helpers for every beetleenv entry point
# ------------------------------------------------------------------------------
#   preflight()   environment checks every script runs before doing anything
#   state_*       state/<ns>/<prefix>/<csp>/*.json read, atomic write
#   poll_for      polling with an interval and a timeout
#   csp_*         CSP name handling and per-CSP .env lookup
#
#   cm-beetle, cb-tumblebug and cb-spider all run elsewhere; beetleenv starts
#   nothing. preflight() is what makes any script safe to run first: it fails
#   with a real message instead of depending on what was run before it.
#
#   Sourced, not executed. Callers set `set -euo pipefail` themselves.
# ==============================================================================

# Guard against double sourcing (provision.sh sources several libs, each of
# which sources this one).
if [ -n "${BEETLEENV_COMMON_SH:-}" ]; then return 0; fi
BEETLEENV_COMMON_SH=1

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

BEETLEENV_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ENV_FILE:-${BEETLEENV_ROOT}/.env}"
STATE_ROOT="${BEETLEENV_ROOT}/state"

# Per-run scratch space.
BEETLEENV_TMP="$(mktemp -d "${TMPDIR:-/tmp}/beetleenv.XXXXXX")"
trap 'rm -rf "${BEETLEENV_TMP}"' EXIT

# Polling. Overridable for testing.
POLL_INTERVAL="${BEETLEENV_POLL_INTERVAL:-15}"
VM_TIMEOUT="${BEETLEENV_VM_TIMEOUT:-900}"          # 15 minutes
RDBMS_TIMEOUT="${BEETLEENV_RDBMS_TIMEOUT:-2400}"   # 40 minutes
BUCKET_TIMEOUT="${BEETLEENV_BUCKET_TIMEOUT:-300}"  # 5 minutes
# A delete that cb-tumblebug could not confirm on the CSP is retried this many
# times, this far apart. Both are for eventual consistency after a managed
# database goes away: the CSP releases what it held asynchronously, and until it
# does, the vNet around it will not delete. See bt_delete_retry in lib/beetle.sh.
DELETE_RETRY_WAIT="${BEETLEENV_DELETE_RETRY_WAIT:-60}"
DELETE_RETRIES="${BEETLEENV_DELETE_RETRIES:-3}"

# ------------------------------------------------------------------------------
# Logging
# ------------------------------------------------------------------------------

log_info()  { printf "${CYAN}[INFO]${NC}  %s\n" "$*"; }
log_ok()    { printf "${GREEN}[OK]${NC}    %s\n" "$*"; }
log_warn()  { printf "${YELLOW}[WARN]${NC}  %s\n" "$*" >&2; }
log_error() { printf "${RED}[ERROR]${NC} %s\n" "$*" >&2; }
log_step()  { printf "${CYAN}==>${NC} %s\n" "$*"; }
die()       { log_error "$*"; exit 1; }

# mask <value> — render a secret for console output.
mask() {
    if [ -z "${1:-}" ]; then printf '<empty>'; else printf '<sensitive>'; fi
}

# ------------------------------------------------------------------------------
# .env
# ------------------------------------------------------------------------------

# env_val <NAME> — value of a variable named at runtime, empty when unset.
env_val() {
    local name="$1"
    printf '%s' "${!name:-}"
}

load_env() {
    if [ ! -f "$ENV_FILE" ]; then
        die ".env not found at ${ENV_FILE}
       cp ${BEETLEENV_ROOT}/.env.example ${ENV_FILE} && chmod 600 ${ENV_FILE}"
    fi
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
}

# check_env_perm — .env is a store here, not an intake form: the RDBMS
#   admin password stays in it for the life of the environment, because every
#   "create the initial database" call re-sends it. File permissions are the only
#   thing protecting it, so a loose mode is refused rather than warned about.
check_env_perm() {
    local mode=""
    if mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null)"; then :
    elif mode="$(stat -f '%Lp' "$ENV_FILE" 2>/dev/null)"; then :
    else
        return 0    # no stat available; nothing to check against
    fi
    case "$mode" in
        600|400) return 0 ;;
    esac
    die ".env permissions are ${mode}, expected 600.
       It holds the RDBMS admin password in plaintext:
         chmod 600 ${ENV_FILE}"
}

# need_env / assert_env — collect every missing key first, then report them
#   together. One run tells you everything to fill in, instead of one key a run.
MISSING_ENV=()
need_env() {
    local name="$1" hint="${2:-}"
    if [ -z "$(env_val "$name")" ]; then
        if [ -n "$hint" ]; then
            MISSING_ENV+=("${name}    ${hint}")
        else
            MISSING_ENV+=("${name}")
        fi
    fi
}

# assert_env <message> — report what need_env collected. Returns 1 rather than
#   exiting, so a caller looping over CSPs can drop just the one that is short a
#   key. Single-CSP callers turn it into an exit themselves.
assert_env() {
    if [ "${#MISSING_ENV[@]}" -eq 0 ]; then return 0; fi
    log_error "${1:-.env is incomplete}. Missing keys in ${ENV_FILE}:"
    printf '         %s\n' "${MISSING_ENV[@]}" >&2
    MISSING_ENV=()
    return 1
}

# ------------------------------------------------------------------------------
# CSP naming
# ------------------------------------------------------------------------------
#
# Everything beetleenv creates is named <prefix>-<kind>-<nn>, where the prefix is
# beetle's nameSeed: the migration APIs apply it themselves at creation time
# (late binding), so the recommendation never carries it.
#
#   nameSeed cpbt  ->  cpbt-rdbms-01, cpbt-os-01, cpbt-infra-01
#
# beetleenv-owned network resources are the exception: they are created directly
# through the resource APIs, which have no nameSeed, so the prefix is applied
# here instead.

csp_lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
csp_upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }

# The CSPs cm-beetle recognises for a desiredCsp. Beetle validates the pair
# against tumblebug anyway; this list only catches a typo before a network call.
BEETLEENV_ALL_CSPS="aws azure gcp alibaba tencent ibm openstack ncp nhn kt"

validate_csp() {
    local csp c
    csp="$(csp_lower "$1")"
    for c in $BEETLEENV_ALL_CSPS; do
        if [ "$c" = "$csp" ]; then return 0; fi
    done
    die "unknown csp: ${1}
       supported: ${BEETLEENV_ALL_CSPS}"
}

# csp_env <csp> <suffix> — value of BEETLEENV_<CSP>_<suffix>.
csp_env() {
    env_val "BEETLEENV_$(csp_upper "$1")_${2}"
}

# csp_region <csp> — the region every API call for this CSP is addressed to.
csp_region() { csp_env "$1" REGION; }

# int_env <csp> <suffix> <default> — a numeric .env value, for the request bodies
#   built with `jq --argjson`. jq rejects a non-number outright, which would
#   surface as a jq parse error rather than as the typo it is.
int_env() {
    local raw
    raw="$(csp_env "$1" "$2")"
    if [ -z "$raw" ]; then
        printf '%s' "$3"
        return 0
    fi
    case "$raw" in
        ''|*[!0-9]*)
            die "BEETLEENV_$(csp_upper "$1")_${2}='${raw}' is not a whole number" ;;
    esac
    printf '%s' "$raw"
}

# bool_env <csp> <suffix> <default> — the same for a JSON boolean. An unrecognised
#   value stops the run: jq would turn it into the string "maybe" and the CSP
#   would read that as false.
bool_env() {
    local raw
    raw="$(csp_env "$1" "$2")"
    if [ -z "$raw" ]; then
        printf '%s' "$3"
        return 0
    fi
    case "$(csp_lower "$raw")" in
        true|yes|1)  printf 'true' ;;
        false|no|0)  printf 'false' ;;
        *) die "BEETLEENV_$(csp_upper "$1")_${2}='${raw}' is not a boolean (true or false)" ;;
    esac
}

# connection_name <csp> — beetle resolves connections as <csp>-<region> and
#   nothing else (pkg/core/migration/object-storage.go GenerateConnectionName),
#   so this is not a convention beetleenv gets to choose. A connection registered
#   on tumblebug under any other name is invisible to beetle.
#   Lower-cased because beetle lower-cases it too - RecommendRDBMS lower-cases
#   the region before building the name, and GenerateConnectionName lower-cases
#   the whole thing - so NCP's "KR" resolves to ncp-kr either way.
connection_name() {
    csp_lower "$(printf '%s-%s' "$1" "$(csp_region "$1")")"
}

# resource_name <csp> <suffix> — <prefix>-<csp>-<suffix>.
#
#   The CSP is part of the name because one namespace holds every CSP's
#   resources, and a cb-tumblebug resource id is unique per namespace and type,
#   not per connection. Without it, "cpbt-vnet" would mean the AWS vNet and the
#   NCP vNet at once - and a per-CSP teardown would delete the other one's.
#
#   These are cb-tumblebug ids, not CSP resource names: tumblebug generates the
#   name the CSP sees from a uid, so no CSP id length limit applies here.
resource_name() {
    printf '%s-%s-%s' "$BEETLEENV_NAME_PREFIX" "$(csp_lower "$1")" "$2"
}

# name_prefix_of <csp> — what every resource of ours on this CSP starts with.
#   deprovision and conn-info select on it, so anything else in the namespace is
#   left alone.
name_prefix_of() {
    printf '%s-%s-' "$BEETLEENV_NAME_PREFIX" "$(csp_lower "$1")"
}

validate_name_prefix() {
    BEETLEENV_NAME_PREFIX="${BEETLEENV_NAME_PREFIX:-cpbt}"
    # beetle's own rule (common.IsValidNameSeed) is 20 chars, alphanumeric start,
    # alphanumeric and hyphens after. Lower case and a 10 char ceiling are ours:
    # the longest name built from it is <prefix>-subnet-1 and CSP id limits start
    # at 30, and a mixed-case seed produces resource names some CSPs reject.
    #
    # No hyphen is ours too, and it is what makes name_prefix_of an exact match.
    # Every "is this ours" test is startswith("<prefix>-<csp>-"), and with a
    # hyphen allowed one prefix can be the start of another's names: "cp" would
    # claim everything "cp-aws-x" created, since "cp-aws-x-aws-vnet" starts with
    # "cp-aws-". Without one, the first segment of a name is its prefix, whole.
    if ! printf '%s' "$BEETLEENV_NAME_PREFIX" | grep -Eq '^[a-z][a-z0-9]{1,9}$'; then
        die "BEETLEENV_NAME_PREFIX='${BEETLEENV_NAME_PREFIX}' is invalid.
       Expected ^[a-z][a-z0-9]{1,9}\$ - 2 to 10 lower-case letters and digits,
       starting with a letter. No hyphen: names are matched on <prefix>-<csp>-,
       and a hyphenated prefix can match another prefix's resources."
    fi
}

# validate_ns — the namespace name, which cb-tumblebug uses verbatim as the
#   namespace id (CreateNs sets Id = Name, with no normalisation).
#
#   The rule mirrors tumblebug's own CheckString: a letter first, and not a
#   hyphen last. Checked here so a bad name fails in preflight rather than in the
#   POST that creates it. Lower case and the 30 character ceiling are ours -
#   tumblebug would take upper case and '+', but the name ends up in URLs and log
#   lines all over these scripts.
validate_ns() {
    if [ -z "${BEETLEENV_NS:-}" ]; then
        die "BEETLEENV_NS is not set in ${ENV_FILE}"
    fi
    if ! printf '%s' "$BEETLEENV_NS" | grep -Eq '^[a-z][a-z0-9-]{0,28}[a-z0-9]$'; then
        die "BEETLEENV_NS='${BEETLEENV_NS}' is invalid.
       Expected 2 to 30 characters: lower-case letters, digits and hyphens,
       starting with a letter and not ending in a hyphen.
       cb-tumblebug enforces the first and last of those itself (CheckString)."
    fi
}

# ------------------------------------------------------------------------------
# preflight
# ------------------------------------------------------------------------------

# preflight_env — everything that can be checked without touching the network.
preflight_env() {
    local cmd
    for cmd in curl jq; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            die "required command not found: ${cmd}"
        fi
    done

    load_env
    check_env_perm

    need_env BEETLE_URL "# e.g. http://localhost:8056/beetle"
    # Not reachable without this, whichever script asked - so this one exits.
    assert_env "cm-beetle connection settings are missing" || exit 1

    validate_name_prefix
    validate_ns
    warn_legacy_layout
}

# warn_legacy_layout — files left in the old state/<csp>/ and keys/<csp>/ layout,
#   from before the namespace and prefix were part of the path.
#
#   Nothing reads them any more, so they are harmless - but a key there is the
#   only saved copy, and a deprovision no longer cleans them up. Warned about
#   rather than moved: the files say which prefix made them but not which
#   namespace, and a guess would file them under the wrong one.
#
#   A file whose name carries this setup's <prefix>-<csp>- gets the command that
#   moves it under the current namespace; anything else is only listed.
#
#   An old-layout directory holds files directly, and a new-layout one holds only
#   directories, so the two cannot be mistaken for each other even when a
#   namespace is named like a CSP.
warn_legacy_layout() {
    local csp root f owner dest rel moves="" others="" keys=0

    for csp in $BEETLEENV_ALL_CSPS; do
        for root in "$STATE_ROOT" "$KEYS_ROOT"; do
            for f in "${root}/${csp}"/*.json "${root}/${csp}"/*.pem; do
                [ -f "$f" ] || continue
                case "$f" in
                    *.json)
                        # network.json has no name, only the ids it records.
                        owner="$(jq -r '.name // .vNetId // empty' "$f" 2>/dev/null || true)"
                        dest="$(state_dir "$csp")" ;;
                    *)
                        owner="$(basename "$f" .pem)"
                        dest="$(keys_dir "$csp")"
                        keys=1 ;;
                esac
                rel="${f#"${BEETLEENV_ROOT}/"}"
                case "$owner" in
                    "$(name_prefix_of "$csp")"*)
                        moves="${moves}
         mkdir -p ${dest#"${BEETLEENV_ROOT}/"} && mv ${rel} ${dest#"${BEETLEENV_ROOT}/"}/" ;;
                    *)
                        others="${others}
         ${rel}  (${owner:-unidentified})" ;;
                esac
            done
        done
    done

    if [ -z "$moves" ] && [ -z "$others" ]; then
        return 0
    fi

    log_warn "files in the old state/<csp>/ or keys/<csp>/ layout, which nothing reads now.
       Records and keys live under <dir>/<ns>/<prefix>/<csp>/ instead."
    if [ -n "$moves" ]; then
        printf '       Made by prefix %s - if in namespace %s, run from %s:%s\n' \
            "$BEETLEENV_NAME_PREFIX" "$BEETLEENV_NS" "$BEETLEENV_ROOT" "$moves" >&2
    fi
    if [ -n "$others" ]; then
        printf '       Made by another prefix - move them with that prefix'"'"'s .env, or delete them:%s\n' \
            "$others" >&2
    fi
    if [ "$keys" -eq 1 ]; then
        printf '       Keys under keys/ are also recreated by ./scripts/conn-info.sh <csp> --ssh.\n' >&2
    fi
}

# require_csp_env <csp> — the keys a CSP cannot be used without. Returns 1 rather
#   than exiting so up.sh can report every CSP in one pass.
require_csp_env() {
    local csp up
    csp="$(csp_lower "$1")"
    up="$(csp_upper "$csp")"

    need_env "BEETLEENV_${up}_REGION"    "# e.g. ap-northeast-2"
    need_env "BEETLEENV_${up}_ZONE"      "# e.g. ap-northeast-2a"
    # Not optional: a managed RDBMS wants subnets in two availability zones, and
    # a second zone equal to the first is refused by the CSP at creation.
    need_env "BEETLEENV_${up}_ZONE2"     "# a SECOND zone, different from _ZONE"
    need_env "BEETLEENV_${up}_VNET_CIDR" "# e.g. 10.0.0.0/16"

    if [ -n "$(csp_env "$csp" DB_ENGINES)" ]; then
        need_env "BEETLEENV_${up}_DB_PASSWORD" "# RDBMS admin password"
    fi

    assert_env "${csp} is missing settings" || return 1

    if [ "$(csp_env "$csp" ZONE)" = "$(csp_env "$csp" ZONE2)" ]; then
        log_error "BEETLEENV_${up}_ZONE and _ZONE2 are the same zone.
         A managed RDBMS needs two, and the CSP rejects the create otherwise."
        return 1
    fi
    validate_db_password "$csp" || return 1
    return 0
}

# validate_db_password <csp> — checked here because the CSP checks it far too
#   late: NCP enforces its own rule and only reports the violation when the
#   database is created, which is about half an hour into the run.
validate_db_password() {
    local csp password
    csp="$(csp_lower "$1")"
    password="$(csp_env "$csp" DB_PASSWORD)"
    if [ -z "$password" ]; then return 0; fi

    if [ "$csp" = "ncp" ]; then
        local len="${#password}"
        if [ "$len" -lt 8 ] || [ "$len" -gt 20 ]; then
            log_error "BEETLEENV_NCP_DB_PASSWORD must be 8-20 characters (it is ${len})."
            return 1
        fi
        if ! printf '%s' "$password" | grep -q '[A-Za-z]' \
           || ! printf '%s' "$password" | grep -q '[0-9]' \
           || ! printf '%s' "$password" | grep -q '[]~!@#$%^*()_=[{};:,.<>?-]'; then
            log_error "BEETLEENV_NCP_DB_PASSWORD needs a letter, a digit and one of ~!@#\$%^*()-_=[]{};:,.<>?"
            return 1
        fi
    elif [ "${#password}" -lt 8 ]; then
        log_error "BEETLEENV_$(csp_upper "$csp")_DB_PASSWORD is shorter than 8 characters; every CSP refuses that."
        return 1
    fi
    return 0
}

# preflight — run by every entry point, so a script run out of order fails with
#   a real message rather than something unrelated further in.
#
#   The namespace is deliberately NOT checked here: up.sh is what creates it, and
#   a check would make up.sh unable to run. Callers that need it use require_ns.
preflight() {
    preflight_env
    preflight_beetle
}

# ------------------------------------------------------------------------------
# state/<ns>/<prefix>/<csp>/*.json
# ------------------------------------------------------------------------------
# cb-tumblebug owns the real state, and beetleenv reads it back through the list
# APIs rather than trusting a local file. These files hold what the APIs do not
# give back: the recommendation each resource was created from.
#
# Two rules the rest of the scripts depend on:
#   - No secrets. The admin password comes from .env only, so a leaked state file
#     grants nothing - which is why the password is injected at the last moment
#     rather than stored in the recommendation on disk.
#   - Advisory only. A missing state file never blocks a deprovision; the list
#     APIs are the source of truth for what exists.
#
# The namespace and the prefix are in the path because the file names are not
# theirs: vm.json, network.json and db-mysql.json are the same for every setup,
# and two setups - a changed prefix, or the same prefix in another namespace -
# would otherwise overwrite and delete each other's records. Needs .env loaded,
# which every caller has done through preflight by the time it gets here.

setup_subdir() { printf '%s/%s/%s' "$BEETLEENV_NS" "$BEETLEENV_NAME_PREFIX" "$1"; }

state_dir()    { printf '%s/%s' "$STATE_ROOT" "$(setup_subdir "$1")"; }
state_path()   { printf '%s/%s.json' "$(state_dir "$1")" "$2"; }
state_exists() { [ -f "$(state_path "$1" "$2")" ]; }

state_read() {
    local f
    f="$(state_path "$1" "$2")"
    if [ -f "$f" ]; then cat "$f"; fi
}

# state_field <csp> <name> <jq filter> — empty when the file or field is absent.
state_field() {
    local f
    f="$(state_path "$1" "$2")"
    if [ ! -f "$f" ]; then return 0; fi
    jq -r "${3} // empty" "$f" 2>/dev/null || true
}

# state_write <csp> <name> <json> — write through a temp file so an interrupted
#   run never leaves a half-written file behind.
state_write() {
    local dir file tmp
    dir="$(state_dir "$1")"
    mkdir -p "$dir"
    file="${dir}/${2}.json"
    tmp="${dir}/.${2}.json.tmp"
    printf '%s' "$3" | jq '.' > "$tmp"
    mv -f "$tmp" "$file"
}

state_delete() {
    rm -f "$(state_path "$1" "$2")"
    prune_empty_dirs "$(state_dir "$1")" "$STATE_ROOT"
}

# state_list <csp> [glob] — base names of the state files a CSP holds.
state_list() {
    local dir pattern f base
    dir="$(state_dir "$1")"
    pattern="${2:-*}"
    if [ ! -d "$dir" ]; then return 0; fi
    for f in "$dir"/${pattern}.json; do
        if [ -f "$f" ]; then
            base="$(basename "$f")"
            printf '%s\n' "${base%.json}"
        fi
    done
}

# ------------------------------------------------------------------------------
# keys/ — VM private keys
# ------------------------------------------------------------------------------
# Unlike state/, this holds a secret. cb-tumblebug generates the VM key pair and
# hands the private half over only when asked, so a key written here is the only
# copy outside tumblebug - and the only way to log in to a node.
#
# It is kept out of state/ deliberately: state/ is documented as holding no
# secrets, and a private key sitting there would quietly break that.
#
# Written by conn-info.sh --ssh and removed by deprovision.sh along with the
# infrastructure the key opens.
#
# Laid out as keys/<ns>/<prefix>/<csp>/, the same as state/. The file name is
# the sshKeyId and already carries the prefix, but not the namespace.

KEYS_ROOT="${BEETLEENV_ROOT}/keys"

keys_dir()  { printf '%s/%s' "$KEYS_ROOT" "$(setup_subdir "$1")"; }
key_path()  { printf '%s/%s.pem' "$(keys_dir "$1")" "$2"; }

# key_write <csp> <keyId> <pem> — write the key 0600, and report what happened on
#   stdout: "written" | "unchanged". The mode is set before the content is, so
#   the key is never readable by anyone else even briefly.
key_write() {
    local dir file tmp
    dir="$(keys_dir "$1")"
    # umask rather than chmod: mkdir -p creates <ns>/ and <prefix>/ on the way,
    # and those should be 0700 too.
    (umask 077 && mkdir -p "$dir")
    chmod 700 "$dir" 2>/dev/null || true
    file="$(key_path "$1" "$2")"

    if [ -f "$file" ] && [ "$(cat "$file")" = "$3" ]; then
        chmod 600 "$file" 2>/dev/null || true
        printf 'unchanged'
        return 0
    fi

    tmp="${dir}/.${2}.pem.tmp"
    : > "$tmp"
    chmod 600 "$tmp"
    printf '%s\n' "$3" > "$tmp"
    mv -f "$tmp" "$file"
    printf 'written'
}

# key_delete <csp> <keyId> — remove one key, and every directory up to keys/ that
#   it leaves empty. Prints "removed" when there was a file to remove.
#
#   One key, not the CSP's whole directory: a key of another prefix can sit
#   beside it, and deleting this setup's VM must not lock anyone out of that one.
key_delete() {
    local dir file
    dir="$(keys_dir "$1")"
    file="$(key_path "$1" "$2")"
    rm -f "${dir}/.${2}.pem.tmp" 2>/dev/null || true
    if [ -f "$file" ]; then
        rm -f "$file"
        printf 'removed'
    fi
    prune_empty_dirs "$dir" "$KEYS_ROOT"
}

# prune_empty_dirs <dir> <root> — rmdir <dir> and its parents while they are
#   empty, stopping after <root>. rmdir refuses a non-empty directory, which is
#   the whole test.
prune_empty_dirs() {
    local dir="$1" root="$2"
    while :; do
        rmdir "$dir" 2>/dev/null || return 0
        [ "$dir" = "$root" ] && return 0
        dir="$(dirname "$dir")"
    done
}

# ------------------------------------------------------------------------------
# Polling
# ------------------------------------------------------------------------------

# poll_for <label> <timeout> <probe...> — call probe until it prints "ready".
#   The probe prints one of: ready | failed:<detail> | pending:<detail>
#   Returns 0 ready, 2 failed, 3 timed out. A timeout leaves the caller to record
#   status "partial" rather than deleting a resource that may still land.
poll_for() {
    local label="$1" timeout="$2"; shift 2
    local waited=0 res kind detail
    while :; do
        res="$("$@" 2>/dev/null)" || res="pending:probe failed"
        kind="${res%%:*}"
        if [ "$res" = "$kind" ]; then detail=""; else detail="${res#*:}"; fi

        case "$kind" in
            ready)
                log_ok "${label} is ready"
                return 0
                ;;
            failed)
                log_error "${label} reported a failure state: ${detail}"
                return 2
                ;;
        esac

        if [ "$waited" -ge "$timeout" ]; then
            log_error "${label} did not become ready within ${timeout}s (last state: ${detail:-unknown})"
            return 3
        fi
        printf '        %s ... %ss elapsed (%s)\n' "$label" "$waited" "${detail:-pending}"
        sleep "$POLL_INTERVAL"
        waited=$((waited + POLL_INTERVAL))
    done
}

# ------------------------------------------------------------------------------
# Deletion
# ------------------------------------------------------------------------------

DELETE_FAILURES=()

# record_delete_failure <label> <reason>
record_delete_failure() { DELETE_FAILURES+=("$1 - $2"); }

# report_delete_failures — reprint everything that would otherwise have scrolled
#   past during a long deprovision. Returns non-zero when anything failed.
report_delete_failures() {
    if [ "${#DELETE_FAILURES[@]}" -eq 0 ]; then return 0; fi
    log_error "${#DELETE_FAILURES[@]} resource(s) could not be deleted:"
    printf '         %s\n' "${DELETE_FAILURES[@]}" >&2
    log_warn "Delete them from the CSP console, or from cb-tumblebug directly."
    return 1
}
